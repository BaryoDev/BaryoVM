// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/backup"
	"github.com/BaryoDev/BaryoVM/internal/compose"
	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"github.com/BaryoDev/BaryoVM/internal/release"
	"github.com/BaryoDev/BaryoVM/internal/sshx"
	"github.com/BaryoDev/BaryoVM/internal/ui"
	"github.com/spf13/cobra"
)

func newStackCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "stack", Short: "Manage docker compose stacks on your VMs"}
	cmd.AddCommand(
		newStackAddCmd(), newStackListCmd(), newStackRemoveCmd(),
		newStackDeployCmd(), newStackReleaseCmd(), newStackPsCmd(), newStackPullCmd(), newStackLogsCmd(),
		newStackBackupCmd(), newStackRestoreCmd(), newStackBackupsCmd(),
		newStackUpdateCmd(), newStackSetUpdateCmd(),
	)
	return cmd
}

func newStackAddCmd() *cobra.Command {
	var vm, path, file string
	var dbContainer, dbName, dbUser, envFile, backupDir, releaseFile string
	var keep int
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Register a compose stack (a project dir on a VM)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := fleet.Load()
			if err != nil {
				return err
			}
			if store.Find(vm) == nil {
				return fmt.Errorf("no VM named %q — register it first with `baryovm vm add`", vm)
			}
			st := fleet.Stack{
				Name: args[0], VM: vm, Dir: path, File: file,
				DBContainer: dbContainer, DBName: dbName, DBUser: dbUser,
				EnvFile: envFile, BackupDir: backupDir, Keep: keep,
				ReleaseFile: releaseFile,
			}
			store.UpsertStack(st)
			if err := store.Save(); err != nil {
				return err
			}
			ui.Emit(ui.Result{OK: true, Action: "stack add", Message: fmt.Sprintf("registered stack %s (%s on %s)", args[0], path, vm), Data: st})
			return nil
		},
	}
	cmd.Flags().StringVar(&vm, "vm", "", "VM the stack runs on (required)")
	cmd.Flags().StringVar(&path, "path", "", "remote project directory, e.g. /opt/barakocms (required)")
	cmd.Flags().StringVar(&file, "file", "", "compose file name (default: compose's own default)")
	cmd.Flags().StringVar(&dbContainer, "db-container", "", "postgres container for backups, e.g. deploy-postgres-1")
	cmd.Flags().StringVar(&dbName, "db-name", "", "database to back up")
	cmd.Flags().StringVar(&dbUser, "db-user", "", "database user (default: postgres)")
	cmd.Flags().StringVar(&envFile, "env-file", "", "config file to back up, relative to the project dir (e.g. .env)")
	cmd.Flags().StringVar(&backupDir, "backup-dir", "", "remote dir for backups (default: ~/<name>-backups)")
	cmd.Flags().IntVar(&keep, "keep", 0, "backups to retain per kind (default 14)")
	cmd.Flags().StringVar(&releaseFile, "release-file", "", "local JSON release manifest for `stack release`")
	_ = cmd.MarkFlagRequired("vm")
	_ = cmd.MarkFlagRequired("path")
	return cmd
}

// runLocal runs a local command (e.g. rsync) and returns combined output.
func runLocal(c *exec.Cmd) (string, error) {
	out, err := c.CombinedOutput()
	return string(out), err
}

