// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/patch"
	"github.com/BaryoDev/BaryoVM/internal/sshx"
	"github.com/BaryoDev/BaryoVM/internal/ui"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func newVMPatchCmd() *cobra.Command {
	var dryRun, reboot bool
	cmd := &cobra.Command{
		Use:   "patch <name>",
		Short: "Update OS packages on a VM and check the stack came back",
		Long: "Snapshots the kernel, containers (with health) and nginx, runs the package\n" +
			"update as a detached systemd unit so a dropped session cannot stop it halfway,\n" +
			"and asks the host whether a reboot is needed (needs-restarting -r, or\n" +
			"/var/run/reboot-required).\n\n" +
			"It reboots only with --reboot, or when you answer yes at the prompt on a\n" +
			"terminal. After that it waits for SSH, then checks that every container that\n" +
			"was running is running and healthy again, nginx is active and nginx -t passes.\n" +
			"A container that was already unhealthy before is not counted as a failure.\n" +
			"Exits non-zero and names any container that did not return.\n\n" +
			"--dry-run lists the pending updates and whether a reboot is currently required.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vm, c, err := dialVM(args[0], "vm patch")
			if err != nil {
				return err
			}
			defer c.Close()

			action := "vm patch"
			if dryRun {
				action = "vm patch (dry run)"
			}
			opt := patch.Options{
				DryRun: dryRun,
				Reboot: reboot,
				Reconnect: func() (sshx.Runner, error) {
					return dialChecked(&vm)
				},
			}
			if !reboot && !ui.JSON() && term.IsTerminal(int(os.Stdin.Fd())) {
				opt.Confirm = func(patch.Report) bool {
					fmt.Fprintf(os.Stderr, "%s needs a reboot. Reboot now? [y/N] ", vm.Name)
					line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
					a := strings.ToLower(strings.TrimSpace(line))
					return a == "y" || a == "yes"
				}
			}

			var rep patch.Report
			runErr := ui.Step("patching "+vm.Name, func() error {
				var perr error
				rep, perr = patch.Run(c, opt)
				return perr
			})
			var problem *patch.Problem
			stackBroken := errors.As(runErr, &problem)
			if runErr != nil && !stackBroken {
				ui.Emit(ui.Result{OK: false, Action: action, Error: runErr.Error()})
				return runErr
			}

			if ui.JSON() {
				ui.Emit(ui.Result{OK: !stackBroken, Action: action, Data: rep, Error: errText(runErr)})
			} else {
				ui.Title(fmt.Sprintf("Patch report for %s", vm.Name))
				ui.Detail("package manager", rep.PackageManager)
				if dryRun {
					for _, p := range rep.Pending {
						ui.Detail(p.Name, p.Version)
					}
				}
				ui.Detail("packages", fmt.Sprintf("%d", rep.PackageCount))
				ui.Detail("kernel", fmt.Sprintf("%s to %s", rep.OldKernel, rep.NewKernel))
				ui.Detail("reboot required", rep.RebootRequired)
				ui.Detail("rebooted", fmt.Sprintf("%t", rep.Rebooted))
				for _, n := range rep.Notes {
					ui.Warnf("%s", n)
				}
				for _, n := range rep.FailedContainers {
					ui.Errorf("container did not come back: %s", n)
				}
				switch {
				case stackBroken:
					ui.Errorf("%s", runErr.Error())
				case dryRun:
					ui.Warnf("dry run: nothing was changed")
				default:
					ui.Successf("%s is patched", vm.Name)
				}
			}
			if stackBroken {
				return fmt.Errorf("%s: fix the listed items and check the VM", runErr.Error())
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "list pending updates and whether a reboot would follow, change nothing")
	cmd.Flags().BoolVar(&reboot, "reboot", false, "reboot without asking when the host says it needs one")
	return cmd
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
