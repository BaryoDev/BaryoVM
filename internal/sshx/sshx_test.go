// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package sshx

import "testing"

func TestQuote(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "''"},
		{"simple", "'simple'"},
		{"with space", "'with space'"},
		{"deploy-postgres-1", "'deploy-postgres-1'"},
		// Shell metacharacters must stay literal inside single quotes (injection safety).
		{"$HOME; rm -rf /", "'$HOME; rm -rf /'"},
		{"a`b`c", "'a`b`c'"},
		{"a&&b||c", "'a&&b||c'"},
		// A single quote is the one char that must be escaped.
		{"a'b", `'a'\''b'`},
		{"'", `''\'''`},
	}
	for _, c := range cases {
		if got := Quote(c.in); got != c.want {
			t.Errorf("Quote(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSudoIsNonInteractiveOrAbsent(t *testing.T) {
	if got := Sudo(false, "docker build"); got != "docker build" {
		t.Errorf("no root asked for, got %q", got)
	}
	got := Sudo(true, "docker build")
	if got != "sudo -n docker build" {
		t.Errorf("Sudo(true, %q) = %q", "docker build", got)
	}
	// -n is the whole point: under -o json there is no terminal to answer a password prompt, so a
	// bare sudo hangs until the session times out.
	if got == "sudo docker build" {
		t.Error("sudo must be non-interactive")
	}
}

func TestSudoShellCoversTheWholeCommand(t *testing.T) {
	cases := []struct {
		name     string
		sudo     bool
		in, want string
	}{
		{"left alone without sudo", false, "nginx -t && systemctl reload nginx", "nginx -t && systemctl reload nginx"},
		// A prefix would run only nginx -t as root and leave the reload to fail on its own.
		{"both halves of a compound command", true, "nginx -t && systemctl reload nginx", `sudo -n sh -c 'nginx -t && systemctl reload nginx'`},
		// A hook already saying sudo is wrapped as well. Sudo inside sudo authorises and execs; the
		// alternative, returning the string untouched, leaves `systemctl reload nginx` below running
		// as the SSH user, which is the bug the wrapping exists to stop.
		{"a command that already says sudo", true, "sudo systemctl reload nginx",
			`sudo -n sh -c 'sudo systemctl reload nginx'`},
		{"a compound command whose first half says sudo", true, "sudo -n nginx -t && systemctl reload nginx",
			`sudo -n sh -c 'sudo -n nginx -t && systemctl reload nginx'`},
		{"a quote survives the wrapping", true, `echo 'hi'`, `sudo -n sh -c 'echo '\''hi'\'''`},
	}
	for _, c := range cases {
		if got := SudoShell(c.sudo, c.in); got != c.want {
			t.Errorf("%s: SudoShell(%v, %q) = %q, want %q", c.name, c.sudo, c.in, got, c.want)
		}
	}
}
