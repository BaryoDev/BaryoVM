// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"github.com/BaryoDev/BaryoVM/internal/remote"
	"github.com/BaryoDev/BaryoVM/internal/sshx"
	"github.com/spf13/cobra"
)

// forcedFixture is stackFixture with vm1 marked forced.
func forcedFixture(t *testing.T) {
	t.Helper()
	stackFixture(t)
	store, err := fleet.Load()
	if err != nil {
		t.Fatal(err)
	}
	store.Find("vm1").Remote = fleet.RemoteForced
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
}

// noShell fails the test if anything tries to send shell to the VM.
func noShell(t *testing.T) sshx.Runner {
	return shellFunc(func(cmd string) (string, error) {
		t.Errorf("shell was sent to a forced VM: %s", cmd)
		return "", errors.New("unexpected")
	})
}

type shellFunc func(string) (string, error)

func (f shellFunc) Run(cmd string) (string, error) { return f(cmd) }

// fakeForced replaces runForced and records the requests.
func fakeForced(t *testing.T, data string, fail error) *[]remote.Request {
	t.Helper()
	var got []remote.Request
	prev := runForced
	runForced = func(vm *fleet.VM, req remote.Request) (remote.Response, error) {
		got = append(got, req)
		return remote.Response{Protocol: remote.Protocol, OK: fail == nil, Action: remote.ActionFor(req.Op),
			Message: req.Stack, Data: json.RawMessage(data)}, fail
	}
	t.Cleanup(func() { runForced = prev })
	return &got
}

func TestForcedVMRefusesOtherStackCommands(t *testing.T) {
	cases := map[string]func() *cobra.Command{
		"pull": newStackPullCmd,
	}
	for name, mk := range cases {
		forcedFixture(t)
		reqs := fakeForced(t, `{}`, nil)
		_, _, err := runCmdE(t, mk(), noShell(t), true, "app")
		if err == nil || !strings.Contains(err.Error(), "remote=forced") {
			t.Errorf("%s: want a forced-VM refusal, got %v", name, err)
		}
		if len(*reqs) != 0 {
			t.Errorf("%s: sent a request %v", name, *reqs)
		}
	}
}

func TestForcedVMSendsTheStructuredRequests(t *testing.T) {
	cases := []struct {
		name string
		mk   func() *cobra.Command
		args []string
		op   remote.Op
	}{
		{"ps", newStackPsCmd, []string{"app"}, remote.OpStatus},
		{"logs", newStackLogsCmd, []string{"app"}, remote.OpLogs},
		{"backup", newStackBackupCmd, []string{"app"}, remote.OpBackup},
		{"update", newStackUpdateCmd, []string{"app"}, remote.OpUpdate},
	}
	for _, c := range cases {
		forcedFixture(t)
		reqs := fakeForced(t, `{"output":"fine"}`, nil)
		if _, _, err := runCmdE(t, c.mk(), noShell(t), true, c.args...); err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if len(*reqs) != 1 || (*reqs)[0].Op != c.op || (*reqs)[0].Stack != "app" {
			t.Errorf("%s: requests %v, want one %s app", c.name, *reqs, c.op)
		}
	}
}

func TestForcedVMBackupNeedsNoLocalDatabaseConfig(t *testing.T) {
	stackFixture(t)
	store, _ := fleet.Load()
	store.Find("vm1").Remote = fleet.RemoteForced
	store.FindStack("app").DBContainer = ""
	store.FindStack("app").DBName = ""
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	reqs := fakeForced(t, `{"output":"saved"}`, nil)
	if _, _, err := runCmdE(t, newStackBackupCmd(), noShell(t), true, "app"); err != nil {
		t.Fatal(err)
	}
	if len(*reqs) != 1 {
		t.Errorf("requests %v", *reqs)
	}
}

