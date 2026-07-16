// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"github.com/BaryoDev/BaryoVM/internal/ui"
	"github.com/spf13/cobra"
)

// Version is the CLI version, overridable at build time with -ldflags.
var Version = "0.0.1-dev"

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the BaryoVM version",
		RunE: func(cmd *cobra.Command, args []string) error {
			ui.Emit(ui.Result{
				OK:      true,
				Action:  "version",
				Message: "BaryoVM " + Version,
				Data:    map[string]string{"version": Version},
			})
			return nil
		},
	}
}
