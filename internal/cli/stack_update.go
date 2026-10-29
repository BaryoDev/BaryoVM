// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/BaryoDev/BaryoVM/internal/backup"
	"github.com/BaryoDev/BaryoVM/internal/compose"
	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"github.com/BaryoDev/BaryoVM/internal/rollback"
	"github.com/BaryoDev/BaryoVM/internal/ui"
	"github.com/BaryoDev/BaryoVM/internal/update"
	"github.com/BaryoDev/BaryoVM/internal/verify"
	"github.com/spf13/cobra"
)

func newStackUpdateCmd() *cobra.Command {
	var svcs []string
	var auto, dryRun, noBackup bool
	var attempts int
	var delay time.Duration
	var notifyURL, notifyOn string

	cmd := &cobra.Command{
		Use:   "update <name>",
		Short: "Pull newer images and recreate, only if they come up healthy",
		Long: "Pulls the stack's images and, when any actually changed, backs up the database,\n" +
			"recreates the affected services, and waits for the stack's health URL. If it does not\n" +
			"come back, the previous images are restored and the stack is checked again.\n\n" +
			"An unchanged stack is left alone: no recreate, no backup, no downtime.\n\n" +
			"--auto is the form a scheduler runs. It refuses any stack not marked autoUpdate, any stack\n" +
			"with no healthUrl, and any stack with no database backup configured, since an unattended\n" +
			"update that cannot tell a healthy start from a crash loop is worse than no update at all.\n" +
			"A stack that genuinely has no database says so with `stack set-update --no-database`.\n" +
			"--dry-run is exempt from both backup refusals: it recreates nothing, so it has nothing\n" +
			"to go back from.\n\n" +
			"--notify (or BARYOVM_NOTIFY_URL) posts the JSON result envelope to a webhook when an --auto\n" +
			"run fails or rolls back, so a 3am failure reaches a person. https only (http for localhost).\n" +
			"The URL usually holds a token, so only its scheme and host are ever printed.",
		Args: cobra.ExactArgs(1),
		Example: "  baryovm stack update playground --dry-run\n" +
			"  baryovm stack update playground\n" +
			"  baryovm stack update playground --auto    # what cron runs\n" +
			"  baryovm stack update playground --auto --notify \"$WEBHOOK_URL\" -o json",
		RunE: func(cmd *cobra.Command, args []string) error {
			// A flag left alone must not override the stack's verify spec, which carries its own
			// patience. 0 means "not given": the spec's value, then the default.
			if !cmd.Flags().Changed("health-attempts") {
				attempts = 0
			}
			if !cmd.Flags().Changed("health-delay") {
				delay = 0
			}
			nopts, err := resolveNotify(cmd.Flags().Changed("notify"), notifyURL, notifyOn, auto, os.Getenv)
			if err != nil {
				return err
			}
			return runStackUpdateNotify(args[0], svcs, auto, dryRun, noBackup, attempts, delay, nopts)
		},
	}
	cmd.Flags().StringSliceVar(&svcs, "service", nil, "limit to these services (defaults to the stack's updateServices)")
	cmd.Flags().BoolVar(&auto, "auto", false, "unattended: refuse stacks not marked autoUpdate")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report what would update, change nothing")
	cmd.Flags().BoolVar(&noBackup, "no-backup", false, "skip the pre-update database backup")
	cmd.Flags().StringVar(&notifyURL, "notify", "", "with --auto, POST the result envelope to this webhook on failure (or set BARYOVM_NOTIFY_URL)")
	cmd.Flags().StringVar(&notifyOn, "notify-on", notifyFailure, "when to notify: failure or always")
	cmd.Flags().IntVar(&attempts, "health-attempts", 20, "health checks before declaring failure")
	cmd.Flags().DurationVar(&delay, "health-delay", 3*time.Second, "wait between health checks")
	return cmd
}

func runStackUpdate(name string, svcs []string, auto, dryRun, noBackup bool, attempts int, delay time.Duration) error {
	return runStackUpdateNotify(name, svcs, auto, dryRun, noBackup, attempts, delay, notifyOptions{})
}

