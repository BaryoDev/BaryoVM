package fleet

import (
	"os"
	"testing"
)

// A stack's settings only matter if they survive being written and read back. `--sudo` was bound to
// its flag but never copied into the struct, so it registered as false, the update ran without
// elevation, and the failure ("permission denied" on a root-owned .env) looked like a host problem
// rather than a dropped field. Go did not catch it: the variable *was* used, by the flag binding.
//
// These round-trip the fields that change behaviour, since a silently-dropped one fails only against
// a real machine.
func TestStackSettingsSurviveARoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BARYOVM_HOME", dir)

	want := Stack{
		Name: "prod", VM: "oracle", Dir: "/opt/app", File: "docker-compose.prod.yml",
		DBContainer: "pg", DBName: "appdb", DBUser: "postgres", EnvFile: ".env",
		BackupDir: "/backups", Keep: 7,
		Sudo:           true,
		AutoUpdate:     true,
		HealthURL:      "http://127.0.0.1:8080/health",
		UpdateServices: []string{"app", "admin"},
	}

	s := &Store{}
	s.UpsertStack(want)
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	reloaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	got := reloaded.FindStack("prod")
	if got == nil {
		t.Fatal("stack did not survive the round trip at all")
	}

	if !got.Sudo {
		t.Error("Sudo was lost: an elevated stack would silently run unelevated")
	}
	if !got.AutoUpdate {
		t.Error("AutoUpdate was lost: a stack would be skipped by --auto, or worse, picked up when it should not be")
	}
	if got.HealthURL != want.HealthURL {
		t.Errorf("HealthURL: got %q", got.HealthURL)
	}
	if got.DBContainer != want.DBContainer || got.DBName != want.DBName || got.EnvFile != want.EnvFile {
		t.Errorf("backup settings lost: %+v", got)
	}
	if got.Keep != want.Keep {
		t.Errorf("Keep: got %d", got.Keep)
	}
	if len(got.UpdateServices) != 2 {
		t.Errorf("UpdateServices: got %v", got.UpdateServices)
	}
	if got.File != want.File {
		t.Errorf("File: got %q", got.File)
	}
}

// Defaults matter as much as the settings: a freshly registered stack must not be eligible for
// unattended updates, or a cron job nobody remembered writing starts touching it.
func TestANewStackIsNotAutoUpdatable(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BARYOVM_HOME", dir)

	s := &Store{}
	s.UpsertStack(Stack{Name: "fresh", VM: "oracle", Dir: "/opt/fresh"})
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	st := got.FindStack("fresh")
	if st.AutoUpdate {
		t.Error("a new stack must not be auto-updatable by default")
	}
	if st.Sudo {
		t.Error("a new stack must not run elevated by default")
	}
	if st.HealthURL != "" {
		t.Error("a new stack has no health URL until one is set")
	}
}

// The fleet file records key paths, so it must not be world-readable.
func TestFleetFileIsOwnerOnly(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BARYOVM_HOME", dir)

	s := &Store{}
	s.UpsertStack(Stack{Name: "x", VM: "v", Dir: "/opt/x"})
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path())
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("fleet file perms = %o, want 600", perm)
	}
}