func TestForcedVMRefusesFlagsTheRequestCannotCarry(t *testing.T) {
	forcedFixture(t)
	reqs := fakeForced(t, `{}`, nil)
	for _, args := range [][]string{
		{"app", "--service", "api"},
		{"app", "--dry-run"},
		{"app", "--no-backup"},
	} {
		if _, _, err := runCmdE(t, newStackUpdateCmd(), noShell(t), true, args...); err == nil {
			t.Errorf("%v accepted on a forced VM", args)
		}
	}
	if _, _, err := runCmdE(t, newStackLogsCmd(), noShell(t), true, "app", "--tail", "5"); err == nil {
		t.Error("logs --tail accepted on a forced VM")
	}
	if len(*reqs) != 0 {
		t.Errorf("a request went out: %v", *reqs)
	}
	// --auto is accepted so a scheduled run can notify; the request is the same plain update.
	if _, _, err := runCmdE(t, newStackUpdateCmd(), noShell(t), true, "app", "--auto"); err != nil {
		t.Fatalf("--auto refused on a forced VM: %v", err)
	}
	if len(*reqs) != 1 || (*reqs)[0].String() != "baryovm-remote v1 update app" {
		t.Errorf("requests %v", *reqs)
	}
}

func TestForcedVMReportsARefusalFromTheVM(t *testing.T) {
	forcedFixture(t)
	fakeForced(t, ``, errors.New("refused: stack \"app\" does not allow update"))
	stdout, _, err := runCmdE(t, newStackPsCmd(), noShell(t), true, "app")
	if err == nil || !strings.Contains(stdout, `"ok":false`) && !strings.Contains(stdout, `"ok": false`) {
		t.Errorf("want a failed envelope and an error, got err=%v stdout=%s", err, stdout)
	}
}

func TestDialCheckedRefusesAForcedVMWithoutDialling(t *testing.T) {
	vm := &fleet.VM{Name: "ci", Host: "127.0.0.1", Remote: fleet.RemoteForced}
	c, err := dialChecked(vm)
	if err == nil || c != nil || !strings.Contains(err.Error(), "remote=forced") {
		t.Errorf("got %v %v", c, err)
	}
}

func TestVerifyRunsDetectsASwallowedCommandOncePerKey(t *testing.T) {
	probed = map[string]bool{}
	prev := newNonce
	newNonce = func() string { return "n0nce" }
	t.Cleanup(func() { newNonce = prev; probed = map[string]bool{} })

	swallowed := shellFunc(func(string) (string, error) { return "deploy ran\n", nil })
	if err := verifyRuns("h:22:u", swallowed); err == nil {
		t.Fatal("a swallowed command must be an error")
	}
	if probed["h:22:u"] {
		t.Error("a failed probe must not be cached")
	}

	calls := 0
	honest := shellFunc(func(cmd string) (string, error) {
		calls++
		return strings.TrimPrefix(cmd, "echo ") + "\n", nil
	})
	for i := 0; i < 3; i++ {
		if err := verifyRuns("h:22:u", honest); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Errorf("probe ran %d times, want 1", calls)
	}
}

func TestCallForcedRejectsAReplyThatIsNotTheWrapper(t *testing.T) {
	r := shellFunc(func(string) (string, error) { return "all done\n", nil })
	_, err := callForced(r, remote.Request{Op: remote.OpStatus, Stack: "app"})
	if err == nil || !strings.Contains(err.Error(), "baryovm-remote") {
		t.Errorf("got %v", err)
	}
}

func TestCallForcedSendsExactlyTheGrammar(t *testing.T) {
	var sent string
	r := shellFunc(func(cmd string) (string, error) {
		sent = cmd
		b, _ := json.Marshal(remote.Response{Protocol: remote.Protocol, OK: true, Action: "remote logs", Message: "app"})
		return string(b), nil
	})
	if _, err := callForced(r, remote.Request{Op: remote.OpLogs, Stack: "app"}); err != nil {
		t.Fatal(err)
	}
	if sent != "baryovm-remote v1 logs app" {
		t.Errorf("sent %q", sent)
	}
}

func TestVMRemoteSetMarksAndClears(t *testing.T) {
	stackFixture(t)
	if _, _, err := runCmdE(t, newVMRemoteSetCmd(), nil, true, "vm1", "forced"); err != nil {
		t.Fatal(err)
	}
	store, _ := fleet.Load()
	if !store.Find("vm1").Forced() {
		t.Error("not marked")
	}
	if _, _, err := runCmdE(t, newVMRemoteSetCmd(), nil, true, "vm1", "off"); err != nil {
		t.Fatal(err)
	}
	store, _ = fleet.Load()
	if store.Find("vm1").Forced() {
		t.Error("not cleared")
	}
	if _, _, err := runCmdE(t, newVMRemoteSetCmd(), nil, true, "vm1", "maybe"); err == nil {
		t.Error("a bad mode must be refused")
	}
}