// runStackUpdateNotify runs the update, then reports the outcome to the notify URL when one is set.
// The core never emits the final envelope itself: the notification result belongs in it, so it is
// written once, after the notification has been tried.
func runStackUpdateNotify(name string, svcs []string, auto, dryRun, noBackup bool, attempts int, delay time.Duration, nopts notifyOptions) error {
	const action = "stack update"
	out, runErr := runStackUpdateCore(name, svcs, auto, dryRun, noBackup, attempts, delay)

	var nres *notifyResult
	if auto && nopts.URL != "" {
		env := out.env
		if !out.have {
			env = ui.Result{OK: runErr == nil, Action: action, Message: name}
			if runErr != nil {
				env.Error = runErr.Error()
			}
		}
		if shouldNotify(nopts.On, env.OK) {
			nres = sendNotify(nopts.URL, env)
		}
	}

	if nres != nil {
		if nres.OK {
			ui.Notef("notified %s", nres.Target)
		} else {
			ui.Warnf("notification to %s failed: %s", nres.Target, nres.Error)
		}
	}
	// A failed notification makes the exit non-zero, but never replaces the update's own error.
	if nres != nil && !nres.OK && runErr == nil {
		runErr = fmt.Errorf("update finished but the notification to %s failed: %s", nres.Target, nres.Error)
		if out.have {
			out.env.OK = false
			out.env.Error = runErr.Error()
		}
	}

	if out.have && (ui.JSON() || !out.printed) {
		env := out.env
		if nres != nil {
			if m, ok := env.Data.(map[string]any); ok {
				m["notify"] = nres
			} else if env.Data == nil {
				env.Data = map[string]any{"notify": nres}
			}
		}
		ui.Emit(env)
	}
	return runErr
}

// updateOutcome is the envelope the update ended with. have is false when the failure was a plain
// error with no envelope (the caller's fallback reports it). printed is true when the human-mode
// message has already been shown.
type updateOutcome struct {
	env     ui.Result
	have    bool
	printed bool
}

func runStackUpdateCore(name string, svcs []string, auto, dryRun, noBackup bool, attempts int, delay time.Duration) (updateOutcome, error) {
	const action = "stack update"
	fail := func(err error) (updateOutcome, error) {
		return updateOutcome{env: ui.Result{OK: false, Action: action, Error: err.Error()}, have: true}, err
	}
	store, err := fleet.Load()
	if err != nil {
		return updateOutcome{}, err
	}
	st := store.FindStack(name)
	if st == nil {
		return updateOutcome{}, fmt.Errorf("no stack named %q: register it with `baryovm stack add %s --vm ... --path ...`", name, name)
	}
	vm := store.Find(st.VM)
	if vm == nil {
		return updateOutcome{}, fmt.Errorf("stack %q references unknown VM %q", name, st.VM)
	}
	if err := requireLinux(vm, action); err != nil {
		return updateOutcome{}, err
	}
	if vm.Forced() {
		return forcedUpdate(vm, name, svcs, dryRun, noBackup)
	}

	if len(svcs) == 0 {
		svcs = st.UpdateServices
	}
	strategies, declared := st.BackupStrategies()
	hasBackup := len(strategies) > 0
	// An explicit empty list (or the legacy noDatabase flag) is a stack that said there is nothing
	// to back up. A stack that said nothing is refused by --auto below.
	noDatabase := declared && !hasBackup

	// Refuse an unattended run before opening a connection, so a misconfigured cron entry is a clean
	// no-op rather than a partially applied change.
	if auto && !st.AutoUpdate {
		err := fmt.Errorf("%w: %s is not marked autoUpdate: set it deliberately with `baryovm stack set-update %s --auto`", update.ErrNotAutoUpdatable, name, name)
		return fail(err)
	}
	// The same builder `stack release` uses, so the two cannot disagree about what healthy means.
	// Built before any connection: a spec that cannot be read is refused with nothing touched.
	vctx := verify.Context{
		Mode: verify.ForUpdate, HealthURL: st.HealthURL, Dir: st.Dir, Sudo: st.Sudo,
		DefaultAttempts: 20, DefaultDelay: 3 * time.Second,
	}
	vplan, verr := st.Verify.Plan(vctx)
	if verr != nil {
		return fail(verr)
	}
	if auto && vplan.Empty() {
		err := fmt.Errorf("%w: %s has no healthUrl or verify spec", update.ErrNoHealthCheck, name)
		return fail(err)
	}
	// Before the configuration checks below, because this one is about the flags just
	// typed. Reported second, it sent an operator off to register a database or record
	// that there is none, and --no-backup was still refused when they came back.
	//
	// A dry run is exempt for the same reason as the configuration check: it recreates
	// nothing, so there is nothing to go back from.
	if auto && !dryRun && noBackup {
		err := fmt.Errorf("--no-backup cannot be combined with --auto: an unattended update keeps a way back")
		return fail(err)
	}
	// A dry run is exempt: it recreates nothing, so it has nothing to go back from.
	if auto && !dryRun && !hasBackup && !noDatabase {
		err := fmt.Errorf("%w: %s declares no backup: register a database with `baryovm stack add`, declare strategies with `baryovm stack set-backup %s --file backup.json`, or record that it has nothing to back up with `baryovm stack set-update %s --no-database`", update.ErrNoBackup, name, name, name)
		return fail(err)
	}

	var res update.Result
	step := fmt.Sprintf("updating stack %s", name)
	if dryRun {
		step = fmt.Sprintf("checking stack %s for updates", name)
	}

	runErr := ui.Step(step, func() error {
		c, err := dialChecked(vm)
		if err != nil {
			return err
		}
		defer c.Close()

		runner := update.SSHRunner{
			Client: c,
			Stack:  composeStack(st),
			Verify: vplan,
		}
		if hasBackup {
			runner.DoBackup = func() (string, error) { return backup.Backup(c, backupConfig(st)) }
		}

		var rec update.Runner = runner
		if !dryRun {
			rec = &recordingRunner{Runner: runner, save: func(imgs []compose.Image) {
				kept, e := rollback.Save(c, backupConfig(st), rollback.FromCompose(name, "update", imgs))
				if e != nil {
					ui.Warnf("could not record the images to roll back to: %v", e)
				} else if kept {
					ui.Notef("keeping the rollback record from before the half-applied release that was cleared")
				}
			}}
		}

		res, err = update.Run(rec, update.Options{
			Services:       svcs,
			Auto:           auto,
			AutoUpdate:     st.AutoUpdate,
			HasHealthCheck: !vplan.Empty(),
			HasBackup:      hasBackup,
			NoDatabase:     noDatabase,
			SkipBackup:     noBackup,
			DryRun:         dryRun,
			HealthAttempts: pick(attempts, vplan.Budget.Attempts),
			HealthDelay:    pickDur(delay, vplan.Budget.Delay),
			HealthTimeout:  vplan.Budget.Timeout,
		})
		return err
	})

	return reportUpdate(action, name, res, runErr)
}

