// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/backup"
	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"github.com/BaryoDev/BaryoVM/internal/managed"
	"github.com/BaryoDev/BaryoVM/internal/recipe"
	"github.com/BaryoDev/BaryoVM/internal/releasestate"
	"github.com/BaryoDev/BaryoVM/internal/secretref"
	"github.com/BaryoDev/BaryoVM/internal/ui"
)

// RecipeFileName is what a directory given as a recipe is expected to hold.
const recipeFileName = "baryovm.recipe.json"

// loadRecipeFile reads and validates the recipe at path, which is a file or a directory holding
// baryovm.recipe.json. It returns the recipe and the absolute path it was read from. An unknown
// schema version is refused here, so `stack add` and `stack release` both stop on it.
func loadRecipeFile(path string) (*recipe.Recipe, string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, "", err
	}
	if fi, err := os.Stat(abs); err == nil && fi.IsDir() {
		abs = filepath.Join(abs, recipeFileName)
	}
	r, err := recipe.Load(abs)
	if err != nil {
		return nil, "", err
	}
	return r, abs, nil
}

// recipeResolver resolves the references a recipe's inputs are bound to. The references come only
// from --input, which the operator typed; a recipe's defaults are literals and are never resolved,
// so a committed recipe cannot name a local secret and no confinement is needed. cmd: stays refused
// for inputs, as the docs say.
func recipeResolver() secretref.Resolver { return secretref.Default() }

// parseInputBindings turns repeated NAME=value flags into a map.
func parseInputBindings(in []string) (map[string]string, error) {
	out := map[string]string{}
	for _, s := range in {
		name, val, ok := strings.Cut(s, "=")
		if !ok || name == "" {
			return nil, fmt.Errorf("--input %q must look like NAME=env:VAR or NAME=file:PATH", s)
		}
		if _, dup := out[name]; dup {
			return nil, fmt.Errorf("--input %s is given twice", name)
		}
		out[name] = val
	}
	return out, nil
}

type recipeData struct {
	*recipe.DeployResult
	ManagedSnapshot string         `json:"managedSnapshot,omitempty"`
	Cloud           []managed.Call `json:"cloud,omitempty"`
}

// releaseRecipe deploys a stack from its recipe. It is the recipe path of `stack release`: the
// services come from the recipe, not from a manifest, and a stack without a recipe never gets here.
func releaseRecipe(st *fleet.Stack, vm *fleet.VM, o stackReleaseOpts, out *ui.Result) error {
	// cloudCalls are the AWS or Azure CLI calls of the pre-release snapshot, kept for the envelope.
	var cloudCalls []managed.Call
	var snapshotID string
	// recipeData is the envelope data: the deploy result with the snapshot record beside it.
	data := func(res *recipe.DeployResult) any {
		if res == nil && len(cloudCalls) == 0 {
			return nil
		}
		if res == nil {
			res = &recipe.DeployResult{}
		}
		return recipeData{DeployResult: res, ManagedSnapshot: snapshotID, Cloud: cloudCalls}
	}
	fail := func(e error, res *recipe.DeployResult) error {
		*out = ui.Result{OK: false, Action: "stack release", Error: e.Error(), Data: data(res)}
		return e
	}
	switch {
	case o.Config != "":
		return fail(fmt.Errorf("--config and a recipe cannot be used together: a recipe replaces the release manifest"), nil)
	case o.Recipe == "" && st.ReleaseFile != "":
		return fail(fmt.Errorf("stack %s has both a release file and a recipe: remove one", st.Name), nil)
	case st.File != "" && st.File != recipe.DefaultComposeFile:
		return fail(fmt.Errorf("stack %s uses compose file %s, and a recipe release writes %s: re-add the stack without --file, or with --file %s", st.Name, st.File, recipe.DefaultComposeFile, recipe.DefaultComposeFile), nil)
	case o.Resume || o.Clear || o.DryRun || o.NoBuild || o.Adopt:
		return fail(fmt.Errorf("--resume, --clear, --dry-run, --no-build and --adopt are not supported with a recipe yet"), nil)
	}
	path := o.Recipe
	if path == "" {
		path = st.RecipeFile
	}
	r, _, err := loadRecipeFile(path)
	if err != nil {
		return fail(err, nil)
	}
	bind, err := parseInputBindings(o.Inputs)
	if err != nil {
		return fail(err, nil)
	}
	env, err := r.ResolveInputs(bind, recipeResolver())
	if err != nil {
		return fail(err, nil)
	}
	// Refused before any connection: a build service needs a manifest-style sync, not built yet.
	if _, err := r.ComposeYAML(); err != nil {
		return fail(err, nil)
	}

	c, err := dialRelease(vm)
	if err != nil {
		return fail(err, nil)
	}
	defer c.Close()

	// The start lock is held for the whole deploy, the same lock the manifest release takes, so two
	// releases of one stack cannot write the same .new and .prev files. There is no step record: a
	// recipe deploy rolls itself back, and there is nothing to resume.
	loc := releasestate.For(st.Dir, st.Name, st.Sudo)
	if err := releasestate.Acquire(c, loc); err != nil {
		return fail(err, nil)
	}
	defer func() { _ = releasestate.Unlock(c, loc) }()

	// A managed database beside the VM is snapshotted first, before the VM backup and before
	// anything changes, as the manifest path does. --no-backup skips it with the rest.
	if !o.NoBackup && st.Managed != nil {
		spec := *st.Managed
		if err := ui.Step("snapshot managed database "+spec.Database, func() error {
			id, calls, e := managedSnapshot(spec)
			snapshotID, cloudCalls = id, calls
			return e
		}); err != nil {
			return fail(err, nil)
		}
	}

	// Before anything changes on the host, as the manifest path does: a backup taken after the new
	// services start already contains their migrations.
	if !o.NoBackup && hasBackupStrategies(st) {
		bcfg := backupConfig(st)
		if backup.SkipMissingContainers(c, &bcfg) {
			ui.Notef("no database container named %s yet, skipping the pre-release backup", bcfg.DBContainer)
		} else if err := ui.Step("backup", func() error {
			_, e := backup.Backup(c, bcfg)
			return e
		}); err != nil {
			return fail(fmt.Errorf("pre-release backup failed: %s", backupCause(err, bcfg.DBContainer)), nil)
		}
	}

	var res *recipe.DeployResult
	err = ui.Step("deploy recipe "+r.Name, func() error {
		var e error
		res, e = recipe.Deploy(c, recipe.DeployInput{
			Recipe: r, Dir: st.Dir, Sudo: st.Sudo, Runtime: st.Runtime, Env: env, Clock: releaseClock,
		})
		return e
	})
	if err != nil {
		return fail(err, res)
	}
	*out = ui.Result{OK: true, Action: "stack release", Message: st.Name + " released from recipe " + r.Name, Data: data(res)}
	ui.Successf("%s: release done", st.Name)
	return nil
}
