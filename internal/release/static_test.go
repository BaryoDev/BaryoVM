package release

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The remote rsync is the one that needs root, not the local one. Asserting the exact flag
// because --rsync-path is easy to confuse with running the local rsync under sudo, which would
// prompt on the operator's machine and still fail to write on the VM.
func TestRsyncCmdUsesRemoteSudoOnlyWhenAsked(t *testing.T) {
	m := &Manifest{LocalRoot: "/tmp/src", RemoteRoot: "/var/www/site", Sync: []string{"out"}}

	plain := strings.Join(m.RsyncCmd("out", "opc", "h", "/k").Args, " ")
	if strings.Contains(plain, "rsync-path") {
		t.Fatalf("sudo not requested, but the command asks for it: %s", plain)
	}

	m.Sudo = true
	withSudo := strings.Join(m.RsyncCmd("out", "opc", "h", "/k").Args, " ")
	if !strings.Contains(withSudo, "--rsync-path=sudo rsync") {
		t.Fatalf("sudo requested, but the remote rsync is not elevated: %s", withSudo)
	}
}

// A static site sets these; a compose stack must not have its behaviour changed by their
// existence. Defaults are the old behaviour so every manifest already in use is unaffected.
func TestStaticFieldsDefaultToComposeBehaviour(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "r.json")
	if err := os.WriteFile(path, []byte(`{
	  "localRoot": "/tmp/src",
	  "remoteRoot": "/opt/app",
	  "sync": ["api"],
	  "builds": []
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if m.Sudo || m.NoCompose || len(m.PostDeploy) != 0 {
		t.Fatalf("a manifest that says nothing about the new fields changed behaviour: %+v", m)
	}
}

func TestStaticManifestRoundTrips(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "r.json")
	if err := os.WriteFile(path, []byte(`{
	  "localRoot": "~/repos/site",
	  "remoteRoot": "/var/www/site",
	  "sync": ["out"],
	  "builds": [],
	  "sudo": true,
	  "noCompose": true,
	  "postDeploy": ["sudo systemctl reload nginx"]
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Sudo {
		t.Error("sudo not read")
	}
	if !m.NoCompose {
		t.Error("noCompose not read")
	}
	if len(m.PostDeploy) != 1 || m.PostDeploy[0] != "sudo systemctl reload nginx" {
		t.Errorf("postDeploy not read: %v", m.PostDeploy)
	}
}

// The trailing slash is rsync's, not ours, and getting it wrong is a live outage: without it
// the build output lands in <webroot>/out/ while --delete empties <webroot> itself.
func TestSyncTrailingSlashSelectsContentsNotDirectory(t *testing.T) {
	m := &Manifest{LocalRoot: "/tmp/site", RemoteRoot: "/var/www/site"}

	dir := m.RsyncCmd("out", "opc", "h", "/k").Args
	src := dir[len(dir)-2]
	if src != "/tmp/site/out" {
		t.Fatalf(`"out" should copy the directory itself, got %q`, src)
	}

	contents := m.RsyncCmd("out/", "opc", "h", "/k").Args
	csrc := contents[len(contents)-2]
	if csrc != "/tmp/site/out/" {
		t.Fatalf(`"out/" should copy the contents, got %q`, csrc)
	}
}
