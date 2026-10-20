// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"strings"
	"testing"
)

// The scan command's engine (running ZAP, parsing, gating) is tested in
// internal/scan with a fake runner. These tests cover only the cli wiring that
// runs before any Docker call, so they need no host and no container.

func TestScanRequiresURL(t *testing.T) {
	cmd := newScanCmd()
	cmd.SetArgs(nil)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--url is required") {
		t.Fatalf("want a --url required error, got %v", err)
	}
}

func TestScanRejectsBadFailOn(t *testing.T) {
	// A typo in --fail-on must error before running anything, so a broken gate
	// value in CI fails loudly instead of scanning with a silently-wrong
	// threshold.
	cmd := newScanCmd()
	cmd.SetArgs([]string{"--url", "https://example.com", "--fail-on", "critical"})
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "unknown severity") {
		t.Fatalf("want an unknown-severity error, got %v", err)
	}
}

func TestScanFailOnDefaultsToHigh(t *testing.T) {
	cmd := newScanCmd()
	f := cmd.Flag("fail-on")
	if f == nil || f.DefValue != "high" {
		t.Fatalf("--fail-on should default to high, got %v", f)
	}
}
