// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package release

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BaryoDev/BaryoVM/internal/secretref"
)

const buildSecret = "npm_s3cr3t'quoted value"

func envRes(vals map[string]string) secretref.Resolver {
	r := secretref.Default()
	r.Getenv = func(k string) string { return vals[k] }
	return r
}

// #93: no build arg value, literal or referenced, is on the command line.
func TestBuildCmdCarriesNoValue(t *testing.T) {
	for _, sudo := range []bool{false, true} {
		m := &Manifest{RemoteRoot: "/srv/app", Sudo: sudo}
		b := Build{Image: "app:1", Dockerfile: "Dockerfile", Context: ".",
			Args:    map[string]string{"LITERAL": "plain-literal-value", "TOK": "env:NPM"},
			Secrets: map[string]string{"npmrc": "env:NPM"}}
		got := m.BuildCmd(b)
		for _, bad := range []string{"plain-literal-value", "env:NPM", "s3cr3t", "LITERAL=", "TOK="} {
			if strings.Contains(got, bad) {
				t.Errorf("sudo=%v: command carries %q: %s", sudo, bad, got)
			}
		}
		if !strings.Contains(got, "cat") || !strings.Contains(got, "eval") {
			t.Errorf("sudo=%v: expected the script to read stdin: %s", sudo, got)
		}
		if sudo && !strings.HasPrefix(got, "sudo -n ") {
			t.Errorf("sudo must wrap the whole script: %s", got)
		}
	}
}

func TestBuildInputResolvesAndQuotes(t *testing.T) {
	b := Build{Image: "app:1",
		Args:    map[string]string{"LITERAL": "it's", "TOK": "env:NPM", "EMPTY": ""},
		Secrets: map[string]string{"npmrc": "env:NPM"}}
	in, err := BuildInput(b, envRes(map[string]string{"NPM": buildSecret}))
	if err != nil {
		t.Fatal(err)
	}
	want := "export EMPTY=''\nexport LITERAL='it'\\''s'\nexport TOK='npm_s3cr3t'\\''quoted value'\nexport BARYOVM_SECRET_0='npm_s3cr3t'\\''quoted value'\n"
	if in != want {
		t.Errorf("stdin:\n%q\nwant\n%q", in, want)
	}
	// Redact removes every value the input holds.
	out := Redact(in, "docker said: it's and "+buildSecret)
	if strings.Contains(out, "s3cr3t") || strings.Contains(out, "it's") {
		t.Errorf("not redacted: %s", out)
	}
}

func TestUnresolvedBuildReferenceFailsBeforeAnyRemoteCall(t *testing.T) {
	m := &Manifest{Builds: []Build{{Image: "app:1", Args: map[string]string{"TOK": "env:NOT_SET"}}}}
	_, err := m.BuildInputs(envRes(nil))
	if err == nil || !strings.Contains(err.Error(), "env:NOT_SET") {
		t.Fatalf("want an error naming the reference: %v", err)
	}
}

func TestManifestCannotRunACommandReference(t *testing.T) {
	m := &Manifest{Builds: []Build{{Image: "app:1", Args: map[string]string{"TOK": "cmd:touch /tmp/pwned"}}}}
	if _, err := m.BuildInputs(secretref.Default()); err == nil {
		t.Fatal("a committed manifest must not run a local command")
	}
}

func TestLoadRefusesBadReferenceAndKeepsLiteralArgs(t *testing.T) {
	dir := t.TempDir()
	write := func(args string) string {
		p := filepath.Join(dir, "r.json")
		_ = os.WriteFile(p, []byte(`{"localRoot":"/s","remoteRoot":"/srv/app","sync":["api"],
		"builds":[{"image":"app:1","dockerfile":"Dockerfile","context":"api","args":`+args+`}]}`), 0o600)
		return p
	}
	if _, err := Load(write(`{"MODE":"prod"}`), false); err != nil {
		t.Fatalf("a released manifest with a literal arg must still load: %v", err)
	}
	if _, err := Load(write(`{"TOK":"env:1BAD"}`), false); err == nil {
		t.Error("a malformed reference must be refused at load")
	}
	if _, err := Load(write(`{"bad name":"x"}`), false); err == nil {
		t.Error("an arg name that is not a variable name must be refused at load")
	}
}
