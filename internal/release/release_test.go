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
	c := m.RsyncCmd("api", "opc", "1.2.3.4", 22, "/keys/id")
	args := strings.Join(c.Args, " ")
	for _, want := range []string{
		"--delete",
		"--exclude bin",
		"--exclude .git",
		"ssh -i '/keys/id' -p 22",
		"/local/src/api",
		"opc@1.2.3.4:/srv/app/",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("RsyncCmd missing %q in: %s", want, args)
		}
	}
}

// The VM's port has to reach rsync. Without it a VM registered on 2222 runs its SSH commands on
// 2222 and its file transfer on 22, so the release either fails or lands on whatever answers 22.
func TestRsyncCmdUsesTheVMPort(t *testing.T) {
	m := &Manifest{LocalRoot: "/local/src", RemoteRoot: "/srv/app"}

	args := strings.Join(m.RsyncCmd("api", "opc", "1.2.3.4", 2222, "/keys/id").Args, " ")
	if !strings.Contains(args, "-p 2222") {
		t.Fatalf("port 2222 not passed to ssh: %s", args)
	}
	if strings.Contains(args, "-p 22 ") {
		t.Fatalf("port 22 used for a VM on 2222: %s", args)
	}

	// An unset port is the same default sshx.Dial uses, not a missing -p.
	zero := strings.Join(m.RsyncCmd("api", "opc", "1.2.3.4", 0, "/keys/id").Args, " ")
	if !strings.Contains(zero, "-p 22") {
		t.Fatalf("unset port should default to 22: %s", zero)
	}
}

// rsync splits the -e string with its own tokenizer before exec'ing ssh, so a key path with a space
// in it has to be quoted or rsync reads it as two arguments.
func TestRsyncCmdQuotesAndExpandsTheKeyPath(t *testing.T) {
	m := &Manifest{LocalRoot: "/local/src", RemoteRoot: "/srv/app"}

	args := strings.Join(m.RsyncCmd("api", "opc", "h", 22, "/keys/my key").Args, " ")
	if !strings.Contains(args, `ssh -i '/keys/my key'`) {
		t.Fatalf("key path with a space is not quoted: %s", args)
	}

	// Expanded here so the path that gets quoted is the path that gets opened, rather than leaning on
	// ssh's own tilde handling for -i.
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory: %v", err)
	}
	tilde := strings.Join(m.RsyncCmd("api", "opc", "h", 22, "~/.ssh/id").Args, " ")
	if !strings.Contains(tilde, "'"+home+"/.ssh/id'") {
		t.Fatalf("~ in the key path was not expanded: %s", tilde)
	}
}

// rsync is not a shell: one literal quote inside a quoted run is written by doubling it. Hand rsync
// the shell's close-escape-reopen form instead and it exits with "Missing trailing-' in remote-shell
// command" before ssh is ever reached, which is the helper's whole job getting the one input it
// exists for wrong.
func TestRsyncCmdQuotesAKeyPathTheWayRsyncParsesIt(t *testing.T) {
	m := &Manifest{LocalRoot: "/local/src", RemoteRoot: "/srv/app"}

	args := strings.Join(m.RsyncCmd("api", "opc", "h", 22, "/keys/arnel's key").Args, " ")

	if !strings.Contains(args, `ssh -i '/keys/arnel''s key'`) {
		t.Fatalf("quote not doubled for rsync's parser: %s", args)
	}
	if strings.Contains(args, `'\''`) {
		t.Fatalf("the shell's escape is a syntax error to rsync: %s", args)
	}
	// What rsync's tokenizer does with the quoted form, so the expectation above is not just a string
	// this test and the code happen to agree on.
	if got := parseRsyncShellArgs(`ssh -i '/keys/arnel''s key' -p 22`); got[2] != "/keys/arnel's key" {
		t.Fatalf("rsync would open %q", got[2])
	}
}

// parseRsyncShellArgs mirrors rsync's own tokenizer for the -e value (main.c: split on spaces, a
// quoted run ends at a single quote unless it is doubled, in which case one literal quote is kept).
func parseRsyncShellArgs(s string) []string {
	var args []string
	var cur strings.Builder
	quote := byte(0)
	started := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ' ' && quote == 0 {
			if started {
				args = append(args, cur.String())
				cur.Reset()
				started = false
			}
			continue
		}
		started = true
		if c == '\'' || c == '"' {
			if quote == 0 {
				quote = c
				continue
			}
			if c == quote {
				if i+1 < len(s) && s[i+1] == quote {
					cur.WriteByte(c)
					i++
					continue
				}
				quote = 0
				continue
			}
		}
		cur.WriteByte(c)
	}
	if started {
		args = append(args, cur.String())
	}
	return args
}
