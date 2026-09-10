// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/BaryoDev/BaryoVM/internal/toolchain"
	"github.com/BaryoDev/BaryoVM/internal/ui"
	"github.com/spf13/cobra"
)

// doctorCheck is one line of doctor's report.
type doctorCheck struct {
	Name    string `json:"name"`
	Present bool   `json:"present"`
	Detail  string `json:"detail"`
	Hint    string `json:"hint,omitempty"`
}

// checkedCLIs are the binaries BaryoVM shells out to locally.
//
// rsync and ssh are what `stack release` runs to sync source: release.RsyncCmd
// builds an rsync command and passes ssh as its transport with -e. The Go SSH
// client covers the command sessions, not that transfer. Missing either one
// fails the release after the pre-release backup has already run, which is the
// worst moment to learn a tool is absent, so doctor has to name them.
var checkedCLIs = []string{"docker", "rsync", "ssh"}

func newDoctorCmd() *cobra.Command {
	var fix bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check (and with --fix, auto-install) local prerequisites",
		Long: "Reports the local tools and cloud credentials BaryoVM uses. With --fix,\n" +
			"any missing command-line tool is downloaded and installed rather than erroring.",
		RunE: func(cmd *cobra.Command, args []string) error {
			results := doctorChecks(fix, runtime.GOOS)
			allOK := true
			for _, r := range results {
				if !r.Present {
					allOK = false
				}
			}

			if ui.JSON() {
				ui.Emit(ui.Result{OK: allOK, Action: "doctor", Data: results})
				return nil
			}
			ui.Title("BaryoVM doctor")
			for _, r := range results {
				if r.Present {
					ui.Successf("%s: %s", r.Name, r.Detail)
					continue
				}
				ui.Warnf("%s: %s", r.Name, r.Detail)
				if r.Hint != "" {
					ui.Detail("hint", r.Hint)
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

// doctorChecks runs every check and reports what it found. goos is passed in so
// the platform-specific advice is testable from any machine.
func doctorChecks(fix bool, goos string) []doctorCheck {
	var results []doctorCheck

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
		results = append(results, doctorCheck{Name: c.name, Present: present, Detail: detail})
	}

	// Local CLIs BaryoVM may shell out to. --fix auto-installs missing ones.
	for _, tool := range checkedCLIs {
		var path string
		var err error
		if fix {
			path, err = toolchain.EnsureCLI(tool)
		} else {
			path, err = exec.LookPath(tool)
		}
		r := doctorCheck{Name: tool, Present: err == nil, Detail: valOrErr(path, err)}
		if !r.Present {
			r.Hint = missingToolHint(tool, goos)
		}
		results = append(results, r)
	}
	return results
}

// missingToolHint answers "now what" where "missing" is not actionable on its
// own. Windows ships no rsync, so a Windows user otherwise gets all the way to
// the first `stack release` before anything says the platform is the problem.
func missingToolHint(tool, goos string) string {
	if tool == "rsync" && goos == "windows" {
		return "Windows does not ship rsync, and `stack release` needs it: run BaryoVM inside WSL, " +
			"or install the rsync package in Git Bash, then check rsync is on PATH"
	}
	return ""
}

func valOrErr(v string, err error) string {
	if err != nil {
		return "missing"
	}
	return v
}