// reportUpdate renders the outcome of an update, whether this process ran it or a forced VM did.
func reportUpdate(action, name string, res update.Result, runErr error) (updateOutcome, error) {
	if ui.JSON() {
		data := updateData(res)
		if runErr != nil {
			return updateOutcome{env: ui.Result{OK: false, Action: action, Message: name, Error: runErr.Error(), Data: data}, have: true}, runErr
		}
		return updateOutcome{env: ui.Result{OK: true, Action: action, Message: name, Data: data}, have: true}, nil
	}

	if runErr != nil {
		if res.RolledBack {
			ui.Errorf("%s: update failed and was rolled back: %v", name, runErr)
		} else {
			ui.Errorf("%s: update failed: %v", name, runErr)
		}
		env := ui.Result{OK: false, Action: action, Message: name, Error: runErr.Error(), Data: updateData(res)}
		return updateOutcome{env: env, have: true, printed: true}, runErr
	}

	switch {
	case res.Skipped == "already up to date":
		ui.Successf("%s: already up to date (checked %s)", name, strings.Join(res.Checked, ", "))
	case res.Skipped == "dry run":
		ui.Successf("%s: update available for %s", name, strings.Join(res.Services, ", "))
	case res.Updated:
		ui.Successf("%s: updated %s (%s)", name, strings.Join(res.Services, ", "), res.Health)
	default:
		ui.Successf("%s: nothing to do", name)
	}
	env := ui.Result{OK: true, Action: action, Message: name, Data: updateData(res)}
	return updateOutcome{env: env, have: true, printed: true}, nil
}

// pick is the flag when it was given, else the plan's value, which already holds the spec's setting
// or the default.
func pick(flag, planned int) int {
	if flag > 0 {
		return flag
	}
	return planned
}

func pickDur(flag, planned time.Duration) time.Duration {
	if flag > 0 {
		return flag
	}
	return planned
}

// recordingRunner remembers the images update.Run read first (they are what runs before any Up)
// and hands them to save just before the first Up, so `stack rollback` has a target that outlives
// the CLI. A failed save only warns: it must not stop an update.
type recordingRunner struct {
	update.Runner
	imgs  []compose.Image
	save  func([]compose.Image)
	saved bool
}