func newStackReleaseCmd() *cobra.Command {
	var config string
	var noBuild, noBackup bool
	cmd := &cobra.Command{
		Use:   "release <name>",
		Short: "Sync source to the VM, build images there, then compose up (config-driven)",
		Args:  cobra.ExactArgs(1),
		Example: "  baryovm stack release baryoclub\n" +
			"  baryovm stack release baryoclub --config ./baryovm.release.json --no-backup",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := fleet.Load()
			if err != nil {
				return err
			}
			st := store.FindStack(args[0])
			if st == nil {
				return fmt.Errorf("no stack named %q", args[0])
			}
			vm := store.Find(st.VM)
			if vm == nil {
				return fmt.Errorf("stack %q references unknown VM %q", args[0], st.VM)
			}
			manifestPath := config
			if manifestPath == "" {
				manifestPath = st.ReleaseFile
			}
			if manifestPath == "" {
				return fmt.Errorf("no release manifest — pass --config <file> or set --release-file on `stack add`")
			}
			m, err := release.Load(manifestPath)
			if err != nil {
				return err
			}

			fail := func(e error) error {
				ui.Emit(ui.Result{OK: false, Action: "stack release", Error: e.Error()})
				return e
			}

			// Safety first: back up before releasing (unless opted out / no DB configured).
			if !noBackup && st.DBContainer != "" && st.DBName != "" {
				if err := ui.Step("backup", func() error {
					c, err := sshx.Dial(vm.Target())
					if err != nil {
						return err
					}
					defer c.Close()
					_, err = backup.Backup(c, backupConfig(st))
					return err
				}); err != nil {
					return fail(fmt.Errorf("pre-release backup failed: %w", err))
				}
			}

			// 1. Sync each source subdir (local rsync). The compose dir (with .env) is never synced.
			for _, sub := range m.Sync {
				sub := sub
				if err := ui.Step("sync "+sub, func() error {
					out, e := runLocal(m.RsyncCmd(sub, vm.User, vm.Host, vm.KeyPath))
					if e != nil {
						return fmt.Errorf("%w: %s", e, strings.TrimSpace(out))
					}
					return nil
				}); err != nil {
					return fail(err)
				}
			}

			// 2 + 3: build images on the VM, then compose up.
			c, err := sshx.Dial(vm.Target())
			if err != nil {
				return fail(err)
			}
			defer c.Close()

			if !noBuild {
				for _, b := range m.Builds {
					b := b
					if err := ui.Step("build "+b.Image, func() error {
						out, e := c.Run(m.BuildCmd(b))
						if e != nil {
							return fmt.Errorf("%w: %s", e, lastLines(out, 8))
						}
						return nil
					}); err != nil {
						return fail(err)
					}
				}
			}

			if err := ui.Step("deploy", func() error {
				_, e := compose.Up(c, compose.Stack{Dir: st.Dir, File: st.File}, compose.UpOptions{})
				return e
			}); err != nil {
				return fail(err)
			}

			ui.Emit(ui.Result{OK: true, Action: "stack release", Message: args[0] + " released"})
			ui.Successf("%s: release done", args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&config, "config", "", "release manifest path (overrides the stack's --release-file)")
	cmd.Flags().BoolVar(&noBuild, "no-build", false, "skip building images (just sync + compose up)")
	cmd.Flags().BoolVar(&noBackup, "no-backup", false, "skip the automatic pre-release DB backup")
	return cmd
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func backupConfig(st *fleet.Stack) backup.Config {
	return backup.Config{
		Name: st.Name, Dir: st.Dir, EnvFile: st.EnvFile,
		DBContainer: st.DBContainer, DBName: st.DBName, DBUser: st.DBUser,
		BackupDir: st.BackupDir, Keep: st.Keep,
	}
}

// runStackBackup resolves a stack (which must have backup config), dials its VM,
// and runs a backup op with a spinner.
func runStackBackup(name, action, step string, fn func(c *sshx.Client, st *fleet.Stack) (string, error)) error {
	store, err := fleet.Load()
	if err != nil {
		return err
	}
	st := store.FindStack(name)
	if st == nil {
		return fmt.Errorf("no stack named %q", name)
	}
	if st.DBContainer == "" || st.DBName == "" {
		return fmt.Errorf("stack %q has no backup config — set --db-container and --db-name via `baryovm stack add %s ...`", name, name)
	}
	vm := store.Find(st.VM)
	if vm == nil {
		return fmt.Errorf("stack %q references unknown VM %q", name, st.VM)
	}
	var out string
	err = ui.Step(step, func() error {
		c, err := sshx.Dial(vm.Target())
		if err != nil {
			return err
		}
		defer c.Close()
		out, err = fn(c, st)
		return err
	})
	if err != nil {
		ui.Emit(ui.Result{OK: false, Action: action, Error: err.Error()})
		return err
	}
	if ui.JSON() {
		ui.Emit(ui.Result{OK: true, Action: action, Message: name, Data: map[string]string{"output": out}})
		return nil
	}
	if trimmed := strings.TrimRight(out, "\n"); trimmed != "" {
		fmt.Println(trimmed)
	}
	ui.Successf("%s: %s done", name, action)
	return nil
}

func newStackBackupCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "backup <name>",
		Short: "Back up the stack's database + config (pg_dump + .env)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStackBackup(args[0], "stack backup", fmt.Sprintf("backing up %s", args[0]),
				func(c *sshx.Client, st *fleet.Stack) (string, error) { return backup.Backup(c, backupConfig(st)) })
		},
	}
}

func newStackBackupsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "backups <name>",
		Short: "List the stack's database backups",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStackBackup(args[0], "stack backups", fmt.Sprintf("listing backups for %s", args[0]),
				func(c *sshx.Client, st *fleet.Stack) (string, error) { return backup.List(c, backupConfig(st)) })
		},
	}
}

func newStackRestoreCmd() *cobra.Command {
	var file string
	var yes bool
	cmd := &cobra.Command{
		Use:   "restore <name>",
		Short: "Restore the stack's database from a backup (REPLACES current data)",
		Args:  cobra.ExactArgs(1),
		Example: "  baryovm stack restore baryoclub --yes\n" +
			"  baryovm stack restore baryoclub --file db-20260717-011514.dump --yes",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				return fmt.Errorf("restore REPLACES the %q database — re-run with --yes to confirm", args[0])
			}
			return runStackBackup(args[0], "stack restore", fmt.Sprintf("restoring %s", args[0]),
				func(c *sshx.Client, st *fleet.Stack) (string, error) {
					return backup.Restore(c, backupConfig(st), file)
				})
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "backup to restore (name or path); default: newest")
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm the destructive restore")
	return cmd
}

func newStackListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List registered stacks",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := fleet.Load()
			if err != nil {
				return err
			}
			if ui.JSON() {
				ui.Emit(ui.Result{OK: true, Action: "stack list", Data: store.Stacks})
				return nil
			}
			if len(store.Stacks) == 0 {
				ui.Warnf("no stacks registered — add one with `baryovm stack add`")
				return nil
			}
			ui.Title(fmt.Sprintf("Stacks (%d)", len(store.Stacks)))
			for _, s := range store.Stacks {
				fmt.Printf("%s  %s\n", ui.AccentStyle.Render(s.Name), ui.DimStyle.Render(fmt.Sprintf("%s:%s", s.VM, s.Dir)))
			}
			return nil
		},
	}
}

func newStackRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove a stack registration (does not touch the VM)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := fleet.Load()
			if err != nil {
				return err
			}
			if !store.RemoveStack(args[0]) {
				return fmt.Errorf("no stack named %q", args[0])
			}
			if err := store.Save(); err != nil {
				return err
			}
			ui.Emit(ui.Result{OK: true, Action: "stack remove", Message: "removed " + args[0]})
			return nil
		},
	}
}

func newStackDeployCmd() *cobra.Command {
	var svcs []string
	var pull, force, noDeps bool
	cmd := &cobra.Command{
		Use:   "deploy <name>",
		Short: "compose up -d the stack (optionally pull + recreate)",
		Args:  cobra.ExactArgs(1),
		Example: "  baryovm stack deploy barako --force-recreate --service app,admin\n" +
			"  baryovm stack deploy barako --pull",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStackOp(args[0], "stack deploy", fmt.Sprintf("deploying stack %s", args[0]),
				func(c *sshx.Client, cs compose.Stack) (string, error) {
					return compose.Up(c, cs, compose.UpOptions{Services: svcs, Pull: pull, ForceRecreate: force, NoDeps: noDeps})
				})
		},
	}
	cmd.Flags().StringSliceVar(&svcs, "service", nil, "limit to these services (comma-separated)")
	cmd.Flags().BoolVar(&pull, "pull", false, "pull images before recreating")
	cmd.Flags().BoolVar(&force, "force-recreate", false, "recreate containers even if unchanged")
	cmd.Flags().BoolVar(&noDeps, "no-deps", false, "don't touch linked services")
	return cmd
}

func newStackPsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ps <name>",
		Short: "Show the stack's containers",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStackOp(args[0], "stack ps", fmt.Sprintf("querying stack %s", args[0]),
				func(c *sshx.Client, cs compose.Stack) (string, error) { return compose.Ps(c, cs) })
		},
	}
}

func newStackPullCmd() *cobra.Command {
	var svcs []string
	cmd := &cobra.Command{
		Use:   "pull <name>",
		Short: "Pull the stack's images",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStackOp(args[0], "stack pull", fmt.Sprintf("pulling images for %s", args[0]),
				func(c *sshx.Client, cs compose.Stack) (string, error) { return compose.Pull(c, cs, svcs) })
		},
	}
	cmd.Flags().StringSliceVar(&svcs, "service", nil, "limit to these services (comma-separated)")
	return cmd
}

func newStackLogsCmd() *cobra.Command {
	var svcs []string
	var tail int
	cmd := &cobra.Command{
		Use:   "logs <name>",
		Short: "Show recent logs for the stack",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStackOp(args[0], "stack logs", fmt.Sprintf("fetching logs for %s", args[0]),
				func(c *sshx.Client, cs compose.Stack) (string, error) { return compose.Logs(c, cs, svcs, tail) })
		},
	}
	cmd.Flags().StringSliceVar(&svcs, "service", nil, "limit to these services (comma-separated)")
	cmd.Flags().IntVar(&tail, "tail", 100, "lines per service")
	return cmd
}

// runStackOp resolves a stack, dials its VM, runs the op with a spinner, and
// prints the compose output (human) or wraps it in the result (JSON).
func runStackOp(name, action, step string, fn func(c *sshx.Client, cs compose.Stack) (string, error)) error {
	store, err := fleet.Load()
	if err != nil {
		return err
	}
	st := store.FindStack(name)
	if st == nil {
		return fmt.Errorf("no stack named %q — register it with `baryovm stack add %s --vm ... --path ...`", name, name)
	}
	vm := store.Find(st.VM)
	if vm == nil {
		return fmt.Errorf("stack %q references unknown VM %q", name, st.VM)
	}

	var out string
	err = ui.Step(step, func() error {
		c, err := sshx.Dial(vm.Target())
		if err != nil {
			return err
		}
		defer c.Close()
		out, err = fn(c, compose.Stack{Dir: st.Dir, File: st.File})
		return err
	})
	if err != nil {
		ui.Emit(ui.Result{OK: false, Action: action, Error: err.Error()})
		return err
	}
	if ui.JSON() {
		ui.Emit(ui.Result{OK: true, Action: action, Message: name, Data: map[string]string{"output": out}})
		return nil
	}
	if trimmed := strings.TrimRight(out, "\n"); trimmed != "" {
		fmt.Println(trimmed)
	}
	ui.Successf("%s: %s done", name, action)
	return nil
}
