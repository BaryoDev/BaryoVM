// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BaryoDev/BaryoVM/internal/fleet"
)

// writeReleaseManifest replaces the fixture's release.json.
func writeReleaseManifest(t *testing.T, localRoot string, sync ...string) {
	t.Helper()
	home := os.Getenv("BARYOVM_HOME")
	m, _ := json.Marshal(map[string]any{"localRoot": localRoot, "remoteRoot": "/srv/x", "sync": sync})
	if err := os.WriteFile(filepath.Join(home, "release.json"), m, 0o600); err != nil {
		t.Fatal(err)
	}
}

// noDial fails the test if a release connects.
func refuseDial(t *testing.T) {
	t.Helper()
	prev := dialRelease
	t.Cleanup(func() { dialRelease = prev })
	dialRelease = func(*fleet.VM) (remoteSession, error) {
		t.Error("nothing may connect")
		return fakeSession{&fakeRunner{}}, nil
	}
}

// Done when: localRoot "~" with sync [".ssh"] is refused at load and on --dry-run, naming the field.
func TestReleaseRefusesALocalRootOutsideTheManifestDirectory(t *testing.T) {
	for _, dry := range []bool{true, false} {
		releaseFixture(t)
		withLookPath(t, hasRsync)
		refuseDial(t)
		writeReleaseManifest(t, "~", ".ssh")
		f := &fakeRunner{}
		args := []string{"demo"}
		if dry {
			args = append(args, "--dry-run")
		}
		_, _, err := runCmdE(t, newStackReleaseCmd(), f, true, args...)
		if err == nil || !strings.Contains(err.Error(), "localRoot") || !strings.Contains(err.Error(), "--release-root") {
			t.Errorf("dry run %v: want a refusal naming localRoot and the way out, got %v", dry, err)
		}
		if len(f.seen) != 0 {
			t.Errorf("dry run %v: no command may run: %v", dry, f.seen)
		}
	}
}

// Done when: a sync entry that is a symlink pointing outside the root is refused before rsync runs.
func TestReleaseRefusesASyncSymlinkPointingOutside(t *testing.T) {
	for _, dry := range []bool{true, false} {
		releaseFixture(t)
		withLookPath(t, hasRsync)
		refuseDial(t)
		home := os.Getenv("BARYOVM_HOME")
		outside := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(home, "src", "keys")); err != nil {
			t.Fatal(err)
		}
		writeReleaseManifest(t, filepath.Join(home, "src"), "keys/")
		f := &fakeRunner{}
		args := []string{"demo"}
		if dry {
			args = append(args, "--dry-run")
		}
		_, _, err := runCmdE(t, newStackReleaseCmd(), f, true, args...)
		if err == nil || !strings.Contains(err.Error(), "sync entry") || !strings.Contains(err.Error(), "symlink") {
			t.Errorf("dry run %v: want a refusal naming the sync entry, got %v", dry, err)
		}
		if len(f.seen) != 0 {
			t.Errorf("dry run %v: rsync and every other command must not run: %v", dry, f.seen)
		}
	}
}

// The way out: the operator registers the root once, and the same manifest then loads.
func TestStackAddReleaseRootLetsAManifestReachOutsideItsDirectory(t *testing.T) {
	releaseFixture(t)
	withLookPath(t, hasRsync)
	refuseDial(t)
	other := t.TempDir()
	if err := os.MkdirAll(filepath.Join(other, "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeReleaseManifest(t, other, "web")

	if _, _, err := runCmdE(t, newStackReleaseCmd(), &fakeRunner{}, true, "demo", "--dry-run"); err == nil {
		t.Fatal("a localRoot outside the manifest's directory must be refused before the root is registered")
	}

	runCmd(t, newStackAddCmd(), nil, true, "demo", "--vm", "vm1", "--path", "/home/opc/umbraco-pwa", "--release-root", filepath.Dir(other))
	store, err := fleet.Load()
	if err != nil {
		t.Fatal(err)
	}
	if st := store.FindStack("demo"); st == nil || st.ReleaseRoot != filepath.Dir(other) {
		t.Fatalf("--release-root was not stored: %+v", st)
	}
	if _, _, err := runCmdE(t, newStackReleaseCmd(), &fakeRunner{}, true, "demo", "--dry-run"); err != nil {
		t.Errorf("with the release root registered the manifest must load: %v", err)
	}

	// Re-adding without the flag keeps it, and an empty value clears it.
	runCmd(t, newStackAddCmd(), nil, true, "demo", "--vm", "vm1", "--path", "/home/opc/umbraco-pwa")
	store, _ = fleet.Load()
	if store.FindStack("demo").ReleaseRoot == "" {
		t.Error("a re-add that did not name --release-root dropped it")
	}
	runCmd(t, newStackAddCmd(), nil, true, "demo", "--vm", "vm1", "--path", "/home/opc/umbraco-pwa", "--release-root", "")
	store, _ = fleet.Load()
	if store.FindStack("demo").ReleaseRoot != "" {
		t.Error("--release-root \"\" must clear the setting")
	}
}

func TestStackAddRefusesARelativeReleaseRoot(t *testing.T) {
	releaseFixture(t)
	_, _, err := runCmdE(t, newStackAddCmd(), nil, true, "demo", "--vm", "vm1", "--path", "/opt/x", "--release-root", "../src")
	if err == nil || !strings.Contains(err.Error(), "--release-root") {
		t.Errorf("want a refusal naming the flag, got %v", err)
	}
}

// The widening is the operator's. An exported project file never carries it.
func TestProjectExportDropsReleaseRoot(t *testing.T) {
	t.Setenv("BARYOVM_HOME", t.TempDir())
	out := t.TempDir()
	mf := filepath.Join(out, "api.release.json")
	if err := os.WriteFile(mf, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &fleet.Store{
		VMs:    []fleet.VM{{Name: "vm", Host: "203.0.113.10", User: "deploy", KeyPath: "/k", Provider: "ssh"}},
		Stacks: []fleet.Stack{{Name: "api", VM: "vm", Dir: "/opt/api", ReleaseFile: mf, ReleaseRoot: "/Users/me/repos"}},
	}
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(out, fleet.DefaultProjectFile)
	if _, _, err := runCmdE(t, newProjectExportCmd(), nil, true, "--out", file, "--name", "demo"); err != nil {
		t.Fatalf("export: %v", err)
	}
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "releaseRoot") || strings.Contains(string(b), "/Users/me/repos") {
		t.Errorf("the export carries the operator's release root:\n%s", b)
	}
}
