// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"fmt"
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"github.com/BaryoDev/BaryoVM/internal/harden"
	"github.com/BaryoDev/BaryoVM/internal/sshx"
	"github.com/BaryoDev/BaryoVM/internal/ui"
	"github.com/spf13/cobra"
)

func newVMHardenCmd() *cobra.Command {
	var dryRun bool
	var ignore []string
	cmd := &cobra.Command{
		Use:   "harden <name>",
		Short: "Apply the SSH hardening policy to a VM",
		Long: "Applies an opinionated, idempotent SSH policy: per-source penalties in sshd\n" +
			"where the daemon supports them, fail2ban with escalating bans, and the small\n" +
			"surface reductions that cost nothing.\n\n" +
			"It never changes the SSH port, never disables public key authentication and\n" +
			"never touches authorized_keys, so it cannot lock you out. The sshd config is\n" +
			"validated before anything is reloaded, and reloaded rather than restarted, so\n" +
			"an open session survives a mistake.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vm, c, err := dialVM(args[0])
			if err != nil {
				return err
			}
			defer c.Close()

			rep, err := harden.Apply(c, harden.Options{DryRun: dryRun, IgnoreCIDRs: ignore})
			if err != nil {
				return err
			}
			action := "vm harden"
			if dryRun {
				action = "vm harden (dry run)"
			}
			if ui.JSON() {
				ui.Emit(ui.Result{OK: true, Action: action, Data: rep})
				return nil
			}
			ui.Title(fmt.Sprintf("Hardening %s (%s)", vm.Name, rep.OS))
			ui.Detail("sshd", rep.SSHVersion)
			if !rep.PenaltiesUsed {
				ui.Warnf("this sshd has no PerSourcePenalties (needs OpenSSH 9.8+); fail2ban is doing all the work")
			}
			for _, ch := range rep.Changes {
				ui.Detail(ch.Item, fmt.Sprintf("%s: %s", ch.Action, ch.Detail))
			}
			ui.Detail("fail2ban", rep.Fail2ban)
			if dryRun {
				ui.Warnf("dry run: nothing was changed")
			} else {
				ui.Successf("policy applied to %s", vm.Name)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report what would change and write nothing")
	cmd.Flags().StringSliceVar(&ignore, "ignore", nil, "extra CIDRs fail2ban must never ban (loopback is always exempt)")
	return cmd
}

func newVMThreatsCmd() *cobra.Command {
	var since string
	cmd := &cobra.Command{
		Use:   "threats <name>",
		Short: "Show what a VM is being attacked with",
		Long: "Reads the SSH authentication record and any fail2ban bans, and summarises\n" +
			"who is hitting the machine, what account names they are guessing, and every\n" +
			"login that actually succeeded.\n\n" +
			"The successful logins are the part worth reading. The rest is volume.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vm, c, err := dialVM(args[0])
			if err != nil {
				return err
			}
			defer c.Close()

			t, err := harden.Collect(c, since)
			if err != nil {
				return err
			}
			if ui.JSON() {
				ui.Emit(ui.Result{OK: true, Action: "vm threats", Data: t})
				return nil
			}

			ui.Title(fmt.Sprintf("%s, since %s", vm.Name, t.Since))
			if t.Window != "" {
				ui.Detail("log starts", t.Window)
			}
			ui.Detail("failed attempts", fmt.Sprintf("%d from %d sources", t.FailedTotal, t.UniqueSources))
			ui.Detail("invalid users", fmt.Sprintf("%d", t.InvalidUser))
			ui.Detail("fail2ban", fmt.Sprintf("%s, %d banned to date, %d banned now", t.Fail2ban, t.BannedTotal, len(t.BannedNow)))

			// Named before the volume, because a password acceptance on a box that is
			// supposed to be key-only is the one line here that changes what you do next.
			if t.PasswordAccepts > 0 {
				ui.Errorf("%d login(s) succeeded with a password; this host should be key-only", t.PasswordAccepts)
			}
			if hits := t.TargetedRealAccounts(); len(hits) > 0 {
				ui.Warnf("attempts named accounts that exist here: %s (generic scanners do not know these)", strings.Join(hits, ", "))
			}

			if len(t.Logins) > 0 {
				ui.Title("Accepted logins")
				for _, l := range t.Logins {
					ui.Detail(fmt.Sprintf("%s@%s", l.User, l.Source), fmt.Sprintf("%s, %d time(s)", l.Method, l.Count))
				}
			}
			if len(t.TopSources) > 0 {
				ui.Title("Top sources")
				for _, s := range t.TopSources {
					ui.Detail(s.Name, fmt.Sprintf("%d", s.Count))
				}
			}
			if len(t.TopUsernames) > 0 {
				ui.Title("Account names guessed")
				for _, u := range t.TopUsernames {
					ui.Detail(u.Name, fmt.Sprintf("%d", u.Count))
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&since, "since", "24 hours ago", "systemd time expression, e.g. \"7 days ago\" or 2026-09-01")
	return cmd
}

// dialVM is requireVM plus the connection, which both commands in this file need
// and neither should re-implement.
func dialVM(name string) (fleet.VM, *sshx.Client, error) {
	vm, err := requireVM(name)
	if err != nil {
		return fleet.VM{}, nil, err
	}
	c, err := sshx.Dial(vm.Target())
	if err != nil {
		return vm, nil, err
	}
	return vm, c, nil
}
