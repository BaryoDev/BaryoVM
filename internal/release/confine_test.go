// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package release

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeManifest writes a manifest with the given localRoot and sync into dir and returns its path.
func writeManifest(t *testing.T, dir, localRoot string, sync ...string) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{"localRoot": localRoot, "remoteRoot": "/srv/app", "sync": sync})
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "release.json")
	if err := os.WriteFile(p, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// A manifest from someone else's branch must not be able to name the operator's home and have a
// release copy a private key to the VM (#158). Refused at load, so --dry-run refuses too.
func TestLoadRefusesALocalRootOutsideTheManifestDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, root := range []string{"~", "~/", "~/work", home, "/", "/etc", filepath.Dir(dir), filepath.Join(dir, "..", "sibling")} {
		_, err := Load(writeManifest(t, dir, root, ".ssh"), false)
		if err == nil || !strings.Contains(err.Error(), "localRoot") {
			t.Errorf("localRoot %q: want a refusal naming localRoot, got %v", root, err)
		}
	}
}

func TestLoadRefusesASyncEntryThatClimbsOut(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	for _, sub := range []string{"../../secrets", "../../etc", "out/../../.."} {
		_, err := Load(writeManifest(t, dir, src, sub), false)
		if err == nil || !strings.Contains(err.Error(), "sync entry") || !strings.Contains(err.Error(), sub) {
			t.Errorf("sync %q: want a refusal naming the entry, got %v", sub, err)
		}
	}
}

// The ordinary shapes keep loading, and the trailing slash survives to the rsync source.
func TestLoadAcceptsPathsInsideTheManifestDirectory(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	m, err := Load(writeManifest(t, dir, src, "out/", "api", "./"), false)
	if err != nil {
		t.Fatalf("paths inside the directory were refused: %v", err)
	}
	args := m.RsyncCmd("out/", "u", "h", 22, "/k").Args
	if got := args[len(args)-2]; got != src+"/out/" {
		t.Errorf("trailing slash changed: %q", got)
	}
	if _, err := Load(writeManifest(t, dir, dir), false); err != nil {
		t.Errorf("the manifest's own directory as localRoot was refused: %v", err)
	}
}

// The way out is the stack's own registration. The manifest cannot name it for itself.
func TestReleaseRootWidensWhatTheStackAllowsAndTheFileCannot(t *testing.T) {
	base := t.TempDir()
	manifestDir := filepath.Join(base, "ops")
	sibling := filepath.Join(base, "site")
	if err := os.MkdirAll(manifestDir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := writeManifest(t, manifestDir, sibling, "out/")

	if _, err := Load(p, false); err == nil {
		t.Fatal("a sibling directory must be refused without a release root")
	}
	if _, err := Load(p, false, base); err != nil {
		t.Errorf("a release root that holds the sibling must allow it: %v", err)
	}
	if _, err := Load(p, false, filepath.Join(base, "other")); err == nil {
		t.Error("a release root that does not hold the sibling must not allow it")
	}
	if _, err := Load(p, false, ""); err == nil {
		t.Error("an empty release root widens nothing")
	}

	// A manifest that names a releaseRoot itself changes nothing: the field is not read.
	body := `{"localRoot":` + quote(sibling) + `,"remoteRoot":"/srv/app","releaseRoot":` + quote(base) + `,"ReleaseRoot":` + quote(base) + `}`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p, false); err == nil {
		t.Error("a committed file widened its own reach")
	}
}

func quote(s string) string { b, _ := json.Marshal(s); return string(b) }

