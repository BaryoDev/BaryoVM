// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BaryoDev/BaryoVM/internal/fleet"
)

func runLogged(t *testing.T, args ...string) error {
	t.Helper()
	root := newRoot()
	root.SetArgs(args)
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	return runRoot(root)
}

func logLines(t *testing.T, home string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(home, "activity.jsonl"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

func activityHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("BARYOVM_HOME", home)
	t.Setenv("BARYOVM_ACTIVITY", "")
	return home
}

func TestSuccessfulCommandWritesOneLine(t *testing.T) {
	home := activityHome(t)
	if err := runLogged(t, "vm", "add", "web1", "--host", "127.0.0.1", "--key", "/k/id"); err != nil {
		t.Fatal(err)
	}
	got := logLines(t, home)
	if len(got) != 1 {
		t.Fatalf("want one line, got %v", got)
	}
	for _, want := range []string{"\"action\":\"vm add\"", "\"vm\":\"web1\"", "\"ok\":true", "\"duration_ms\":", "\"ts\":", "\"version\":", "\"host\":"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("line lacks %s: %s", want, got[0])
		}
	}
	if strings.Contains(got[0], "/k/id") || strings.Contains(got[0], "127.0.0.1") {
		t.Errorf("flag values must not be recorded: %s", got[0])
	}
}

func TestFailedCommandWritesOneLineWithTheError(t *testing.T) {
	home := activityHome(t)
	if err := runLogged(t, "vm", "remove", "ghost"); err == nil {
		t.Fatal("want the command to fail")
	}
	got := logLines(t, home)
	if len(got) != 1 {
		t.Fatalf("want one line, got %v", got)
	}
	for _, want := range []string{"\"action\":\"vm remove\"", "\"ok\":false", "\"error\":"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("line lacks %s: %s", want, got[0])
		}
	}
}

func TestStackCommandAlsoNamesItsVM(t *testing.T) {
	home := activityHome(t)
	s := &fleet.Store{}
	s.Upsert(fleet.VM{Name: "oracle", Host: "127.0.0.1", Port: 1, User: "u", KeyPath: "/nonexistent/key"})
	s.UpsertStack(fleet.Stack{Name: "site", VM: "oracle", Dir: "/srv/site"})
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	_ = runLogged(t, "stack", "ps", "site")
	got := logLines(t, home)
	if len(got) != 1 || !strings.Contains(got[0], "\"stack\":\"site\"") || !strings.Contains(got[0], "\"vm\":\"oracle\"") {
		t.Fatalf("want stack and vm, got %v", got)
	}
}

func TestListsHelpAndVersionAreNotRecorded(t *testing.T) {
	home := activityHome(t)
	for _, args := range [][]string{{"vm", "list"}, {"stack", "list"}, {"version"}, {"vm", "--help"}, {"help"}, {"completion", "bash"}} {
		_ = runLogged(t, args...)
	}
	if got := logLines(t, home); len(got) != 0 {
		t.Fatalf("nothing should be recorded, got %v", got)
	}
}

func TestVMExecNeverRecordsTheCommand(t *testing.T) {
	home := activityHome(t)
	s := &fleet.Store{}
	s.Upsert(fleet.VM{Name: "web1", Host: "127.0.0.1", Port: 1, User: "u", KeyPath: "/nonexistent/key"})
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"vm", "exec", "web1", "--", "echo", "secret-ghp_abc123"},
		{"vm", "exec", "-o", "json", "web1", "--", "echo", "secret-ghp_abc123"},
	} {
		_ = runLogged(t, args...)
	}
	got := logLines(t, home)
	if len(got) != 2 {
		t.Fatalf("want two lines, got %v", got)
	}
	for _, l := range got {
		if strings.Contains(l, "secret") || strings.Contains(l, "ghp_") || strings.Contains(l, "echo") {
			t.Errorf("exec arguments leaked: %s", l)
		}
		if !strings.Contains(l, "\"action\":\"vm exec\"") || !strings.Contains(l, "\"vm\":\"web1\"") {
			t.Errorf("exec line lacks action or vm: %s", l)
		}
	}
}

func TestActivityOffWritesNothing(t *testing.T) {
	home := activityHome(t)
	t.Setenv("BARYOVM_ACTIVITY", "off")
	_ = runLogged(t, "vm", "remove", "ghost")
	if got := logLines(t, home); len(got) != 0 {
		t.Fatalf("want nothing, got %v", got)
	}
}

func TestUnwritableLogKeepsTheCommandsOwnResult(t *testing.T) {
	home := activityHome(t)
	if err := os.Mkdir(filepath.Join(home, "activity.jsonl"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := runLogged(t, "vm", "add", "web1", "--host", "h", "--key", "/k"); err != nil {
		t.Fatalf("a log failure must not fail the command: %v", err)
	}
	err := runLogged(t, "vm", "remove", "ghost")
	if err == nil || !strings.Contains(err.Error(), "no VM named") {
		t.Fatalf("want the command's own error, got %v", err)
	}
	if code, _ := exitCodeOf(err); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
}
