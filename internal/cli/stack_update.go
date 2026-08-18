package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/BaryoDev/BaryoVM/internal/backup"
	"github.com/BaryoDev/BaryoVM/internal/compose"
	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"github.com/BaryoDev/BaryoVM/internal/sshx"
	"github.com/BaryoDev/BaryoVM/internal/ui"
	"github.com/BaryoDev/BaryoVM/internal/update"
	"github.com/spf13/cobra"
)

func newStackUpdateCmd() *cobra.Command {
	var svcs []string
	var auto, dryRun, noBackup bool
	var attempts int
	var delay time.Duration

	cmd := &cobra.Command{
		Use:   "update <name>",
		Short: "Pull newer images and recreate, only if they come up healthy",
		Long: "Pulls the stack's images and, when any actually changed, backs up the database,\n" +
			"recreates the affected services, and waits for the stack's health URL. If it does not\n" +
			"come back, the previous images are restored and the stack is checked again.\n\n" +
			"An unchanged stack is left alone: no recreate, no backup, no downtime.\n\n" +
			"--auto is the form a scheduler runs. It refuses any stack not marked autoUpdate, and any\n" +
			"stack with no healthUrl, since an unattended update that cannot tell a healthy start from\n" +
			"a crash loop is worse than no update at all.",
		Args: cobra.ExactArgs(1),
		Example: "  baryovm stack update playground --dry-run\n" +
			"  baryovm stack update playground\n" +
			"  baryovm stack update playground --auto    # what cron runs",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStackUpdate(args[0], svcs, auto, dryRun, noBackup, attempts, delay)
		},
	}
	cmd.Flags().StringSliceVar(&svcs, "service", nil, "limit to these services (defaults to the stack's updateServices)")
	cmd.Flags().BoolVar(&auto, "auto", false, "unattended: refuse stacks not marked autoUpdate")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report what would update, change nothing")
	cmd.Flags().BoolVar(&noBackup, "no-backup", false, "skip the pre-update database backup")
	cmd.Flags().IntVar(&attempts, "health-attempts", 20, "health checks before declaring failure")
	cmd.Flags().DurationVar(&delay, "health-delay", 3*time.Second, "wait between health checks")
	return cmd
}

func runStackUpdate(name string, svcs []string, auto, dryRun, noBackup bool, attempts int, delay time.Duration) error {
	const action = "stack update"

	store, err := fleet.Load()
	if err != nil {
		return err
	}
	st := store.FindStack(name)
	if st == nil {
		return fmt.Errorf("no stack named %q: register it with `baryovm stack add %s --vm ... --path ...`", name, name)
	}
	vm := store.Find(st.VM)
	if vm == nil {
		return fmt.Errorf("stack %q references unknown VM %q", name, st.VM)
	}

	if len(svcs) == 0 {
		svcs = st.UpdateServices
	}
	hasBackup := st.DBContainer != "" && st.DBName != ""

	// Refuse an unattended run before opening a connection, so a misconfigured cron entry is a clean
	// no-op rather than a partially applied change.
	if auto && !st.AutoUpdate {
		err := fmt.Errorf("%w: %s is not marked autoUpdate: set it deliberately with `baryovm stack set-update %s --auto`", update.ErrNotAutoUpdatable, name, name)
		ui.Emit(ui.Result{OK: false, Action: action, Error: err.Error()})
		return err
	}
	if auto && st.HealthURL == "" {
		err := fmt.Errorf("%w: %s has no healthUrl", update.ErrNoHealthCheck, name)
		ui.Emit(ui.Result{OK: false, Action: action, Error: err.Error()})
		return err
	}
	if auto && noBackup {
		return fmt.Errorf("--no-backup cannot be combined with --auto: an unattended update keeps a way back")
	}

	var res update.Result
	step := fmt.Sprintf("updating stack %s", name)
	if dryRun {
		step = fmt.Sprintf("checking stack %s for updates", name)
	}

	runErr := ui.Step(step, func() error {
		c, err := sshx.Dial(vm.Target())
		if err != nil {
			return err
		}
		defer c.Close()

		runner := update.SSHRunner{
			Client:    c,
			Stack:     compose.Stack{Dir: st.Dir, File: st.File, Sudo: st.Sudo},
			HealthURL: st.HealthURL,
		}
		if hasBackup {
			runner.DoBackup = func() (string, error) { return backup.Backup(c, backupConfig(st)) }
		}

		res, err = update.Run(runner, update.Options{
			Services:       svcs,
			Auto:           auto,
			AutoUpdate:     st.AutoUpdate,
			HasHealthCheck: st.HealthURL != "",
			HasBackup:      hasBackup,
			SkipBackup:     noBackup,
			DryRun:         dryRun,
			HealthAttempts: attempts,
			HealthDelay:    delay,
		})
		return err
	})

	if ui.JSON() {
		data := map[string]string{
			"updated":    fmt.Sprint(res.Updated),
			"rolledBack": fmt.Sprint(res.RolledBack),
			"services":   strings.Join(res.Services, ","),
			"checked":    strings.Join(res.Checked, ","),
			"health":     res.Health,
			"skipped":    res.Skipped,
		}
		if runErr != nil {
			ui.Emit(ui.Result{OK: false, Action: action, Message: name, Error: runErr.Error(), Data: data})
			return runErr
		}
		ui.Emit(ui.Result{OK: true, Action: action, Message: name, Data: data})
		return nil
	}

	if runErr != nil {
		if res.RolledBack {
			ui.Errorf("%s: update failed and was rolled back: %v", name, runErr)
		} else {
			ui.Errorf("%s: update failed: %v", name, runErr)
		}
		return runErr
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
	return nil
}

// newStackSetUpdateCmd configures the update policy, so the risky bit (marking a stack as safe to
// update unattended) is an explicit, separate act rather than a flag buried in `stack add`.
func newStackSetUpdateCmd() *cobra.Command {
	var auto, noAuto bool
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
			if auto {
				if st.HealthURL == "" {
					return fmt.Errorf("set --health-url before --auto: an unattended update needs a way to tell success from a crash loop")
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
				}})
				return nil
			}
			ui.Successf("%s: autoUpdate=%v healthUrl=%s", st.Name, st.AutoUpdate, st.HealthURL)
			return nil
		},
	}
	cmd.Flags().BoolVar(&auto, "auto", false, "allow unattended updates (requires --health-url)")
	cmd.Flags().BoolVar(&noAuto, "no-auto", false, "disallow unattended updates")
	cmd.Flags().StringVar(&healthURL, "health-url", "", "URL probed from the VM after an update, e.g. http://127.0.0.1:8091/health")
	cmd.Flags().StringSliceVar(&svcs, "service", nil, "limit updates to these services")
	return cmd
}
