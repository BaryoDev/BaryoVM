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

func TestLoadDefaultsAndValidation(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "r.json")
	os.WriteFile(p, []byte(`{"localRoot":"~/src","remoteRoot":"/srv/app","sync":["api"],
		"builds":[{"image":"app:1","dockerfile":"Dockerfile","context":"api"}]}`), 0o600)

	m, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Exclude defaults when omitted.
	if len(m.Exclude) == 0 || m.Exclude[0] != "bin" {
		t.Errorf("expected default excludes, got %v", m.Exclude)
	}

	// Missing required fields must error.
	bad := filepath.Join(dir, "bad.json")
	os.WriteFile(bad, []byte(`{"sync":["api"]}`), 0o600)
	if _, err := Load(bad); err == nil {
		t.Error("expected error for missing localRoot/remoteRoot")
	}
}

func TestBuildCmd(t *testing.T) {
	m := &Manifest{RemoteRoot: "/srv/app"}
	got := m.BuildCmd(Build{
		Image: "app:1", Dockerfile: "deploy/Dockerfile.api", Context: "api",
		Args: map[string]string{"NEXT_PUBLIC_API_URL": "", "MODE": "prod"},
	})
	for _, want := range []string{
		"docker build",
		"-f '/srv/app/deploy/Dockerfile.api'",
		"-t 'app:1'",
		"'/srv/app/api'",
		"--build-arg 'MODE=prod'",
		"--build-arg 'NEXT_PUBLIC_API_URL='",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("BuildCmd missing %q in:\n%s", want, got)
		}
	}
	// Deterministic arg order (sorted): MODE before NEXT_PUBLIC…
	if strings.Index(got, "MODE=") > strings.Index(got, "NEXT_PUBLIC") {
		t.Errorf("build args not in deterministic order:\n%s", got)
	}
	if strings.Contains(got, "--no-cache") {
		t.Error("unexpected --no-cache")
	}
	if !strings.Contains(m.BuildCmd(Build{Image: "x", NoCache: true}), "--no-cache") {
		t.Error("expected --no-cache when set")
	}
}

func TestRsyncCmd(t *testing.T) {
	m := &Manifest{
		LocalRoot: "/local/src", RemoteRoot: "/srv/app",
		Exclude: []string{"bin", ".git"},
	}
	c := m.RsyncCmd("api", "opc", "1.2.3.4", "/keys/id")
	args := strings.Join(c.Args, " ")
	for _, want := range []string{
		"--delete",
		"--exclude bin",
		"--exclude .git",
		"ssh -i /keys/id",
		"/local/src/api",
		"opc@1.2.3.4:/srv/app/",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("RsyncCmd missing %q in: %s", want, args)
		}
	}
}