func (r *recordingRunner) Images(svcs []string) ([]compose.Image, error) {
	imgs, err := r.Runner.Images(svcs)
	if err == nil && !r.saved && r.imgs == nil {
		r.imgs = imgs
	}
	return imgs, err
}

func (r *recordingRunner) Up(svcs []string) (string, error) {
	if !r.saved {
		r.saved = true
		r.save(r.imgs)
	}
	return r.Runner.Up(svcs)
}

// newStackSetUpdateCmd configures the update policy, so the risky bit (marking a stack as safe to
// update unattended) is an explicit, separate act rather than a flag buried in `stack add`.
func newStackSetUpdateCmd() *cobra.Command {
	var auto, noAuto, noDB bool
	var healthURL string
	var svcs []string

	cmd := &cobra.Command{
		Use:   "set-update <name>",
		Short: "Set a stack's update policy (autoUpdate, health URL)",
		Args:  cobra.ExactArgs(1),
		Example: "  baryovm stack set-update playground --health-url http://127.0.0.1:5005/health --auto\n" +
			"  baryovm stack set-update club --health-url http://127.0.0.1:8091/health --no-auto",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := fleet.Load()
			if err != nil {
				return err
			}
			st := store.FindStack(args[0])
			if st == nil {
				return fmt.Errorf("no stack named %q", args[0])
			}
			if auto && noAuto {
				return fmt.Errorf("--auto and --no-auto are mutually exclusive")
			}
			if cmd.Flags().Changed("health-url") {
				st.HealthURL = healthURL
			}
			if cmd.Flags().Changed("service") {
				st.UpdateServices = svcs
			}
			if cmd.Flags().Changed("no-database") {
				switch {
				case st.Backup == nil:
					st.NoDatabase = noDB
				case noDB:
					// A declared list becomes the explicit empty one.
					empty := []backup.Strategy{}
					st.Backup, st.NoDatabase = &empty, false
				case len(*st.Backup) == 0:
					st.Backup, st.NoDatabase = nil, false
				}
			}
			if auto {
				if st.HealthURL == "" && !st.Verify.HasChecks() {
					return fmt.Errorf("set --health-url (or a verify spec with `stack set-verify`) before --auto: an unattended update needs a way to tell success from a crash loop")
				}
				st.AutoUpdate = true
			}
			if noAuto {
				st.AutoUpdate = false
			}
			if err := store.Save(); err != nil {
				return err
			}
			if ui.JSON() {
				ui.Emit(ui.Result{OK: true, Action: "stack set-update", Message: st.Name, Data: map[string]string{
					"autoUpdate": fmt.Sprint(st.AutoUpdate),
					"healthUrl":  st.HealthURL,
					"noDatabase": fmt.Sprint(noDatabaseOf(st)),
				}})
				return nil
			}
			ui.Successf("%s: autoUpdate=%v healthUrl=%s noDatabase=%v", st.Name, st.AutoUpdate, st.HealthURL, noDatabaseOf(st))
			return nil
		},
	}
	cmd.Flags().BoolVar(&auto, "auto", false, "allow unattended updates (requires --health-url)")
	cmd.Flags().BoolVar(&noAuto, "no-auto", false, "disallow unattended updates")
	cmd.Flags().StringVar(&healthURL, "health-url", "", "URL probed from the VM after an update, e.g. http://127.0.0.1:8091/health")
	cmd.Flags().StringSliceVar(&svcs, "service", nil, "limit updates to these services")
	cmd.Flags().BoolVar(&noDB, "no-database", false, "this stack has no database, so --auto may update it with no backup (--no-database=false to undo)")
	return cmd
}

// updateData is the envelope data of `stack update`. The string fields are the published shape and
// stay as they are, "true" and a comma-joined list included. "result" is the same outcome with real
// booleans and lists, which is what new consumers should read.
func updateData(res update.Result) map[string]any {
	return map[string]any{
		"result":     res,
		"updated":    fmt.Sprint(res.Updated),
		"rolledBack": fmt.Sprint(res.RolledBack),
		"services":   strings.Join(res.Services, ","),
		"checked":    strings.Join(res.Checked, ","),
		"health":     res.Health,
		"skipped":    res.Skipped,
	}
}

// noDatabaseOf reports whether the stack says it has nothing to back up.
func noDatabaseOf(st *fleet.Stack) bool {
	list, declared := st.BackupStrategies()
	return declared && len(list) == 0
}
