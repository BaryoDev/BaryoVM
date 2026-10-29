// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"github.com/BaryoDev/BaryoVM/internal/recipe"
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

// recipeResolver resolves the references a recipe's inputs are bound to. Recipe inputs belong to a
// file in the app repository, so cmd: is refused, the same as for release manifests.
//
// TODO: confine this to dir once secretref.Resolver has Confine(root): env: names must start with
// BARYOVM_, and file: paths must be relative and stay inside dir after symlinks are resolved.
func recipeResolver(dir string) secretref.Resolver {
	_ = dir
	return fileResolver()
}

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

// releaseRecipe deploys a stack from its recipe. It is the recipe path of `stack release`: the
// services come from the recipe, not from a manifest, and a stack without a recipe never gets here.
func releaseRecipe(st *fleet.Stack, vm *fleet.VM, o stackReleaseOpts, out *ui.Result) error {
	fail := func(e error, data any) error {
		*out = ui.Result{OK: false, Action: "stack release", Error: e.Error(), Data: data}
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
	r, abs, err := loadRecipeFile(path)
	if err != nil {
		return fail(err, nil)
	}
	bind, err := parseInputBindings(o.Inputs)
	if err != nil {
		return fail(err, nil)
	}
	env, err := r.ResolveInputs(bind, recipeResolver(filepath.Dir(abs)))
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

	var res *recipe.DeployResult
	err = ui.Step("deploy recipe "+r.Name, func() error {
		var e error
		res, e = recipe.Deploy(c, recipe.DeployInput{
			Recipe: r, Dir: st.Dir, Sudo: st.Sudo, Env: env, Clock: releaseClock,
		})
		return e
	})
	if err != nil {
		return fail(err, res)
	}
	*out = ui.Result{OK: true, Action: "stack release", Message: st.Name + " released from recipe " + r.Name, Data: res}
	ui.Successf("%s: release done", st.Name)
	return nil
}
