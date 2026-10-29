// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package cli is BaryoVM's command surface (cobra). The .NET MAUI app and the
// MCP server call these same commands with -o json.
package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"github.com/BaryoDev/BaryoVM/internal/hostkeys"
	"github.com/BaryoDev/BaryoVM/internal/sshx"
	"github.com/BaryoDev/BaryoVM/internal/ui"
	"github.com/spf13/cobra"
)

var outputFormat string
var strictHostKeys bool

// errDryRun signals that a command stopped early because --dry-run was set.
var errDryRun = errors.New("dry run")

func newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:           "baryovm",
		Short:         "Provision VMs and deploy apps to your own boxes",
		Long:          ui.AccentStyle.Render("BaryoVM") + " turns provision → install Docker → deploy into one command.\nCLI-first: the MAUI app and MCP server drive this same CLI (with -o json).",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			before, _ := fleet.Load()
			lastRun = &ranCommand{cmd: cmd, args: args, before: before}
			ui.SetJSON(outputFormat == "json")
			// Only apply the flag when it was actually given, so it overrides the environment
			// rather than a default false switching strict mode off for a CI job that set it.
			if cmd.Flags().Changed("strict-host-keys") {
				sshx.SetStrictHostKeys(strictHostKeys)
			}
		},
	}
	root.PersistentFlags().StringVarP(&outputFormat, "output", "o", "human", "output format: human | json")
	root.PersistentFlags().BoolVar(&strictHostKeys, "strict-host-keys", false,
		"refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)")
	root.AddCommand(newVersionCmd(), newVMCmd(), newDeployCmd(), newDoctorCmd(), newUpCmd(), newStackCmd(), newScanCmd(), newGitCmd(), newGhCmd(), newGuardCmd(), newSiteCmd())
	rejectUnknownSubcommands(root)
	return root
}

// rejectUnknownSubcommands makes every group (a command with subcommands and nothing to run itself)
// fail on a word it does not know. Cobra only does that for the root; below it, a group with no
// RunE treats the typo as an argument, prints help and exits 0, so a script running `vm health`
// reads success. A group given no argument still prints its help.
func rejectUnknownSubcommands(cmd *cobra.Command) {
	for _, sub := range cmd.Commands() {
		rejectUnknownSubcommands(sub)
	}
	if cmd.HasParent() && cmd.HasSubCommands() && !cmd.Runnable() {
		cmd.Args = func(c *cobra.Command, args []string) error {
			if len(args) > 0 {
				return fmt.Errorf("unknown command %q for %q", args[0], c.CommandPath())
			}
			return nil
		}
		cmd.RunE = func(c *cobra.Command, args []string) error { return c.Help() }
	}
}

// reportError prints a failed run in the format asked for and returns the exit code. The output
// format is set here as well as in PersistentPreRun, because cobra rejects bad arguments before
// that hook runs and the error would otherwise come out as text under -o json.
func reportError(err error) int {
	code, silent := exitCodeOf(err)
	if !silent {
		ui.SetJSON(outputFormat == "json")
		ui.Emit(ui.Result{OK: false, Action: "baryovm", Error: err.Error()})
	}
	return code
}

// Execute runs the root command.
func Execute() {
	// Tell the operator what host identity was just trusted. sshx does not print (engine packages
	// never do), so the hook lives here.
	//
	// Collected and printed at the end rather than as it happens. Dialling occurs inside ui.Step,
	// whose spinner rewrites the line every 80ms, so a Detail printed mid-step is drawn and then
	// erased by the next frame. Tested against a real host: the key was learned and written and the
	// operator saw nothing, which is the one moment the fingerprint is worth checking.
	var learned []string
	sshx.OnLearnHostKey = func(host, fingerprint string) {
		learned = append(learned, host+" "+fingerprint)
	}
	defer func() {
		for _, l := range learned {
			host, fp, _ := strings.Cut(l, " ")
			ui.Warnf("learned the host key for %s: %s", host, fp)
			ui.Detail("verify it", hostkeys.Keyscan(host)+" | ssh-keygen -lf -")
		}
	}()

	if err := runRoot(newRoot()); err != nil {
		os.Exit(reportError(err))
	}
}
