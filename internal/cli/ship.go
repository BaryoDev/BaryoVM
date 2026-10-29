// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"github.com/BaryoDev/BaryoVM/internal/releasestate"
	"github.com/BaryoDev/BaryoVM/internal/secretref"
	"github.com/BaryoDev/BaryoVM/internal/ship"
	"github.com/BaryoDev/BaryoVM/internal/ui"
	"github.com/spf13/cobra"
)

// publishSpecFile is read from the repository when neither --file nor the stack carries a spec.
const publishSpecFile = "baryovm.publish.json"

// newShipDeps builds the seams for one run. A variable so tests replace every outside call.
var newShipDeps = func(dir string, spec ship.Spec, tokenRef string, yes bool, timeout time.Duration) (ship.Deps, error) {
	h := &http.Client{Timeout: 30 * time.Second}
	reg, err := ship.NewRegistry(spec, h)
	if err != nil {
		return ship.Deps{}, err
	}
	d := ship.Deps{
		Dir: dir, Git: ship.ExecGit{}, Registry: reg, ReadFile: os.ReadFile,
		// Resolved only after the gate passed and only on a real run. The token is held in memory for
		// the release call and the run reads; it is never stored or printed.
		GitHub: func() (ship.GitHub, error) {
			tok, err := secretref.Default().WithCmd().Resolve(tokenRef)
			if err != nil {
				return nil, fmt.Errorf("github token: %w (pass --token-ref, for example env:GITHUB_TOKEN)", err)
			}
			return ship.APIGitHub{Token: strings.TrimSpace(tok), H: h}, nil
		},
		Deployed: deployedCommit,
		Timings:  ship.Timings{Watch: timeout},
	}
	if !yes {
		d.Confirm = confirmShip
	}
	return d, nil
}

// confirmShip shows what will ship and asks. Without a terminal there is nobody to ask, so the
// caller must pass --yes.
func confirmShip(summary string) (bool, error) {
	if ui.JSON() || !ui.IsTTY() {
		return false, errors.New("ship tags and pushes: pass --yes to go ahead without a terminal, or use --dry-run to see the plan")
	}
	fmt.Fprint(os.Stderr, summary+"\nTag and push? [y/N] ")
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.EqualFold(strings.TrimSpace(line), "y"), nil
}

// deployedCommit reads the commit the named stack last released from its VM.
func deployedCommit(ctx context.Context, stack string) (string, bool, error) {
	store, err := fleet.Load()
	if err != nil {
		return "", false, err
	}
	st := store.FindStack(stack)
	if st == nil {
		return "", false, fmt.Errorf("no stack named %q: register it with `baryovm stack add`", stack)
	}
	vm := store.Find(st.VM)
	if vm == nil {
		return "", false, fmt.Errorf("stack %q is on VM %q, which is not registered", stack, st.VM)
	}
	c, err := dialRelease(vm)
	if err != nil {
		return "", false, err
	}
	defer c.Close()
	d, err := releasestate.ReadDeployed(c, statusInput(st).Loc)
	if err != nil || d == nil {
		return "", false, err
	}
	return d.Commit, true, nil
}

// loadPublishSpec resolves the spec: --file, then the stack's setting, then the repository file.
func loadPublishSpec(name, file, dir string) (ship.Spec, string, error) {
	if file != "" {
		s, err := readSpecFile(file)
		return s, "--file " + file, err
	}
	if store, err := fleet.Load(); err == nil {
		if st := store.FindStack(name); st != nil && st.Publish != nil {
			return *st.Publish, "stack " + name, nil
		}
	}
	p := filepath.Join(dir, publishSpecFile)
	if _, err := os.Stat(p); err == nil {
		s, err := readSpecFile(p)
		return s, p, err
	}
	return ship.Spec{}, "", fmt.Errorf("no publish spec for %q: pass --file, run `baryovm stack set-publish %s --file spec.json`, or add %s to the repository", name, name, publishSpecFile)
}

func readSpecFile(path string) (ship.Spec, error) {
	b, err := os.ReadFile(path) // #nosec G304 -- the spec file is the user's own argument
	if err != nil {
		return ship.Spec{}, err
	}
	var s ship.Spec
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return ship.Spec{}, fmt.Errorf("read %s: %w", path, err)
	}
	return s, s.Validate()
}

type shipFlags struct {
	version, dir, file, tokenRef string
	dry, yes                     bool
	timeout                      time.Duration
}

func (f *shipFlags) bind(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.version, "version", "", "version to release (default: the one the project file declares)")
	cmd.Flags().StringVar(&f.dir, "repo", ".", "repository directory")
	cmd.Flags().StringVar(&f.file, "file", "", "publish spec JSON (default: the stack's setting, then "+publishSpecFile+")")
	cmd.Flags().StringVar(&f.tokenRef, "token-ref", "cmd:gh auth token", "secret reference for the GitHub token (env:, file:, cmd:)")
	cmd.Flags().BoolVar(&f.dry, "dry-run", false, "run every preflight check and print the plan; tag and push nothing")
	cmd.Flags().BoolVar(&f.yes, "yes", false, "skip the confirmation before tagging")
	cmd.Flags().DurationVar(&f.timeout, "timeout", 30*time.Minute, "how long to watch the workflow run")
}

