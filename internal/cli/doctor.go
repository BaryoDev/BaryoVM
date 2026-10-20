// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/release"
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
	// Optional marks a check that a working machine may legitimately fail: the
	// cloud credential files matter only if you provision through a provider.
	// ok is computed over the required checks, so a box with every release tool
	// and no cloud account is reported ready, which it is.
	Optional bool `json:"optional,omitempty"`
}

// checkedCLIs are the binaries BaryoVM shells out to locally, and whether a
// machine without one is actually broken.
//
// ssh is required: rsync passes it as its transport with -e, and the Go SSH
// client covers the command sessions rather than that transfer.
//
// rsync is optional: without it `stack release` streams a tar archive over the
// Go SSH session instead, and the "release transport" line says which one a
// release will use.
//
// docker is not required, and saying otherwise was wrong in a way that mattered:
// every docker call this tool makes is remote, over the SSH client, on the VM.
// Nothing runs docker on the operator's machine. A deploy container carrying
// only rsync and ssh can do everything BaryoVM does, and marking docker required
// made `baryovm doctor && baryovm stack release app` exit 1 there. It stays on
// the list because a missing local docker is worth seeing, and --fix still knows
// how to install it.
var checkedCLIs = []struct {
	name     string
	optional bool
}{
	{"docker", true},
	{"rsync", true},
	{"ssh", false},
}

func newDoctorCmd() *cobra.Command {
	var fix bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check (and with --fix, auto-install) local prerequisites",
		Long: "Reports the local tools and cloud credentials BaryoVM uses. With --fix,\n" +
			"a missing tool BaryoVM knows how to install is installed; anything it cannot\n" +
			"install is reported with what to do about it.",
		RunE: func(cmd *cobra.Command, args []string) error {
			results := doctorChecks(fix, runtime.GOOS)
			var missing []string
			for _, r := range results {
				if !r.Present && !r.Optional {
					missing = append(missing, r.Name)
				}
			}
			var err error
			if len(missing) > 0 {
				err = fmt.Errorf("missing: %s", strings.Join(missing, ", "))
			}

			if ui.JSON() {
				res := ui.Result{OK: err == nil, Action: "doctor", Data: results}
				if err != nil {
					res.Error = err.Error()
				}
				ui.Emit(res)
				return err
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
			if err == nil {
				return nil
			}
			if !fix {
				ui.Detail("hint", "run `baryovm doctor --fix` to install what BaryoVM can install")
			}
			ui.Emit(ui.Result{OK: false, Action: "doctor", Error: err.Error()})
			return err
		},
	}
	cmd.Flags().BoolVar(&fix, "fix", false, "install the missing tools BaryoVM knows how to install")
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
		results = append(results, doctorCheck{Name: c.name, Present: present, Detail: detail, Optional: true})
	}

	// Local CLIs BaryoVM may shell out to. --fix auto-installs missing ones.
	for _, c := range checkedCLIs {
		tool := c.name
		var path string
		var err error
		if fix {
			path, err = toolchain.EnsureCLI(tool)
		} else {
			path, err = lookPath(tool)
		}
		r := doctorCheck{Name: tool, Present: err == nil, Detail: path, Optional: c.optional}
		if err != nil {
			// Plain lookup only knows it is absent; EnsureCLI knows why it could
			// not fix that, which is the part the user can act on.
			r.Detail = "missing"
			if fix {
				r.Detail = err.Error()
			}
			r.Hint = missingToolHint(tool, goos)
		}
		results = append(results, r)
	}

	// Decided from what the lookup above found, so --fix installing rsync shows as rsync.
	detail := string(release.TransportTar) + " over ssh (rsync not found, release streams a tar archive)"
	for _, r := range results {
		if r.Name == "rsync" && r.Present {
			detail = string(release.TransportRsync) + " (" + r.Detail + ")"
		}
	}
	results = append(results, doctorCheck{Name: "release transport", Present: true, Detail: detail})
	return results
}

// missingToolHint answers "now what" where "missing" is not actionable on its
// own: the platform ships no such package, or BaryoVM has no installer for it
// and --fix will not be the answer however many times you run it.
func missingToolHint(tool, goos string) string {
	switch {
	case tool == "rsync" && goos == "windows":
		return "Windows does not ship rsync, and `stack release` needs it: run BaryoVM inside WSL, " +
			"or install the rsync package in Git Bash, then check rsync is on PATH"
	case tool == "rsync" && goos == "darwin":
		return "install it with `brew install rsync`, or run `baryovm doctor --fix`"
	case tool == "rsync":
		return "install the rsync package with your distro's package manager, or run `baryovm doctor --fix`"
	case tool == "ssh" && goos == "windows":
		return "install the OpenSSH client (Settings, Optional features) or run BaryoVM inside WSL; " +
			"--fix does not install ssh"
	case tool == "ssh":
		return "install your OpenSSH client package (openssh-client on Debian, openssh-clients on " +
			"Fedora, already present on macOS so check PATH); --fix does not install ssh"
	}
	return ""
}
