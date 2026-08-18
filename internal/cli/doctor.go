// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"os"
	"os/exec"
	"path/filepath"

	"github.com/BaryoDev/BaryoVM/internal/toolchain"
	"github.com/BaryoDev/BaryoVM/internal/ui"
	"github.com/spf13/cobra"
)

func newDoctorCmd() *cobra.Command {
	var fix bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check (and with --fix, auto-install) local prerequisites",
		Long: "Reports the local tools and cloud credentials BaryoVM uses. With --fix,\n" +
			"any missing command-line tool is downloaded and installed rather than erroring.",
		RunE: func(cmd *cobra.Command, args []string) error {
			type check struct {
				Name    string `json:"name"`
				Present bool   `json:"present"`
				Detail  string `json:"detail"`
			}
			var results []check

			// Cloud credentials (used by the in-process SDKs, so nothing to install).
			home, _ := os.UserHomeDir()
			for _, c := range []struct{ name, path string }{
				{"aws credentials", filepath.Join(home, ".aws", "credentials")},
				{"oci config", filepath.Join(home, ".oci", "config")},
			} {
				_, err := os.Stat(c.path)
				present := err == nil
				detail := c.path
				if !present {
					detail = "not found at " + c.path
				}
				results = append(results, check{c.name, present, detail})
			}

			// Local CLIs BaryoVM may shell out to. --fix auto-installs missing ones.
			for _, tool := range []string{"docker"} {
				var path string
				var err error
				if fix {
					path, err = toolchain.EnsureCLI(tool)
				} else {
					path, err = exec.LookPath(tool)
				}
				results = append(results, check{tool, err == nil, valOrErr(path, err)})
			}

			if ui.JSON() {
				ui.Emit(ui.Result{OK: true, Action: "doctor", Data: results})
				return nil
			}
			ui.Title("BaryoVM doctor")
			allOK := true
			for _, r := range results {
				if r.Present {
					ui.Successf("%s: %s", r.Name, r.Detail)
				} else {
					allOK = false
					ui.Warnf("%s: %s", r.Name, r.Detail)
				}
			}
			if !allOK && !fix {
				ui.Detail("hint", "run `baryovm doctor --fix` to auto-install missing tools")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&fix, "fix", false, "download and install anything missing")
	return cmd
}

func valOrErr(v string, err error) string {
	if err != nil {
		return "missing"
	}
	return v
}
