// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package scan

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// a trimmed but real-shaped ZAP baseline report: two alerts, one medium
// (missing CSP) and one low (cookie without HttpOnly), under one site.
const sampleReport = `{
  "site": [{
    "@name": "https://example.com",
    "alerts": [
      {
        "alert": "Content Security Policy (CSP) Header Not Set",
        "riskcode": "2",
        "confidence": "3",
        "count": "4",
        "cweid": "693",
        "solution": "Ensure a CSP header is set.",
        "instances": [
          {"uri": "https://example.com/"},
          {"uri": "https://example.com/about"}
        ]
      },
      {
        "alert": "Cookie No HttpOnly Flag",
        "riskcode": "1",
        "confidence": "2",
        "count": "1",
        "cweid": "1004",
        "solution": "Set HttpOnly.",
        "instances": [{"uri": "https://example.com/login"}]
      }
    ]
  }]
}`

func TestParseReport(t *testing.T) {
	rep, err := ParseReport("https://example.com", []byte(sampleReport))
	if err != nil {
		t.Fatalf("ParseReport: %v", err)
	}
	if len(rep.Findings) != 2 {
		t.Fatalf("want 2 findings, got %d", len(rep.Findings))
	}
	// Worst first: the medium CSP finding must sort ahead of the low cookie one.
	if rep.Findings[0].Severity != "medium" {
		t.Errorf("want first finding medium, got %s", rep.Findings[0].Severity)
	}
	if rep.Findings[1].Severity != "low" {
		t.Errorf("want second finding low, got %s", rep.Findings[1].Severity)
	}
	got := rep.Findings[0]
	if got.Count != 4 {
		t.Errorf("count: want 4, got %d", got.Count)
	}
	if got.CWE != "CWE-693" {
		t.Errorf("cwe: want CWE-693, got %q", got.CWE)
	}
	if len(got.URLs) != 2 {
		t.Errorf("urls: want 2, got %d", len(got.URLs))
	}
	if rep.Counts["medium"] != 1 || rep.Counts["low"] != 1 {
		t.Errorf("counts: want 1 medium 1 low, got %v", rep.Counts)
	}
}

func TestFailsAt(t *testing.T) {
	rep, err := ParseReport("https://example.com", []byte(sampleReport))
	if err != nil {
		t.Fatal(err)
	}
	// The worst finding is medium. So the gate trips at medium and below,
	// and passes at high. This is the check that a green scan can actually
	// go red, and that a strict gate is not accidentally lenient.
	cases := []struct {
		threshold Severity
		wantFail  bool
	}{
		{SeverityHigh, false},
		{SeverityMedium, true},
		{SeverityLow, true},
		{SeverityInformational, true},
	}
	for _, c := range cases {
		if got := rep.FailsAt(c.threshold); got != c.wantFail {
			t.Errorf("FailsAt(%s): want %v, got %v", c.threshold, c.wantFail, got)
		}
	}
}

func TestFailsAtCleanReport(t *testing.T) {
	// A report with no alerts must never trip any gate, including the lowest.
	rep, err := ParseReport("https://example.com", []byte(`{"site":[{"alerts":[]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if rep.FailsAt(SeverityInformational) {
		t.Error("clean report tripped the gate")
	}
}

func TestParseSeverity(t *testing.T) {
	ok := map[string]Severity{
		"high": SeverityHigh, "MEDIUM": SeverityMedium, "low": SeverityLow,
		"info": SeverityInformational, "informational": SeverityInformational,
	}
	for in, want := range ok {
		got, err := ParseSeverity(in)
		if err != nil || got != want {
			t.Errorf("ParseSeverity(%q): got %v err %v", in, got, err)
		}
	}
	if _, err := ParseSeverity("critical"); err == nil {
		t.Error("ParseSeverity: unknown value should error, so a CI typo cannot disable the gate")
	}
}

func TestCommandTargetsBaselineAndImage(t *testing.T) {
	argv := command(Options{URL: "https://example.com"})
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, zapImage) {
		t.Errorf("command does not use %s: %v", zapImage, argv)
	}
	if !strings.Contains(joined, "zap-baseline.py") {
		t.Errorf("command is not the baseline (passive) scan: %v", argv)
	}
	// Guard the safety property: the command must never invoke the active
	// scanner. If someone wires zap-full-scan.py in here, this fails.
	if strings.Contains(joined, "zap-full-scan") || strings.Contains(joined, "zap-active") {
		t.Errorf("command runs an active scan; baseline must stay passive: %v", argv)
	}
	if !strings.Contains(joined, "example.com") {
		t.Errorf("command does not target the URL: %v", argv)
	}
	// Without /zap/wrk the baseline exits before writing the report.
	if !strings.Contains(joined, "/zap/wrk") || !strings.Contains(joined, "cat /zap/wrk/zap.json") {
		t.Errorf("command must give zap a /zap/wrk and read the report from it: %v", argv)
	}
}

// fakeRunner returns canned bytes or an error, so Run's wiring is tested with
// no Docker and no host.
type fakeRunner struct {
	out []byte
	err error
	got []string
}

func (f *fakeRunner) Run(_ context.Context, argv []string) ([]byte, error) {
	f.got = argv
	return f.out, f.err
}

func TestRunParsesRunnerOutput(t *testing.T) {
	f := &fakeRunner{out: []byte(sampleReport)}
	rep, err := Run(context.Background(), f, Options{URL: "https://example.com"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rep.Findings) != 2 {
		t.Errorf("want 2 findings from runner output, got %d", len(rep.Findings))
	}
	if len(f.got) == 0 {
		t.Error("runner was never invoked")
	}
}

func TestRunSurfacesRunnerError(t *testing.T) {
	f := &fakeRunner{err: errors.New("docker: not found")}
	if _, err := Run(context.Background(), f, Options{URL: "https://x"}); err == nil {
		t.Error("Run should surface a runner error")
	}
}

func TestRunRejectsEmptyURL(t *testing.T) {
	if _, err := Run(context.Background(), &fakeRunner{}, Options{}); err == nil {
		t.Error("Run should reject an empty URL")
	}
}
