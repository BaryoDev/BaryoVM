// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"fmt"
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/compose"
	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"github.com/BaryoDev/BaryoVM/internal/sshx"
	"github.com/BaryoDev/BaryoVM/internal/ui"
	"github.com/spf13/cobra"
)

func newStackCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "stack", Short: "Manage docker compose stacks on your VMs"}
	cmd.AddCommand(
		newStackAddCmd(), newStackListCmd(), newStackRemoveCmd(),
		newStackDeployCmd(), newStackPsCmd(), newStackPullCmd(), newStackLogsCmd(),
	)
	return cmd
}

func newStackAddCmd() *cobra.Command {
	var vm, path, file string
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
			st := fleet.Stack{Name: args[0], VM: vm, Dir: path, File: file}
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
	_ = cmd.MarkFlagRequired("vm")
	_ = cmd.MarkFlagRequired("path")
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
