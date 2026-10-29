package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BaryoDev/BaryoVM/internal/fleet"
)

func tlsManifestFile(t *testing.T, tlsBlock string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "m.json")
	body := `{"localRoot":"/a","remoteRoot":"/var/www/app","sync":["out/"],"noCompose":true` + tlsBlock + `}`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestStackTLSDryRunPrintsConfigsAndNeverDials(t *testing.T) {
	stackFixture(t)
	prev := dialRelease
	dialRelease = func(vm *fleet.VM) (remoteSession, error) {
		t.Fatal("dry run opened an SSH connection")
		return nil, nil
	}
	t.Cleanup(func() { dialRelease = prev })

	p := tlsManifestFile(t, `,"tls":{"hosts":["app.example.com"],"email":"ops@example.com","proxy":{"port":5012}}`)
	stdout, _ := runCmd(t, newStackTLSCmd(), nil, true, "app", "--config", p, "--dry-run")

	var env envelope
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("not an envelope: %v\n%s", err, stdout)
	}
	if !env.OK || env.Action != "stack tls" {
		t.Fatalf("unexpected envelope: %+v", env)
	}
	if !strings.Contains(string(env.Data), "proxy_pass http://127.0.0.1:5012") {
		t.Fatalf("dry run data lacks the vhost: %s", env.Data)
	}
}

func TestStackTLSWithoutATLSBlockSaysSo(t *testing.T) {
	stackFixture(t)
	p := tlsManifestFile(t, ``)
	_, _, err := runCmdE(t, newStackTLSCmd(), nil, true, "app", "--config", p, "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "tls") {
		t.Fatalf("want an error naming the missing tls block, got %v", err)
	}
}

func TestStackTLSIsRegisteredUnderStack(t *testing.T) {
	found := false
	for _, c := range newStackCmd().Commands() {
		if c.Name() == "tls" {
			found = true
		}
	}
	if !found {
		t.Fatal("stack tls is not registered")
	}
}
