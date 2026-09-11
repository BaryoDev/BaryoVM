// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/sshx"
	"github.com/BaryoDev/BaryoVM/internal/ui"
	"github.com/spf13/cobra"
)

// exitError carries a process exit code through cobra so Execute can propagate
// it. silent means the command already wrote its JSON envelope.
type exitError struct {
	code   int
	msg    string
	silent bool
}

func (e *exitError) Error() string { return e.msg }
func (e *exitError) ExitCode() int { return e.code }

// buildExecRemote quotes each argv element and joins them for a remote shell.
// Quoting every word (rather than joining first) is what stops spaces and
// metacharacters in one argument from becoming shell syntax.
func buildExecRemote(argv []string) (string, error) {
	if len(argv) == 0 {
		return "", fmt.Errorf("missing command: baryovm vm exec <name> -- <command>...")
	}
	parts := make([]string, len(argv))
	for i, a := range argv {
		parts[i] = sshx.Quote(a)
	}
	return strings.Join(parts, " "), nil
}

func newVMExecCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "exec <name> -- <command>...",
		Short: "Run a command on a registered VM over SSH",
		Long: strings.TrimSpace(`
Run an arbitrary command on one registered VM using the same SSH key already
in fleet.json. This is the same authority as opening an interactive SSH
session; the CLI only shortens the path.

There is no --sudo flag: put sudo in the remote command yourself (prefer
sudo -n so -o json does not hang on a password prompt).

A non-zero remote exit becomes a non-zero CLI exit. Under -o json the envelope
keeps stdout, stderr and exitCode as separate fields.
`),
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			remote, err := buildExecRemote(args[1:])
			if err != nil {
				return err
			}
			vm, err := requireVM(name)
			if err != nil {
				return err
			}

			c, err := sshx.Dial(vm.Target())
			if err != nil {
				return err
			}
			defer c.Close()

			cap, err := c.RunCapture(remote)
			if err != nil {
				return err
			}

			data := map[string]any{
				"command":  remote,
				"stdout":   cap.Stdout,
				"stderr":   cap.Stderr,
				"exitCode": cap.ExitCode,
			}

			if ui.JSON() {
				ok := cap.ExitCode == 0
				msg := vm.Name + ": exit " + fmt.Sprintf("%d", cap.ExitCode)
				if ok {
					msg = vm.Name + ": ok"
				}
				ui.Emit(ui.Result{
					OK:      ok,
					Action:  "vm exec",
					Message: msg,
					Data:    data,
					Error:   nonZeroErr(cap),
				})
				if cap.ExitCode != 0 {
					return &exitError{code: cap.ExitCode, msg: nonZeroErr(cap), silent: true}
				}
				return nil
			}

			if _, err := io.WriteString(os.Stdout, cap.Stdout); err != nil {
				return err
			}
			if cap.Stderr != "" {
				if _, err := io.WriteString(os.Stderr, cap.Stderr); err != nil {
					return err
				}
			}
			if cap.ExitCode != 0 {
				return &exitError{code: cap.ExitCode, msg: nonZeroErr(cap)}
			}
			return nil
		},
	}
}

func nonZeroErr(cap sshx.Capture) string {
	if cap.ExitCode == 0 {
		return ""
	}
	msg := strings.TrimSpace(cap.Stderr)
	if msg == "" {
		return fmt.Sprintf("remote exit status %d", cap.ExitCode)
	}
	return msg
}

// exitCodeOf returns a process exit code for err, defaulting to 1.
func exitCodeOf(err error) (int, bool) {
	var ee *exitError
	if errors.As(err, &ee) {
		return ee.code, ee.silent
	}
	return 1, false
}
