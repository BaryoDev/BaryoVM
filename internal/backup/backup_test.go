package backup

import (
	"encoding/json"
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
