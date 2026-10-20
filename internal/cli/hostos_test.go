// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"encoding/base64"
	"errors"
	"io"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/spf13/cobra"

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

// guardedCommands is every command that has no Windows form yet, with the arguments that get it
// past flag validation and to the point where it looks at the VM.
var guardedCommands = []struct {
	name string
	cmd  func() *cobra.Command
	args []string
}{
	{"deploy", newDeployCmd, []string{"--vm", "win1", "--image", "nginx", "--name", "site"}},
	{"up", newUpCmd, []string{"win1", "--image", "nginx", "--container", "site"}},
	{"vm ping", newVMPingCmd, []string{"win1"}},
	{"vm bootstrap", newVMBootstrapCmd, []string{"win1"}},
	{"vm harden", newVMHardenCmd, []string{"win1"}},
	{"vm threats", newVMThreatsCmd, []string{"win1"}},
	{"stack deploy", newStackDeployCmd, []string{"app"}},
	{"stack ps", newStackPsCmd, []string{"app"}},
	{"stack pull", newStackPullCmd, []string{"app"}},
	{"stack logs", newStackLogsCmd, []string{"app"}},
	{"stack release", newStackReleaseCmd, []string{"app"}},
	{"stack backup", newStackBackupCmd, []string{"app"}},
	{"stack backups", newStackBackupsCmd, []string{"app"}},
	{"stack restore", newStackRestoreCmd, []string{"app", "--yes"}},
	{"stack update", newStackUpdateCmd, []string{"app"}},
}

// Each Linux-only command, run against a Windows VM, must say so and must not dial. The key path
// does not exist, so a command that got as far as dialling would fail with "read key" and not with
// the Windows message; the stubbed seams catch the stack commands that dial through them.
func TestEveryLinuxOnlyCommandRefusesAWindowsVMBeforeDialling(t *testing.T) {
	t.Setenv("BARYOVM_HOME", t.TempDir())
	store := &fleet.Store{
		VMs:    []fleet.VM{{Name: "win1", Host: "10.0.0.2", User: "admin", KeyPath: "/nonexistent/key", OS: sshx.Windows}},
		Stacks: []fleet.Stack{{Name: "app", VM: "win1", Dir: "C:/app", DBContainer: "db", DBName: "app"}},
	}
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	dialled := false
	prevRun, prevRelease := runOnVM, dialRelease
	runOnVM = func(*fleet.VM, func(sshx.Runner) (opOutput, error)) (opOutput, error) {
		dialled = true
		return opOutput{}, nil
	}
	dialRelease = func(*fleet.VM) (remoteSession, error) {
		dialled = true
		return nil, errors.New("dialled")
	}
	t.Cleanup(func() { runOnVM, dialRelease = prevRun, prevRelease })

	for _, row := range guardedCommands {
		t.Run(row.name, func(t *testing.T) {
			dialled = false
			cmd := row.cmd()
			cmd.SetArgs(row.args)
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SilenceUsage = true
			err := cmd.Execute()
			if err == nil {
				t.Fatal("expected the Windows refusal, got success")
			}
			for _, want := range []string{"does not support Windows yet", "`baryovm " + row.name + "`", "win1"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err, want)
				}
			}
			if dialled {
				t.Error("the command dialled the Windows VM")
			}
		})
	}
}

func TestUnknownOSIsRefusedNotRunAsLinux(t *testing.T) {
	err := requireLinux(&fleet.VM{Name: "box", OS: "freebsd"}, "stack deploy")
	if err == nil || err.Error() != `unknown os "freebsd" for VM box in fleet.json` {
		t.Fatalf("got %v", err)
	}
	if _, err := hostOS(&fleet.VM{Name: "box", OS: "freebsd"}); err == nil {
		t.Error("vm exec reads the OS through hostOS and must refuse an unknown one")
	}
}

func TestHandEditedOSNamesAreRead(t *testing.T) {
	if err := requireLinux(&fleet.VM{Name: "w", OS: " Windows "}, "deploy"); err == nil || !strings.Contains(err.Error(), "Windows") {
		t.Errorf("a Windows VM written in another case must still be refused: %v", err)
	}
	if err := requireLinux(&fleet.VM{Name: "l", OS: "Linux"}, "deploy"); err != nil {
		t.Errorf("Linux in another case is Linux: %v", err)
	}
}

func TestVMExecRefusesAnUnknownOSBeforeDialling(t *testing.T) {
	t.Setenv("BARYOVM_HOME", t.TempDir())
	store := &fleet.Store{VMs: []fleet.VM{{Name: "box", Host: "10.0.0.2", User: "u", KeyPath: "/nonexistent/key", OS: "freebsd"}}}
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	cmd := newVMExecCmd()
	cmd.SetArgs([]string{"box", "--", "ls"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SilenceUsage = true
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), `unknown os "freebsd" for VM box in fleet.json`) {
		t.Fatalf("got %v", err)
	}
}

func TestBuildExecRemoteForWindowsRunsThePowerShellAsGiven(t *testing.T) {
	for _, sh := range []sshx.Shell{sshx.ShellCmd, sshx.ShellPowerShell} {
		line, shown, err := buildExecRemoteFor(sshx.Windows, sh, []string{"Get-ChildItem", `"C:\Program Files"`, "|", "Select-Object", "-First", "1"})
		if err != nil {
			t.Fatal(err)
		}
		want := `Get-ChildItem "C:\Program Files" | Select-Object -First 1`
		if shown != want {
			t.Errorf("shown = %q", shown)
		}
		if got := decodeWindowsScript(t, line); got != want {
			t.Errorf("%s: the script inside the line is %q, want %q", sh, got, want)
		}
	}
}

func TestBuildExecRemoteForRefusesAScriptTheHostCannotTake(t *testing.T) {
	long := strings.Repeat("a", 3000)
	_, _, err := buildExecRemoteFor(sshx.Windows, sshx.ShellCmd, []string{long})
	if err == nil || !strings.Contains(err.Error(), "3000") || !strings.Contains(err.Error(), "8191") {
		t.Fatalf("cmd: expected a refusal naming the length and the limit, got %v", err)
	}
	if _, _, err := buildExecRemoteFor(sshx.Windows, sshx.ShellPowerShell, []string{long}); err != nil {
		t.Errorf("powershell takes it: %v", err)
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

// decodeWindowsScript reads the user's script back out of a powershell -EncodedCommand line.
func decodeWindowsScript(t *testing.T, line string) string {
	t.Helper()
	const prefix = "powershell -NoProfile -NonInteractive -EncodedCommand "
	if !strings.HasPrefix(line, prefix) {
		t.Fatalf("not an encoded powershell line: %q", line)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSuffix(strings.TrimPrefix(line, prefix), "; exit $LASTEXITCODE"))
	if err != nil {
		t.Fatal(err)
	}
	u := make([]uint16, len(raw)/2)
	for i := range u {
		u[i] = uint16(raw[2*i]) | uint16(raw[2*i+1])<<8
	}
	payload := string(utf16.Decode(u))
	const open = "[ScriptBlock]::Create('"
	const closer = "')}catch{"
	i, j := strings.Index(payload, open), strings.LastIndex(payload, closer)
	if i < 0 || j < i {
		t.Fatalf("no script literal in %q", payload)
	}
	lit := strings.ReplaceAll(payload[i+len(open):j], "''", "'")
	return strings.TrimSuffix(lit, "\n$global:bvmOk=$?;$global:bvmCode=$LASTEXITCODE")
}
