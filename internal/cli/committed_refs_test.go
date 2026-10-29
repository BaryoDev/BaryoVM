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

// A manifest from someone's branch must not be able to read the operator's secrets.
func TestCommittedManifestRefsAreConfined(t *testing.T) {
	for _, ref := range []string{"file:/etc/passwd", "file:~/.ssh/id_ed25519", "file:../outside", "env:HOME"} {
		for _, field := range []string{"args", "secrets"} {
			for _, dry := range []bool{true, false} {
				releaseFixture(t)
				withLookPath(t, hasRsync)
				home := os.Getenv("BARYOVM_HOME")
				m, _ := json.Marshal(map[string]any{
					"localRoot": filepath.Join(home, "src"), "remoteRoot": "/srv/x", "sync": []string{"web"},
					"builds": []map[string]any{{"image": "x:1", "dockerfile": "Dockerfile", "context": "web",
						field: map[string]string{"TOKEN": ref}}},
				})
				if err := os.WriteFile(filepath.Join(home, "release.json"), m, 0o600); err != nil {
					t.Fatal(err)
				}
				f := &fakeRunner{}
				prev := dialRelease
				dialRelease = func(*fleet.VM) (remoteSession, error) {
					t.Errorf("%s: nothing may connect", ref)
					return fakeSession{f}, nil
				}
				args := []string{"demo"}
				if dry {
					args = append(args, "--dry-run")
				}
				_, _, err := runCmdE(t, newStackReleaseCmd(), f, true, args...)
				dialRelease = prev
				if err == nil || !strings.Contains(err.Error(), "refused in a committed file") || !strings.Contains(err.Error(), ref) {
					t.Errorf("%s in %s (dry run %v): want a refusal naming the reference and the rule, got %v", ref, field, dry, err)
				}
				if len(f.seen) != 0 {
					t.Errorf("%s: no command may reach the runner: %v", ref, f.seen)
				}
			}
		}
	}
}

// A symlink beside the manifest that points out passes the load-time check and is caught when the
// release resolves it, before anything connects.
func TestCommittedManifestSymlinkOutsideIsRefused(t *testing.T) {
	releaseFixture(t)
	withLookPath(t, hasRsync)
	home := os.Getenv("BARYOVM_HOME")
	outside := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(outside, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(home, "link")); err != nil {
		t.Fatal(err)
	}
	m, _ := json.Marshal(map[string]any{
		"localRoot": filepath.Join(home, "src"), "remoteRoot": "/srv/x", "sync": []string{"web"},
		"builds": []map[string]any{{"image": "x:1", "dockerfile": "Dockerfile", "context": "web",
			"args": map[string]string{"TOKEN": "file:link"}}},
	})
	if err := os.WriteFile(filepath.Join(home, "release.json"), m, 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakeRunner{}
	prev := dialRelease
	t.Cleanup(func() { dialRelease = prev })
	dialRelease = func(*fleet.VM) (remoteSession, error) {
		t.Error("nothing may connect")
		return fakeSession{f}, nil
	}
	_, _, err := runCmdE(t, newStackReleaseCmd(), f, true, "demo")
	if err == nil || !strings.Contains(err.Error(), "refused in a committed file") || strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("want a refusal that does not carry the value, got %v", err)
	}
	if len(f.seen) != 0 {
		t.Errorf("no command may reach the runner: %v", f.seen)
	}
}

func TestCommittedProjectKeyRefsAreConfined(t *testing.T) {
	for _, ref := range []string{"file:/etc/passwd", "file:~/.ssh/id_ed25519", "env:HOME"} {
		dir, _ := projectDir(t, func(p map[string]any) {
			p["vms"].([]any)[0].(map[string]any)["keyPath"] = ref
		})
		f := &fakeRunner{}
		_, dialed, err := projectRun(t, f, "--file", filepath.Join(dir, fleet.DefaultProjectFile))
		if err == nil || !strings.Contains(err.Error(), "refused in a committed file") || !strings.Contains(err.Error(), "vms[0].keyPath") {
			t.Errorf("%s: want a refusal naming the field and the rule, got %v", ref, err)
		}
		if len(dialed) != 0 || len(f.seen) != 0 {
			t.Errorf("%s: nothing may connect or run", ref)
		}
	}
}

// A symlink inside the project directory that points out is caught at resolve time.
func TestCommittedProjectKeySymlinkOutsideIsRefused(t *testing.T) {
	dir, _ := projectDir(t, func(p map[string]any) {
		p["vms"].([]any)[0].(map[string]any)["keyPath"] = "file:link"
	})
	outside := filepath.Join(t.TempDir(), "k")
	if err := os.WriteFile(outside, []byte("/keys/x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	f := &fakeRunner{}
	_, dialed, err := projectRun(t, f, "--file", filepath.Join(dir, fleet.DefaultProjectFile))
	if err == nil || !strings.Contains(err.Error(), "refused in a committed file") || len(dialed) != 0 {
		t.Fatalf("want a refusal and no connection, got %v", err)
	}
}
