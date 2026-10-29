// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package release

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckRequireLocalNamesTheMissingPath(t *testing.T) {
	root := t.TempDir()
	m := &Manifest{LocalRoot: root, RemoteRoot: "/srv/app", RequireLocal: []string{"dist/index.html"}}

	err := m.CheckRequireLocal()
	if err == nil {
		t.Fatal("a missing required path was accepted")
	}
	if want := filepath.Join(root, "dist", "index.html"); !strings.Contains(err.Error(), want) {
		t.Errorf("the error should name %s: %v", want, err)
	}

	if err := os.MkdirAll(filepath.Join(root, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "dist", "index.html"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.CheckRequireLocal(); err != nil {
		t.Errorf("the path exists but was refused: %v", err)
	}
}

func TestLoadRejectsAnEmptyRequireLocalEntry(t *testing.T) {
	p := filepath.Join(t.TempDir(), "r.json")
	if err := os.WriteFile(p, []byte(`{"localRoot":"/src","remoteRoot":"/srv/app","requireLocal":["dist"," "]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p, false); err == nil || !strings.Contains(err.Error(), "requireLocal") {
		t.Errorf("want an error naming requireLocal, got %v", err)
	}
}

func TestVerificationNamesItsSource(t *testing.T) {
	manifest := &Manifest{RemoteRoot: "/srv/app", Verify: []string{"true"}}
	if got := manifest.Verification("http://x/health").Source; got != "manifest" {
		t.Errorf("manifest verify: source %q", got)
	}
	none := &Manifest{RemoteRoot: "/srv/app"}
	if got := none.Verification("http://x/health").Source; got != "healthUrl" {
		t.Errorf("healthUrl: source %q", got)
	}
	if got := none.Verification("").Source; got != "none" {
		t.Errorf("nothing configured: source %q", got)
	}
}
