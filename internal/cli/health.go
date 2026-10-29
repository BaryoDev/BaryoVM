// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"errors"
	"fmt"

	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"github.com/BaryoDev/BaryoVM/internal/health"
	"github.com/BaryoDev/BaryoVM/internal/ui"
	"github.com/spf13/cobra"
)

func newVMHealthCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "health [name]",
		Short: "Ask a VM the questions an owner should ask monthly (read only)",
		Long: "Reads disk use on / and the docker root, pending updates split into security and\n" +
			"other, whether a reboot is required, containers that are restarting or exited\n" +
			"unexpectedly, and whether automatic security updates are enabled. Each is a line\n" +
			"with a verdict: ok, warn, fail, unknown or skip.\n\n" +
			"It changes nothing on the host. It needs `sudo -n` for docker and the package\n" +
			"manager; a check it cannot answer is reported as unknown and the rest still run.\n" +
			"Exits non-zero when any check is a fail, which includes any pending security update.\n\n" +
			"With --all it checks every registered VM. In -o json a single VM gives one report\n" +
			"in data, and --all gives a list of reports.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if all == (len(args) == 1) {
				err := errors.New("give a VM name, or --all for every registered VM")
				ui.Emit(ui.Result{OK: false, Action: "vm health", Error: err.Error()})
				return err
			}
			if all {
				return healthAll()
			}
			return healthOne(args[0])
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "check every registered VM")
	return cmd
}

func healthOne(name string) error {
	vm, err := requireVM(name)
	if err != nil {
		ui.Emit(ui.Result{OK: false, Action: "vm health", Error: err.Error()})
		return err
	}
	if err := requireLinux(&vm, "vm health"); err != nil {
		ui.Emit(ui.Result{OK: false, Action: "vm health", Error: err.Error()})
		return err
	}
	rep := checkVM(vm)
	return emitHealth([]health.Report{rep}, false)
}

func healthAll() error {
	store, err := fleet.Load()
	if err != nil {
		ui.Emit(ui.Result{OK: false, Action: "vm health", Error: err.Error()})
		return err
	}
	if len(store.VMs) == 0 {
		err := errors.New("no VMs registered yet: add one with `baryovm vm add`")
		ui.Emit(ui.Result{OK: false, Action: "vm health", Error: err.Error()})
		return err
	}
	var reps []health.Report
	for _, vm := range store.VMs {
		if err := requireLinux(&vm, "vm health"); err != nil {
			reps = append(reps, health.Skipped(vm.Name, err.Error()))
			continue
		}
		reps = append(reps, checkVM(vm))
	}
	return emitHealth(reps, true)
}

// checkVM dials and runs the checks. An unreachable VM is a report, not an error, so --all goes on
// to the next machine.
func checkVM(vm fleet.VM) health.Report {
	var rep health.Report
	_ = ui.Step("checking "+vm.Name, func() error {
		c, err := dialRelease(&vm)
		if err != nil {
			rep = health.Unreachable(vm.Name, err)
			return nil
		}
		defer c.Close()
		rep = health.Run(c, vm.Name, health.Options{})
		return nil
	})
	return rep
}

func emitHealth(reps []health.Report, all bool) error {
	failed := 0
	for _, r := range reps {
		if r.Failed() {
			failed++
		}
	}
	var data any = reps[0]
	if all {
		data = reps
	}
	if ui.JSON() {
		res := ui.Result{OK: failed == 0, Action: "vm health", Data: data}
		if failed > 0 {
			res.Error = fmt.Sprintf("%d of %d VMs have a failing check", failed, len(reps))
			if !all {
				res.Error = reps[0].VM + " has a failing check"
			}
		}
		ui.Emit(res)
	} else {
		for _, r := range reps {
			printHealth(r)
		}
	}
	if failed > 0 {
		if all {
			return fmt.Errorf("%d of %d VMs have a failing check", failed, len(reps))
		}
		return fmt.Errorf("%s has a failing check", reps[0].VM)
	}
	return nil
}

func printHealth(r health.Report) {
	ui.Title("Health of " + r.VM)
	if r.Verdict == health.Skip {
		ui.Warnf("skipped: %s", r.Error)
		return
	}
	if r.Error != "" {
		ui.Errorf("%s", r.Error)
		return
	}
	for _, c := range r.Checks {
		switch c.Verdict {
		case health.OK:
			ui.Successf("%s: %s", c.Name, c.Detail)
		case health.Warn:
			ui.Warnf("%s: %s", c.Name, c.Detail)
		case health.Fail:
			ui.Errorf("%s: %s", c.Name, c.Detail)
		default:
			ui.Notef("%s (%s): %s", c.Name, c.Verdict, c.Detail)
		}
		for _, it := range c.Items {
			ui.Notef("    %s", it)
		}
	}
}
