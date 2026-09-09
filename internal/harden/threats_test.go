// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package harden

import "testing"

const sample = `WINDOW|Sep 09 06:17:53
FAILED|939
INVALID|494
ACCEPTED|51
PASSWORDACCEPT|0
UNIQUE|46
USER|admin|106
USER|ubuntu|30
USER|user|17
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
	got := parseThreats("SRC|a|5\nSRC|b|90\nSRC|c|12\nUSER|x|1\nUSER|y|40\n")
	if got.TopSources[0].Name != "b" || got.TopSources[2].Name != "a" {
		t.Errorf("TopSources not sorted: %+v", got.TopSources)
	}
	if got.TopUsernames[0].Name != "y" {
		t.Errorf("TopUsernames not sorted: %+v", got.TopUsernames)
	}
}

func TestTargetedRealAccountsSeparatesNoiseFromSomethingWorseThanNoise(t *testing.T) {
	noise := parseThreats("USER|admin|106\nUSER|ubuntu|30\n")
	if hits := noise.TargetedRealAccounts([]string{"opc", "root"}); len(hits) != 0 {
		t.Errorf("dictionary traffic reported as targeted: %v", hits)
	}
	// Somebody guessing the real account name has learned something about the box,
	// and that is a different situation from the same volume of generic guesses.
	targeted := parseThreats("USER|admin|106\nUSER|OPC|4\n")
	hits := targeted.TargetedRealAccounts([]string{"opc", "root"})
	if len(hits) != 1 || hits[0] != "OPC" {
		t.Errorf("TargetedRealAccounts = %v, want the real account matched case-insensitively", hits)
	}
}

func TestAtoiTreatsRubbishAsZero(t *testing.T) {
	if atoi("not a number") != 0 || atoi(" 12 ") != 12 {
		t.Error("atoi is not total")
	}
}