// runShip is the whole of `ship` and `stack publish`: they differ only in the stage.
func runShip(action, stage, name string, f shipFlags) error {
	fail := func(err error, data any) error {
		ui.Emit(ui.Result{OK: false, Action: action, Error: err.Error(), Data: data})
		return err
	}
	if err := validStackName(name); err != nil {
		return fail(err, nil)
	}
	dir, err := filepath.Abs(f.dir)
	if err != nil {
		return fail(err, nil)
	}
	spec, src, err := loadPublishSpec(name, f.file, dir)
	if err != nil {
		return fail(err, nil)
	}
	d, err := newShipDeps(dir, spec, f.tokenRef, f.yes, f.timeout)
	if err != nil {
		return fail(err, nil)
	}
	// Ctrl-C ends the waits at once. A tag already pushed stays, and the same command continues.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var rec *ship.Record
	runErr := ui.Step(action+": "+name, func() error {
		var e error
		rec, e = ship.Run(ctx, d, spec, ship.Options{Name: name, Version: f.version, DryRun: f.dry, Stage: stage})
		return e
	})
	if rec != nil && !rec.DryRun && len(rec.Steps) > 0 {
		if rec.Version != "" {
			if p, err := ship.RecordPath(baryovmHome(), name, rec.Version); err != nil {
				ui.Warnf("the record was not saved: %v", err)
			} else if err := writeShipRecord(p, rec); err != nil {
				ui.Warnf("the record was not saved: %v", err)
			}
		}
	}
	if runErr != nil {
		return fail(runErr, rec)
	}
	msg := fmt.Sprintf("%s %s", name, rec.Tag)
	switch {
	case rec.DryRun:
		msg += ": dry run passed, nothing was tagged (spec from " + src + ")"
	case stage == ship.StageTag:
		msg += ": tagged and pushed, the repository's workflow publishes"
	default:
		msg += ": shipped and verified"
	}
	if ui.JSON() {
		ui.Emit(ui.Result{OK: true, Action: action, Message: msg, Data: rec})
		return nil
	}
	for _, c := range rec.Checks {
		ui.Notef("%s: %s", c.Name, c.Observed)
	}
	for _, p := range rec.Plan {
		ui.Notef("plan: %s", p)
	}
	ui.Successf("%s", msg)
	return nil
}

// writeShipRecord stores the record where `baryovm prove` will read it, 0600, via a temp file.
func writeShipRecord(path string, rec *ship.Record) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func newShipCmd() *cobra.Command {
	var f shipFlags
	cmd := &cobra.Command{
		Use:   "ship <name>",
		Short: "Run the release gate, tag, watch the publish run, verify the registry, create the GitHub release",
		Long: "Checks that the release is safe (clean tree on the default branch and synced, version not\n" +
			"already tagged or published, changelog section for that version, release notes), then tags\n" +
			"and pushes the tag, watches the workflow run, asserts its publish step ran and was not\n" +
			"skipped, polls the registry until every package resolves, reads the published artifact's\n" +
			"metadata against the spec's assertions, and creates the GitHub release.\n\n" +
			"BaryoVM never publishes and never holds a registry credential: the repository's own workflow\n" +
			"publishes, and BaryoVM only reads the registry. The GitHub token comes from --token-ref.\n" +
			"A rerun after an interrupted watch finds the tag at HEAD and picks up from the watch.\n" +
			"What was asserted, with the values seen, is saved under $BARYOVM_HOME/ship/<name>/.",
		Example: "  baryovm ship shop --dry-run\n  baryovm ship shop --yes -o json",
		Args:    cobra.ExactArgs(1),
		RunE:    func(cmd *cobra.Command, args []string) error { return runShip("ship", ship.StageFull, args[0], f) },
	}
	f.bind(cmd)
	return cmd
}

func newStackPublishCmd() *cobra.Command {
	var f shipFlags
	cmd := &cobra.Command{
		Use:   "publish <name>",
		Short: "Run the release gate, then tag and push the tag",
		Long: "The same gate as `baryovm ship`, then the tag and its push, and nothing after: the\n" +
			"repository's workflow publishes. Use `baryovm ship` to also watch, verify and release.",
		Example: "  baryovm stack publish shop --dry-run\n  baryovm stack publish shop --yes",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runShip("stack publish", ship.StageTag, args[0], f)
		},
	}
	f.bind(cmd)
	return cmd
}

// newStackSetPublishCmd stores the stack's publish spec in fleet.json.
func newStackSetPublishCmd() *cobra.Command {
	var file string
	var clear bool
	cmd := &cobra.Command{
		Use:   "set-publish <name>",
		Short: "Set a stack's publish spec read by ship and stack publish",
		Long: "Stores a publish spec (registry, versionFrom, changelog, packages, assertions, requireDeployed)\n" +
			"on the stack. --clear removes it.",
		Example: "  baryovm stack set-publish shop --file publish.json\n  baryovm stack set-publish shop --clear",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			fail := func(err error) error {
				ui.Emit(ui.Result{OK: false, Action: "stack set-publish", Error: err.Error()})
				return err
			}
			if clear == (file != "") {
				return fail(fmt.Errorf("give exactly one of --file <spec.json> or --clear"))
			}
			store, err := fleet.Load()
			if err != nil {
				return fail(err)
			}
			st := store.FindStack(args[0])
			if st == nil {
				return fail(fmt.Errorf("no stack named %q", args[0]))
			}
			if clear {
				st.Publish = nil
			} else {
				spec, err := readSpecFile(file)
				if err != nil {
					return fail(err)
				}
				st.Publish = &spec
			}
			if err := store.Save(); err != nil {
				return fail(err)
			}
			if ui.JSON() {
				ui.Emit(ui.Result{OK: true, Action: "stack set-publish", Message: st.Name, Data: map[string]any{"publish": st.Publish}})
				return nil
			}
			ui.Successf("%s: publish spec %s", st.Name, map[bool]string{true: "set", false: "cleared"}[st.Publish != nil])
			return nil
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "JSON file holding the publish spec")
	cmd.Flags().BoolVar(&clear, "clear", false, "remove the stack's publish spec")
	return cmd
}
