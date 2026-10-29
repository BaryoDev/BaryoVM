// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/guard"
	"github.com/spf13/cobra"
)

func newGhCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "gh <gh arguments>",
		Short: "Run gh, refusing a PR, release or issue whose text carries a key",
		Long: strings.TrimSpace(`
Runs gh with the same arguments. For pr create, release create and issue
create it first scans --title, --body, --body-file, --notes and --notes-file
for keys. A refusal names the file and line and never prints the value, and gh
is not run. A body read from stdin (-F -) is refused because it cannot be
checked: write it to a file. Every other gh command passes straight through
with gh's output and exit code.

Flags after "gh" belong to gh, so this command takes none of its own. For JSON
output of a refusal set BARYOVM_OUTPUT=json.
`),
		Example:            "  baryovm gh pr create --title 'Add login' --body-file body.md",
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
				return cmd.Help()
			}
			return finishGuard(guard.CheckGh(guardRunner, args), "gh", args)
		},
	}
}
