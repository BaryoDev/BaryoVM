// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package releasestate

import (
	"errors"
	"strings"
	"testing"
	"time"
)

type stubRunner struct {
	seen    []string
	answers func(cmd string) string
}

func (s *stubRunner) Run(cmd string) (string, error) {
	s.seen = append(s.seen, cmd)
	if s.answers != nil {
		return s.answers(cmd), nil
	}
	return "", nil
}

func TestForPlacesTheFileNextToTheRoot(t *testing.T) {
	l := For("/home/opc/site-src/", "demo", false)
	if l.File != "/home/opc/.baryovm-release-demo.json" {
		t.Errorf("file: %s", l.File)
	}
	if l.Lock() != l.File+".lock" {
		t.Errorf("lock: %s", l.Lock())
	}
	if l.DeployedFile() != "/home/opc/.baryovm-release-demo.deployed.json" {
		t.Errorf("deployed: %s", l.DeployedFile())
	}
}

func TestForNeverUsesASharedDirectory(t *testing.T) {
	for root, want := range map[string]string{
		"/srv": "/.baryovm-release-a.json",
		"/":    "/.baryovm-release-a.json",
		"srv":  ".baryovm-release-a.json",
	} {
		if got := For(root, "a", false).File; got != want {
			t.Errorf("%q: %s, want %s", root, got, want)
		}
	}
}

func TestWriteCmdUsesMktemp(t *testing.T) {
	cmd := WriteCmd(For("/home/opc/src", "d", false), "/home/opc/x.json", []byte("{}"))
	if !strings.Contains(cmd, "mktemp '/home/opc/.baryovm-release.XXXXXX'") || strings.Contains(cmd, ".tmp") {
		t.Errorf("write does not go through mktemp: %s", cmd)
	}
}

func TestCommandsQuoteAndElevate(t *testing.T) {
	l := For("/home/o'pc/src", "d", true)
	for _, cmd := range []string{l.LockCmd(), l.UnlockCmd(), l.ReadCmd(), l.RemoveCmd(), WriteCmd(l, l.File, []byte("{}"))} {
		if !strings.HasPrefix(cmd, "sudo -n ") {
			t.Errorf("missing sudo -n: %s", cmd)
		}
		if strings.Contains(cmd, "o'pc") {
			t.Errorf("path not quoted: %s", cmd)
		}
	}
	plain := For("/home/opc/src", "d", false)
	if strings.Contains(plain.LockCmd(), "sudo") {
		t.Errorf("plain lock uses sudo: %s", plain.LockCmd())
	}
}

func TestMarkDoneUnsafeAndDescribe(t *testing.T) {
	s := New("demo", "abc")
	s.Mark("backup", StatusDone, nil)
	s.Mark("build:x", StatusStarted, nil)
	if !s.Done("backup") || s.Done("build:x") || s.Done("nope") {
		t.Errorf("done: %+v", s.Steps)
	}
	if _, bad := s.Unsafe(); bad {
		t.Error("a build is resumable")
	}
	s.Mark("build:x", StatusFailed, errors.New("boom"))
	if s.Failed != "build:x" || s.Error != "boom" {
		t.Errorf("failed: %q %q", s.Failed, s.Error)
	}
	s.Mark("build:x", StatusDone, nil)
	if s.Failed != "" {
		t.Errorf("failure not cleared: %q", s.Failed)
	}
	s.Mark("deploy", StatusStarted, nil)
	if n, bad := s.Unsafe(); !bad || n != "deploy" {
		t.Errorf("unsafe: %q %v", n, bad)
	}
	d := s.Describe()
	for _, want := range []string{"demo", "abc", "backup (done)", "deploy (started)"} {
		if !strings.Contains(d, want) {
			t.Errorf("describe lacks %q: %s", want, d)
		}
	}
}

func TestParseBackupFile(t *testing.T) {
	if got := ParseBackupFile("x\nbackup ok: db-1.dump (1K) in /bk\n"); got != "db-1.dump" {
		t.Errorf("got %q", got)
	}
	if got := ParseBackupFile("nothing"); got != "" {
		t.Errorf("got %q", got)
	}
}

func TestWriteThenReadRoundTrips(t *testing.T) {
	l := For("/home/opc/src", "demo", false)
	var written string
	w := &stubRunner{}
	s := New("demo", "abc")
	s.Mark("backup", StatusDone, nil)
	if err := Write(w, l, s); err != nil {
		t.Fatal(err)
	}
	written = w.seen[0]
	if !strings.Contains(written, "base64 -d") || !strings.Contains(written, l.File) {
		t.Errorf("write command: %s", written)
	}
	r := &stubRunner{answers: func(string) string { return "" }}
	got, err := Read(r, l)
	if err != nil || got != nil {
		t.Errorf("empty file should read as nil, got %v %v", got, err)
	}
	r.answers = func(string) string { return "not json" }
	if _, err := Read(r, l); err == nil {
		t.Error("bad JSON should error")
	}
}

func TestBeginRefusesOverAnExistingRecordAndReportsBusy(t *testing.T) {
	l := For("/home/opc/src", "demo", false)
	r := &stubRunner{answers: func(cmd string) string {
		switch {
		case strings.Contains(cmd, "echo locked"):
			return "locked\n"
		case strings.Contains(cmd, "cat "):
			return `{"stack":"demo","commit":"old","steps":[]}`
		}
		return ""
	}}
	cur, existing, err := Begin(r, l, New("demo", "new"))
	if err != nil || cur != nil || existing == nil || existing.Commit != "old" {
		t.Fatalf("begin: %v %v %v", cur, existing, err)
	}
	for _, cmd := range r.seen {
		if strings.Contains(cmd, "base64 -d") {
			t.Errorf("wrote over an existing record: %s", cmd)
		}
	}

	busy := &stubRunner{answers: func(cmd string) string {
		if strings.Contains(cmd, "echo locked") {
			return "busy\n"
		}
		return ""
	}}
	if _, _, err := Begin(busy, l, New("demo", "new")); !errors.Is(err, ErrBusy) {
		t.Errorf("want ErrBusy, got %v", err)
	}
}

func TestWriteStampsUpdatedAndActiveReadsIt(t *testing.T) {
	l := For("/srv/site", "d", false)
	s := New("d", "abc")
	s.Mark("sync", StatusStarted, nil)
	if err := Write(&stubRunner{}, l, s); err != nil {
		t.Fatal(err)
	}
	if s.Updated == "" {
		t.Fatal("Write should stamp the heartbeat")
	}
	at, _ := time.Parse(time.RFC3339, s.Updated)
	if !s.Active(at.Add(time.Minute)) {
		t.Error("a write a minute ago, no failed step: the release may still be running")
	}
	if s.Active(at.Add(ActiveWindow + time.Minute)) {
		t.Error("a write older than the window is a dead release")
	}
	s.Mark("sync", StatusFailed, errors.New("boom"))
	if s.Active(at.Add(time.Minute)) {
		t.Error("a failed step means the release stopped, whatever the clock says")
	}
	old := &State{Stack: "d"}
	if old.Active(at) {
		t.Error("a record with no heartbeat predates the field and counts as stopped")
	}
}
