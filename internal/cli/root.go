// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package cli is BaryoVM's command surface (cobra). The .NET MAUI app and the
// MCP server call these same commands with -o json.
package cli

import (
	"os"

	"github.com/BaryoDev/BaryoVM/internal/ui"
	"github.com/spf13/cobra"
)

var outputFormat string

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
	root.AddCommand(newVersionCmd(), newVMCmd(), newDeployCmd(), newDoctorCmd())
	return root
}

// Execute runs the root command.
func Execute() {
	if err := newRoot().Execute(); err != nil {
		ui.Emit(ui.Result{OK: false, Action: "baryovm", Error: err.Error()})
		os.Exit(1)
	}
}
