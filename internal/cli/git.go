// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/guard"
	"github.com/spf13/cobra"
)

func newGitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "git <git arguments>",
		Short: "Run git, refusing a commit or push that carries a key or goes to the wrong host",
		Long: strings.TrimSpace(`
Runs git with the same arguments, after two checks:

  commit  scans the staged diff for keys (and the working diff with -a or
          paths) plus the message
  push    checks the remote host against the allowed hosts, then scans every
          commit not already on the remote

A refusal names the file and line and never prints the value, and git is not
run. Everything else passes straight through to git, with git's output and exit
code. A line carrying the comment allow-secret is skipped, for test fixtures.

The allowed hosts come from git config (one --add per host) and the
BARYOVM_ALLOWED_HOSTS environment variable (comma separated). With neither
set, any host is allowed:

  git config --add baryovm.allowedHosts github.com

Flags after "git" belong to git, so this command takes none of its own. For
JSON output of a refusal set BARYOVM_OUTPUT=json.
`),
		Example:            "  baryovm git commit -m 'add login'\n  baryovm git push -u origin main",
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
				return cmd.Help()
			}
			globals, _, _ := guard.SplitGit(args)
			pol, err := loadPolicy(globals)
			if err != nil {
				return finishGuard(policyRefusal("git", err), "git", args)
			}
			return finishGuard(guard.CheckGit(guardRunner, args, pol), "git", args)
		},
	}
}
