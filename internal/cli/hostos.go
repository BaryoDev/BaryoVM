// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"fmt"
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"github.com/BaryoDev/BaryoVM/internal/sshx"
)

// hostOS is the VM's OS for a command to act on. An entry with no OS is Linux; a name that is
// neither linux nor windows (a typo in fleet.json) is refused rather than run as Linux.
func hostOS(vm *fleet.VM) (sshx.OS, error) {
	if !vm.OS.Known() {
		return "", fmt.Errorf("unknown os %q for VM %s in fleet.json", string(vm.OS), vm.Name)
	}
	return vm.HostOS(), nil
}

// requireLinux stops a command that only has a Linux form before it opens a connection, so a Linux
// command is never sent to a Windows host to fail in cmd.exe with an error about something else.
// Each command passes its own name so the message says which one has no Windows form yet.
func requireLinux(vm *fleet.VM, command string) error {
	os, err := hostOS(vm)
	if err != nil {
		return err
	}
	if os == sshx.Windows {
		return fmt.Errorf("`baryovm %s` does not support Windows yet, and %s is a Windows VM: only `vm exec` runs there", command, vm.Name)
	}
	return nil
}

// buildExecRemoteFor is buildExecRemote for the VM's OS. It returns the line to send and the
// command to show in output.
//
// On Linux each word is quoted, so an argument with a space or a metacharacter stays one word. On
// Windows the words are joined into one PowerShell script and run as written: quoting each would
// turn a parameter such as -Name into a string, and the point of `vm exec` on Windows is to run a
// PowerShell command, not an executable.
func buildExecRemoteFor(os sshx.OS, sh sshx.Shell, argv []string) (line, shown string, err error) {
	if len(argv) == 0 {
		return "", "", fmt.Errorf("missing command: baryovm vm exec <name> -- <command>...")
	}
	if os.Resolved() == sshx.Windows {
		shown = strings.Join(argv, " ")
		line, err = sshx.Windows.ScriptLine(sh, shown)
		return line, shown, err
	}
	line, err = buildExecRemote(argv)
	return line, line, err
}
