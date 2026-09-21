// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package cli is BaryoVM's command surface (cobra). The .NET MAUI app and the
// MCP server call these same commands with -o json.
package cli

import (
	"errors"
	"os"
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/sshx"
	"github.com/BaryoDev/BaryoVM/internal/ui"
	"github.com/spf13/cobra"
)

var outputFormat string

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
			ui.SetJSON(outputFormat == "json")
		},
	}
	root.PersistentFlags().StringVarP(&outputFormat, "output", "o", "human", "output format: human | json")
	root.AddCommand(newVersionCmd(), newVMCmd(), newDeployCmd(), newDoctorCmd(), newUpCmd(), newStackCmd())
	return root
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
			ui.Detail("verify it", "ssh-keyscan -t ecdsa,ed25519 "+host+" | ssh-keygen -lf -")
		}
	}()

	if err := newRoot().Execute(); err != nil {
		code, silent := exitCodeOf(err)
		if !silent {
			ui.Emit(ui.Result{OK: false, Action: "baryovm", Error: err.Error()})
		}
		os.Exit(code)
	}
}
