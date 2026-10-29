// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package harden

import (
	"os/exec"
	"strings"
	"testing"
)

func mustContain(t *testing.T, s string, needles ...string) {
	t.Helper()
	for _, n := range needles {
		if !contains(s, n) {
			t.Errorf("script is missing %q", n)
		}
	}
}

func TestSSHFilesBranches(t *testing.T) {
	s := script(Options{})
	// perms are fixed, and a dry run only says what it would do
	mustContain(t, s, "say ssh-perms applied", "say ssh-perms would-apply", "say ssh-perms already-correct")
	// backups are moved aside, never deleted, and the message says where
	mustContain(t, s, "authorized_keys.*", ".baryovm-moved-keys", "say ssh-backups applied", "nothing deleted")
	if contains(s, "rm -f \"$f\"") {
		t.Error("a backup of authorized_keys is deleted instead of moved aside")
	}
	// stale keys are report only
	mustContain(t, s, "say authorized-keys "+StateReport, "No key was removed")
}

func TestUpdatesAndRebootAreReportOnly(t *testing.T) {
	s := script(Options{})
	mustContain(t, s,
		"say updates "+StateReport,
		"say unattended-upgrades "+StateReport,
		"say reboot "+StateReport,
		"/var/run/reboot-required",
		"needs-restarting -r",
		"Nothing was applied", "Not rebooted")
	for _, forbidden := range []string{"dnf -y update", "dnf upgrade", "apt-get upgrade", "apt-get -y upgrade", "reboot now", "systemctl reboot"} {
		if contains(s, forbidden) {
			t.Errorf("a report-only check runs %q", forbidden)
		}
	}
}

func TestNginxBranches(t *testing.T) {
	s := script(Options{})
	mustContain(t, s,
		"server_tokens off", "say nginx-tokens applied", "say nginx-tokens would-apply", "say nginx-tokens already-correct",
		"nginx -t", "say nginx-default-server "+StateReport, "default_server")
	// the missing catch-all changes what a client without SNI gets, so it is never added
	if contains(s, "listen 443 ssl default_server") {
		t.Error("harden writes a default_server block")
	}
}

func TestKexPaths(t *testing.T) {
	s := script(Options{})
	t.Run("added", func(t *testing.T) {
		mustContain(t, s, "ssh -Q kex", "KexAlgorithms $KEXLINE", "say kex applied", "checked with sshd -T", "existing algorithms kept after")
		// hybrids come first, in the order the build offers them
		if !strings.HasPrefix(pqKex, "mlkem768x25519-sha256 ") {
			t.Errorf("pqKex = %q; the ML-KEM hybrid must come first", pqKex)
		}
	})
	t.Run("already present", func(t *testing.T) {
		mustContain(t, s, "say kex already-correct", "hybrid key exchange is first")
	})
	t.Run("openssh too old", func(t *testing.T) {
		mustContain(t, s, "neither mlkem768x25519-sha256 nor sntrup761x25519-sha512", "Nothing was changed", "say kex "+StateReport)
	})
	t.Run("rhel crypto policy", func(t *testing.T) {
		// the drop-in sorts before 50-redhat.conf, and when sshd -T still shows another
		// algorithm first the policy needs a subpolicy: say so, change nothing more.
		mustContain(t, s, "crypto-policies", "needs a subpolicy", "say kex "+StateReport)
	})
	t.Run("dry run", func(t *testing.T) {
		mustContain(t, s, "say kex would-apply")
	})
}

func TestParseApplyCountsReportedFindings(t *testing.T) {
	out := "FACT|os|ol\nFACT|sshd|OpenSSH_9.9\n" +
		"CHANGE|ssh-perms|applied|fixed\n" +
		"CHANGE|updates|report|3 security update(s) pending. Nothing was applied\n" +
		"CHANGE|reboot|report|newer kernel installed. Not rebooted\n" +
		"CHANGE|kex|already-correct|ok\n"
	r := parseApply(out)
	if r.Reported != 2 {
		t.Errorf("Reported = %d, want 2", r.Reported)
	}
	if len(r.Changes) != 4 || r.Changes[1].Action != StateReport {
		t.Errorf("changes = %+v", r.Changes)
	}
}

func TestDryRunSaysChangeVersusReport(t *testing.T) {
	s := script(Options{DryRun: true})
	mustContain(t, s, "DRY=1", "would-apply", StateReport)
}

func TestNormalizeCIDRs(t *testing.T) {
	got, err := NormalizeCIDRs([]string{"203.0.113.7/32", " 10.0.0.0/8 "})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "203.0.113.7/32" || got[1] != "10.0.0.0/8" {
		t.Errorf("got %v", got)
	}
	for _, bad := range []string{"203.0.113.7", "nope", "10.0.0.0/33"} {
		if _, err := NormalizeCIDRs([]string{bad}); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	if _, err := NormalizeCIDRs(nil); err != nil {
		t.Errorf("empty list: %v", err)
	}
}

func TestSSHFromWritesFirewallRules(t *testing.T) {
	s := script(Options{SSHFrom: []string{"203.0.113.0/24"}})
	mustContain(t, s, "203.0.113.0/24", "firewall-cmd", "--add-rich-rule", "ufw allow from", "proto tcp",
		"say ssh-firewall applied", "say ssh-firewall would-apply", "say ssh-firewall already-correct", "say ssh-firewall skipped")
	if contains(s, "say ssh-exposure") && !contains(s, "Limit it with --ssh-from") {
		t.Error("exposure report does not point at --ssh-from")
	}
}

func TestSSHExposureIsReportedNotChanged(t *testing.T) {
	s := script(Options{})
	mustContain(t, s, "say ssh-exposure "+StateReport, "Limit it with --ssh-from")
	if contains(s, "ssh-firewall applied") {
		t.Error("without --ssh-from the firewall section can change the firewall")
	}
	if contains(s, "--add-rich-rule") || contains(s, "ufw allow from") {
		t.Error("without --ssh-from the script writes firewall rules")
	}
}

// The guard is the lockout protection, so run it for real rather than reading it.
func TestSSHFromRefusesARuleThatExcludesTheClient(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	guard := "#!/bin/bash\n" + sshFromGuard(Options{SSHFrom: []string{"203.0.113.0/24"}}) + "\necho PASSED\n"
	run := func(client string) string {
		cmd := exec.Command(bash, "-c", guard)
		cmd.Env = []string{"SSH_CLIENT=" + client, "PATH=/usr/bin:/bin"}
		out, _ := cmd.CombinedOutput()
		return string(out)
	}
	if out := run("198.51.100.9 50000 22"); !contains(out, "ERROR|") || contains(out, "PASSED") {
		t.Errorf("a rule excluding the client was not refused: %q", out)
	}
	if out := run("203.0.113.9 50000 22"); contains(out, "ERROR|") || !contains(out, "PASSED") {
		t.Errorf("a rule including the client was refused: %q", out)
	}
	if out := run(""); !contains(out, "ERROR|") {
		t.Errorf("an unknown client was allowed: %q", out)
	}
}
