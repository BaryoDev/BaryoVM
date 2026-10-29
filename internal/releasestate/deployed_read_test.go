// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package releasestate

import (
	"strings"
	"testing"

	"github.com/BaryoDev/BaryoVM/internal/sshx"
)

func TestReadDeployedCommandIsReadOnlyAndQuoted(t *testing.T) {
	l := For("/opt/app/dist", "my app", true)
	cmd := l.ReadDeployedCmd()
	if !strings.Contains(cmd, "sudo -n") || !strings.Contains(cmd, "cat ") || strings.Contains(cmd, "mkdir") || strings.Contains(cmd, " mv ") {
		t.Fatalf("not a plain read: %s", cmd)
	}
	if !strings.Contains(cmd, sshx.Quote(l.DeployedFile())) {
		t.Fatalf("path not quoted: %s", cmd)
	}
}

func TestReadDeployedParsesAndTreatsEmptyAsNone(t *testing.T) {
	l := For("/opt/app", "app", false)
	none := &stubRunner{}
	d, err := ReadDeployed(none, l)
	if err != nil || d != nil {
		t.Fatalf("empty: %v %v", d, err)
	}
	ok := &stubRunner{answers: func(string) string {
		return `{"stack":"app","commit":"abc123","at":"2026-10-01T00:00:00Z"}`
	}}
	d, err = ReadDeployed(ok, l)
	if err != nil || d == nil || d.Commit != "abc123" {
		t.Fatalf("parse: %v %v", d, err)
	}
	bad := &stubRunner{answers: func(string) string { return "{nope" }}
	if _, err = ReadDeployed(bad, l); err == nil {
		t.Fatal("bad JSON should error")
	}
}
