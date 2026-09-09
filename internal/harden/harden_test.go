// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package harden

import "testing"

func TestParseApplyReadsFactsAndChanges(t *testing.T) {
	out := `FACT|os|ol
FACT|sshVersion|OpenSSH_9.9p1, OpenSSL 3.5.5 27 Jan 2026
FACT|penalties|1
CHANGE|sshd|applied|wrote /etc/ssh/sshd_config.d/10-baryovm-hardening.conf and reloaded
CHANGE|fail2ban|already-correct|already installed
CHANGE|jail|applied|wrote jail.local and started fail2ban
FACT|fail2ban|active
`
	r := parseApply(out)
	if r.OS != "ol" {
		t.Errorf("OS = %q, want ol", r.OS)
	}
	if !r.PenaltiesUsed {
		t.Error("PenaltiesUsed = false, want true")
	}
	if r.Fail2ban != "active" {
		t.Errorf("Fail2ban = %q, want active", r.Fail2ban)
	}
	// The version string contains a comma and could contain the separator itself,
	// so the parser has to keep everything after the second field.
	if want := "OpenSSH_9.9p1, OpenSSL 3.5.5 27 Jan 2026"; r.SSHVersion != want {
		t.Errorf("SSHVersion = %q, want %q", r.SSHVersion, want)
	}
	if len(r.Changes) != 3 {
		t.Fatalf("got %d changes, want 3", len(r.Changes))
	}
	if r.Changes[0].Item != "sshd" || r.Changes[0].Action != "applied" {
		t.Errorf("first change = %+v", r.Changes[0])
	}
}

func TestParseApplyOnAnOlderSSHReportsNoPenalties(t *testing.T) {
	r := parseApply("FACT|os|debian\nFACT|penalties|0\nFACT|fail2ban|active\n")
	if r.PenaltiesUsed {
		t.Error("PenaltiesUsed = true on a daemon that does not support them")
	}
}

func TestFirstErrorIsReported(t *testing.T) {
	out := "FACT|os|ol\nERROR|sshd rejected the config, drop-in removed and nothing reloaded\n"
	if got := firstError(out); got != "sshd rejected the config, drop-in removed and nothing reloaded" {
		t.Errorf("firstError = %q", got)
	}
	if got := firstError("FACT|os|ol\n"); got != "" {
		t.Errorf("firstError on clean output = %q, want empty", got)
	}
}

func TestScriptRespectsDryRunAndIgnoreList(t *testing.T) {
	dry := script(Options{DryRun: true, IgnoreCIDRs: []string{"10.0.0.0/24"}})
	if !contains(dry, "DRY=1") {
		t.Error("dry run script does not set DRY=1")
	}
	if !contains(dry, "10.0.0.0/24") {
		t.Error("ignore CIDR missing from the script")
	}
	// Loopback is not optional: banning it would lock out anything looping back
	// through the host's own address.
	if !contains(dry, "127.0.0.1/8") {
		t.Error("loopback missing from the ignore list")
	}
	if contains(script(Options{}), "DRY=1") {
		t.Error("a normal run set DRY=1")
	}
}

// The drop-in has to sort before the stock 50-* files, because sshd takes the first
// value it sees for a keyword and 50-redhat.conf sets two of the ones this policy
// changes. A rename to a higher number would silently stop those two taking effect.
func TestDropInSortsBeforeTheStockFiles(t *testing.T) {
	if got := sshdDropIn; got != "/etc/ssh/sshd_config.d/10-baryovm-hardening.conf" {
		t.Errorf("drop-in path = %q; it must sort before 50-redhat.conf", got)
	}
}

func contains(hay, needle string) bool {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
