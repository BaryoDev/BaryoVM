// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package harden

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const sample = `WINDOW|Sep 09 06:17:53
FAILED|939
INVALID|494
ACCEPTED|51
PASSWORDACCEPT|0
UNIQUE|46
USER|admin|106|invalid
USER|ubuntu|30|invalid
USER|user|17|invalid
SRC|171.25.158.74|84
SRC|40.82.214.8|83
LOGIN|opc|49.149.30.119|publickey|49
LOGIN|opc|52.173.181.20|publickey|1
F2B|active
BANNEDTOTAL|3
BANNEDNOW|23.29.118.224 45.9.20.11
`

func TestParseThreatsReadsTheWholeReport(t *testing.T) {
	got := parseThreats(sample)
	if got.FailedTotal != 939 || got.InvalidUser != 494 || got.UniqueSources != 46 {
		t.Errorf("counts = %d/%d/%d, want 939/494/46", got.FailedTotal, got.InvalidUser, got.UniqueSources)
	}
	if got.AcceptedTotal != 51 || got.PasswordAccepts != 0 {
		t.Errorf("accepted = %d, password = %d", got.AcceptedTotal, got.PasswordAccepts)
	}
	if got.Fail2ban != "active" || got.BannedTotal != 3 {
		t.Errorf("fail2ban = %q, banned total = %d", got.Fail2ban, got.BannedTotal)
	}
	if len(got.BannedNow) != 2 || got.BannedNow[0] != "23.29.118.224" {
		t.Errorf("BannedNow = %v, want two addresses", got.BannedNow)
	}
	if len(got.Logins) != 2 || got.Logins[0].User != "opc" || got.Logins[0].Count != 49 {
		t.Errorf("Logins = %+v", got.Logins)
	}
}

// An empty collection with a "top" label reads as "nothing is attacking you", which
// is the wrong answer when the truth is "the journal had nothing to read".
func TestParseThreatsOnAnEmptyJournalIsAllZeroRatherThanWrong(t *testing.T) {
	got := parseThreats("WINDOW|\nFAILED|0\nINVALID|0\nACCEPTED|0\nUNIQUE|0\nF2B|inactive\n")
	if got.FailedTotal != 0 || len(got.TopSources) != 0 || len(got.TopUsernames) != 0 {
		t.Errorf("empty journal produced %+v", got)
	}
	if got.Fail2ban != "inactive" {
		t.Errorf("Fail2ban = %q, want inactive", got.Fail2ban)
	}
}

// Counts arrive already sorted from the remote pipeline, but the report is a
// contract for a UI that will render them in order, so the sort is asserted here
// rather than assumed of the remote shell.
func TestTopListsComeBackHighestFirst(t *testing.T) {
	got := parseThreats("SRC|a|5\nSRC|b|90\nSRC|c|12\nUSER|x|1|invalid\nUSER|y|40|invalid\n")
	if got.TopSources[0].Name != "b" || got.TopSources[2].Name != "a" {
		t.Errorf("TopSources not sorted: %+v", got.TopSources)
	}
	if got.TopUsernames[0].Name != "y" {
		t.Errorf("TopUsernames not sorted: %+v", got.TopUsernames)
	}
}

func TestTargetedRealAccountsSeparatesNoiseFromSomethingWorseThanNoise(t *testing.T) {
	noise := parseThreats("USER|admin|106|invalid\nUSER|ubuntu|30|invalid\n")
	if hits := noise.TargetedRealAccounts(); len(hits) != 0 {
		t.Errorf("dictionary traffic reported as targeted: %v", hits)
	}
	// Somebody guessing an account that exists has learned something about the box,
	// and that is a different situation from the same volume of generic guesses.
	targeted := parseThreats("USER|admin|106|invalid\nUSER|opc|4|existing\n")
	hits := targeted.TargetedRealAccounts()
	if len(hits) != 1 || hits[0] != "opc" {
		t.Errorf("TargetedRealAccounts = %v, want the existing account", hits)
	}
}

// The pipeline, not the parser. The first version of this harvested usernames only
// from "Invalid user" lines, which sshd emits precisely when the account does NOT
// exist, so TargetedRealAccounts could never fire. The test that was supposed to
// cover it fed "USER|opc" straight into the parser and passed against a collector
// that could never produce that line, which is the shape of a test proving nothing.
func TestUserHarvestSeparatesExistingAccountsFromInvalidOnes(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on this machine")
	}
	log := filepath.Join(t.TempDir(), "journal.log")
	// Real sshd output shapes, including the trap: an invalid-user failure also
	// contains "Failed password for", so a naive second grep double counts it as an
	// existing account.
	fixture := strings.Join([]string{
		"Sep 09 19:01:02 host sshd-session[1]: Invalid user admin from 203.0.113.5 port 1",
		"Sep 09 19:01:03 host sshd-session[1]: Failed password for invalid user admin from 203.0.113.5 port 1 ssh2",
		"Sep 09 19:02:00 host sshd-session[2]: Invalid user ubuntu from 198.51.100.7 port 2",
		"Sep 09 19:03:00 host sshd-session[3]: Failed password for opc from 198.51.100.9 port 3 ssh2",
		"Sep 09 19:04:00 host sshd-session[4]: Failed publickey for opc from 198.51.100.9 port 4 ssh2",
		"Sep 09 19:05:00 host sshd-session[5]: Connection closed by authenticating user root 203.0.113.9 port 5 [preauth]",
		"Sep 09 19:06:00 host sshd-session[6]: Accepted publickey for opc from 10.0.0.2 port 6 ssh2",
	}, "\n") + "\n"
	if err := os.WriteFile(log, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := exec.Command(sh, "-c", "LOG="+log+"\n"+userHarvest).CombinedOutput()
	if err != nil {
		t.Fatalf("harvest failed: %v\n%s", err, out)
	}
	got := parseThreats(string(out))

	byName := map[string]Attempt{}
	for _, a := range got.TopUsernames {
		byName[a.Name] = a
	}
	if a, ok := byName["opc"]; !ok || !a.Existing || a.Count != 2 {
		t.Errorf("opc = %+v, want an existing account seen twice", a)
	}
	if a, ok := byName["root"]; !ok || !a.Existing {
		t.Errorf("root = %+v, want an existing account (connection closed while authenticating)", a)
	}
	if a, ok := byName["admin"]; !ok || a.Existing {
		t.Errorf("admin = %+v, want invalid; a Failed-password line for an invalid user must not count as existing", a)
	}
	// The successful login is not an attempt and must not appear as one.
	if len(got.Logins) != 0 {
		t.Errorf("harvest picked up an accepted login: %+v", got.Logins)
	}
	if hits := got.TargetedRealAccounts(); len(hits) != 2 {
		t.Errorf("TargetedRealAccounts = %v, want opc and root", hits)
	}
}

func TestAtoiTreatsRubbishAsZero(t *testing.T) {
	if atoi("not a number") != 0 || atoi(" 12 ") != 12 {
		t.Error("atoi is not total")
	}
}
