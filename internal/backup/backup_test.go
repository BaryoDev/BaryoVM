package backup

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// `stack backups` answers an empty listing with a line of prose. A human reads it; an agent driving
// the CLI has to pattern-match English inside a blob to learn there is nothing to restore from,
// which is the same shape issue #19 is about on `stack logs`.
func TestAnEmptyListingIsNotJustProse(t *testing.T) {
	empty := DescribeList(NoBackupsMarker + "\n")
	if empty.Count != 0 {
		t.Errorf("expected 0 backups, got %d: %v", empty.Count, empty.Backups)
	}
	if len(empty.Backups) != 0 {
		t.Errorf("the marker line is not a backup: %v", empty.Backups)
	}
	if empty.Note == "" {
		t.Error("an empty listing needs a note saying so")
	}

	full := DescribeList("/home/opc/app-backups/db-20260902-010101.dump\n" +
		"/home/opc/app-backups/db-20260901-010101.dump\n")
	if full.Count != 2 {
		t.Fatalf("expected 2 backups, got %d: %v", full.Count, full.Backups)
	}
	if full.Backups[0] != "/home/opc/app-backups/db-20260902-010101.dump" {
		t.Errorf("newest first, got %v", full.Backups)
	}
	if full.Note != "" {
		t.Errorf("a listing with backups in it must not carry the empty note: %q", full.Note)
	}

	a, _ := json.Marshal(empty)
	b, _ := json.Marshal(full)
	if string(a) == string(b) {
		t.Fatalf("nothing and something serialize the same: %s", a)
	}
}

// The marker the script echoes and the marker the describer skips are one constant, so the two
// cannot drift apart and start counting the prose as a backup.
func TestListEchoesTheMarkerDescribeListSkips(t *testing.T) {
	var f fakeRunner
	if _, err := List(&f, Config{Name: "app"}); err != nil {
		t.Fatalf("List: %v", err)
	}
	if !strings.Contains(f.cmd, "'"+NoBackupsMarker+"'") {
		t.Fatalf("the listing script does not echo the marker: %s", f.cmd)
	}
}

type fakeRunner struct{ cmd string }

func (f *fakeRunner) Run(cmd string) (string, error) {
	f.cmd = cmd
	return "", nil
}

// The script List builds, run against real directories.
//
// The version this replaces was `ls glob 2>/dev/null || echo marker`, which cannot tell "no dumps
// here" from "could not read the directory": both arrived as the marker, so DescribeList reported an
// unreadable backup directory as a successful listing of zero backups. That is precisely the
// empty-versus-failed confusion ListResult was added to end, one line above ListResult.
func TestTheListingScriptTellsEmptyFromUnreadable(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on this machine")
	}
	runFor := func(t *testing.T, dir string) (string, error) {
		t.Helper()
		script, err := List(scriptOnly{}, Config{Name: "app", BackupDir: dir})
		if err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command(sh, "-c", script).Output()
		return string(out), err
	}

	t.Run("a directory that was never created is empty, not an error", func(t *testing.T) {
		out, err := runFor(t, filepath.Join(t.TempDir(), "never-backed-up"))
		if err != nil {
			t.Fatalf("a stack that has never been backed up is a normal answer, got error: %v", err)
		}
		if got := DescribeList(out); got.Count != 0 {
			t.Errorf("count = %d, want 0", got.Count)
		}
	})

	t.Run("a readable directory holding no dumps is empty", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		out, err := runFor(t, dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := DescribeList(out); got.Count != 0 {
			t.Errorf("count = %d, want 0; a non-dump file is not a backup", got.Count)
		}
	})

	t.Run("a directory that cannot be read is an error, not an empty listing", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores the permission bits this case depends on")
		}
		dir := t.TempDir()
		locked := filepath.Join(dir, "locked")
		if err := os.Mkdir(locked, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

		if _, err := runFor(t, locked); err == nil {
			t.Error("an unreadable backup directory reported success; empty and unavailable must not look the same")
		}
	})

	t.Run("dumps are listed", func(t *testing.T) {
		dir := t.TempDir()
		for _, n := range []string{"db-20260901-010101.dump", "db-20260902-010101.dump"} {
			if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		out, err := runFor(t, dir)
		if err != nil {
			t.Fatal(err)
		}
		if got := DescribeList(out); got.Count != 2 {
			t.Errorf("count = %d, want 2: %v", got.Count, got.Backups)
		}
	})
}

// scriptOnly hands back the script instead of running it, so a test can run it locally.
type scriptOnly struct{}

func (scriptOnly) Run(cmd string) (string, error) { return cmd, nil }