// A sync entry that is a link to a directory outside passes the lexical check. It is caught before
// rsync runs.
func TestCheckConfinedRefusesASyncSymlinkPointingOut(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "id_ed25519"), []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(src, "out")); err != nil {
		t.Fatal(err)
	}
	m, err := Load(writeManifest(t, dir, src, "out/"), false)
	if err != nil {
		t.Fatalf("the link is inside the directory as written, load must accept it: %v", err)
	}
	err = m.CheckConfined()
	if err == nil || !strings.Contains(err.Error(), "sync entry") || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("want a refusal naming the sync entry and the symlink, got %v", err)
	}
	// With the stack's release root covering the target, the same link is allowed.
	m, err = Load(writeManifest(t, dir, src, "out/"), false, filepath.Dir(outside))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.CheckConfined(); err != nil {
		t.Errorf("a release root holding the link target must allow it: %v", err)
	}
}

func TestCheckConfinedRefusesALocalRootThatIsASymlinkOut(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(dir, "src")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	m, err := Load(writeManifest(t, dir, link, "."), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.CheckConfined(); err == nil || !strings.Contains(err.Error(), "localRoot") {
		t.Fatalf("want a refusal naming localRoot, got %v", err)
	}
}

func TestCheckConfinedAcceptsLinksThatStayInside(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(filepath.Join(src, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(src, "real"), filepath.Join(src, "out")); err != nil {
		t.Fatal(err)
	}
	m, err := Load(writeManifest(t, dir, src, "out/", "missing"), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.CheckConfined(); err != nil {
		t.Errorf("a link inside the directory and a not-yet-built entry must pass: %v", err)
	}
}

// A manifest reached through a symlinked directory (/tmp on macOS) with a real-path localRoot is not
// an escape.
func TestLoadAcceptsARealPathLocalRootForAManifestReachedThroughASymlink(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "via")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(real, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	p := writeManifest(t, real, src, "out/")
	linked := filepath.Join(link, filepath.Base(p))
	if _, err := Load(linked, false); err != nil {
		t.Fatalf("a real-path localRoot beside a manifest reached through a symlink was refused: %v", err)
	}
	// And the reverse: a localRoot written through the link, manifest at the real path.
	p2 := writeManifest(t, real, filepath.Join(link, "src"), "out/")
	if _, err := Load(p2, false); err != nil {
		t.Fatalf("a localRoot written through a symlink to the manifest's own directory was refused: %v", err)
	}
	// The real-path form must not become a way out.
	if _, err := Load(writeManifest(t, real, filepath.Join(filepath.Dir(real), "nowhere"), "out/"), false); err == nil {
		t.Error("a path outside the directory must still be refused")
	}
}

func TestARefusedRelativeLocalRootSaysWhereItWasRead(t *testing.T) {
	dir := t.TempDir()
	_, err := Load(writeManifest(t, dir, "../src", "out/"), false)
	if err == nil || !strings.Contains(err.Error(), "relative localRoot resolves against the current directory, not the manifest's") {
		t.Fatalf("want the relative-path explanation, got %v", err)
	}
	_, err = Load(writeManifest(t, dir, "/etc", "out/"), false)
	if err == nil || strings.Contains(err.Error(), "relative localRoot") {
		t.Errorf("an absolute localRoot must not get the relative note: %v", err)
	}
}

func TestLoadRefusesARequireLocalEntryThatClimbsOut(t *testing.T) {
	dir := t.TempDir()
	body := `{"localRoot":` + quote(dir) + `,"remoteRoot":"/srv/app","requireLocal":["../../etc/passwd"]}`
	p := filepath.Join(dir, "r.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p, false); err == nil || !strings.Contains(err.Error(), "requireLocal") {
		t.Fatalf("want a refusal naming requireLocal, got %v", err)
	}
}

// `stack tls` syncs nothing, so a localRoot elsewhere is not its business.
func TestLoadRemoteOnlySkipsTheLocalPathCheck(t *testing.T) {
	dir := t.TempDir()
	p := writeManifest(t, dir, "/etc", "../x")
	if _, err := Load(p, false); err == nil {
		t.Fatal("Load must refuse it")
	}
	if _, err := LoadRemoteOnly(p, false); err != nil {
		t.Errorf("LoadRemoteOnly must not check local paths: %v", err)
	}
}
