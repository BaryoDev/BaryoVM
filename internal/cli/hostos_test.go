// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"strings"
	"testing"

	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"github.com/BaryoDev/BaryoVM/internal/sshx"
)

func TestLinuxOnlyCommandFailsEarlyOnAWindowsVM(t *testing.T) {
	err := requireLinux(&fleet.VM{Name: "win1", OS: sshx.Windows}, "stack deploy")
	if err == nil {
		t.Fatal("a Linux-only command must not be sent to a Windows VM")
	}
	for _, want := range []string{"stack deploy", "win1", "Windows"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q does not name %q", err, want)
		}
	}
}

func TestLinuxOnlyCommandRunsOnLinuxAndOnAnEntryWithNoOS(t *testing.T) {
	for _, os := range []sshx.OS{"", sshx.Linux} {
		if err := requireLinux(&fleet.VM{Name: "web1", OS: os}, "stack deploy"); err != nil {
			t.Errorf("os %q: %v", os, err)
		}
	}
}

func TestStackDeployOnAWindowsVMFailsBeforeAnythingIsSent(t *testing.T) {
	t.Setenv("BARYOVM_HOME", t.TempDir())
	store := &fleet.Store{
		VMs:    []fleet.VM{{Name: "win1", Host: "10.0.0.2", User: "admin", KeyPath: "/keys/id", OS: sshx.Windows}},
		Stacks: []fleet.Stack{{Name: "app", VM: "win1", Dir: "C:/app"}},
	}
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	f := &fakeRunner{}

	_, _, err := runCmdE(t, newStackDeployCmd(), f, false, "app")

	if err == nil || !strings.Contains(err.Error(), "stack deploy") || !strings.Contains(err.Error(), "win1") {
		t.Fatalf("expected an error naming the command and the VM, got %v", err)
	}
	if len(f.seen) != 0 {
		t.Errorf("a Linux command reached the Windows host: %v", f.seen)
	}
}

func TestBuildExecRemoteForWindowsRunsThePowerShellAsGiven(t *testing.T) {
	for _, sh := range []sshx.Shell{sshx.ShellCmd, sshx.ShellPowerShell} {
		line, shown, err := buildExecRemoteFor(sshx.Windows, sh, []string{"Get-Process", "-Name", "sshd"})
		if err != nil {
			t.Fatal(err)
		}
		if shown != "Get-Process -Name sshd" {
			t.Errorf("shown = %q", shown)
		}
		if line != sshx.Windows.Script(sh, "Get-Process -Name sshd") {
			t.Errorf("%s: line is not the encoded script: %q", sh, line)
		}
	}
}

func TestBuildExecRemoteForLinuxIsUnchanged(t *testing.T) {
	line, shown, err := buildExecRemoteFor(sshx.Linux, "", []string{"echo", "a'b"})
	if err != nil {
		t.Fatal(err)
	}
	if line != `'echo' 'a'\''b'` || shown != line {
		t.Errorf("line = %q, shown = %q", line, shown)
	}
	if _, _, err := buildExecRemoteFor(sshx.Windows, "", nil); err == nil {
		t.Error("expected an error for an empty argv")
	}
}
