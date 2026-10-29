// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package scan runs an OWASP ZAP baseline scan against a URL and turns its
// report into findings the cli layer can render or gate a release on.
//
// The scan is ZAP's *baseline* only: it spiders the target and reports what
// passive analysis sees (missing headers, cookie flags, information leaks). It
// sends no attack payloads, so it is safe to run against a site you are
// authorised to reach. Active scanning is deliberately not exposed here.
//
// This package never prints. It shells out (locally or over SSH via a Runner)
// to the official ZAP Docker image, parses the JSON report, and returns a
// Report. The cli layer decides how to show it.
package scan

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Severity is a ZAP risk level, ordered so higher is worse.
type Severity int

const (
	SeverityInformational Severity = iota
	SeverityLow
	SeverityMedium
	SeverityHigh
)

// severityNames maps ZAP's "riskcode" string (0..3) to a Severity. ZAP reports
// riskcode as a decimal string in the JSON: "0" info, "1" low, "2" medium,
// "3" high.
var severityByRiskcode = map[string]Severity{
	"0": SeverityInformational,
	"1": SeverityLow,
	"2": SeverityMedium,
	"3": SeverityHigh,
}

// String renders a Severity as the lowercase word used in flags and output.
func (s Severity) String() string {
	switch s {
	case SeverityHigh:
		return "high"
	case SeverityMedium:
		return "medium"
	case SeverityLow:
		return "low"
	default:
		return "informational"
	}
}

// ParseSeverity turns a --fail-on value into a Severity. It accepts the four
// level words and "info" as an alias for informational. An empty or unknown
// value is an error so a typo in CI does not silently disable the gate.
func ParseSeverity(s string) (Severity, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "high":
		return SeverityHigh, nil
	case "medium":
		return SeverityMedium, nil
	case "low":
		return SeverityLow, nil
	case "info", "informational":
		return SeverityInformational, nil
	default:
		return 0, fmt.Errorf("unknown severity %q: use high, medium, low, or informational", s)
	}
}

// Finding is one ZAP alert, flattened to what a reader acts on.
type Finding struct {
	Name       string   `json:"name"`
	Severity   string   `json:"severity"`
	Confidence string   `json:"confidence,omitempty"`
	Count      int      `json:"count"`
	CWE        string   `json:"cwe,omitempty"`
	Solution   string   `json:"solution,omitempty"`
	URLs       []string `json:"urls,omitempty"`
	// Check names the edge check that produced a finding (caa, spf, dmarc,
	// bare-ip, security.txt, hsts, edge). Empty for ZAP findings.
	Check string `json:"check,omitempty"`
	// Detail is a value the reader wants verbatim, like an HSTS header or the
	// CAs a CAA record allows. Empty for ZAP findings.
	Detail string `json:"detail,omitempty"`

	severity Severity // parsed, for gating; not serialised
}

// WithFindings returns a copy of the report with extra findings merged in,
// counts recomputed and the list sorted worst-first. Existing findings and the
// Target are untouched, so a ZAP report plus edge findings is one list.
func (r Report) WithFindings(extra []Finding) Report {
	out := Report{Target: r.Target, Findings: append([]Finding{}, r.Findings...), Counts: map[string]int{}}
	out.Findings = append(out.Findings, extra...)
	sortFindings(out.Findings)
	for _, f := range out.Findings {
		out.Counts[f.Severity]++
	}
	return out
}

// sortFindings orders worst first, then by name so the order is stable for
// tests and diffs.
func sortFindings(fs []Finding) {
	sort.SliceStable(fs, func(i, j int) bool {
		if fs[i].severity != fs[j].severity {
			return fs[i].severity > fs[j].severity
		}
		return fs[i].Name < fs[j].Name
	})
}

// Report is the outcome of a scan.
type Report struct {
	Target   string    `json:"target"`
	Findings []Finding `json:"findings"`
	// Counts is a severity word to number map for a quick summary.
	Counts map[string]int `json:"counts"`
}

// FailsAt reports whether any finding is at or above threshold, which is how a
// CI gate decides its exit code.
func (r Report) FailsAt(threshold Severity) bool {
	for _, f := range r.Findings {
		if f.severity >= threshold {
			return true
		}
	}
	return false
}

// The ZAP baseline JSON report shape, only the fields we read. ZAP groups
// alerts under one site entry per scanned origin.
type zapReport struct {
	Site []struct {
		Alerts []zapAlert `json:"alerts"`
	} `json:"site"`
}

type zapAlert struct {
	Alert      string `json:"alert"`
	Riskcode   string `json:"riskcode"`
	Confidence string `json:"confidence"`
	Count      string `json:"count"`
	CWEID      string `json:"cweid"`
	Solution   string `json:"solution"`
	Instances  []struct {
		URI string `json:"uri"`
	} `json:"instances"`
}

// maxURLs caps how many example URLs we keep per finding so a report of a large
// site does not carry thousands of near-identical instances.
const maxURLs = 10

// ParseReport turns ZAP's baseline JSON into a Report. It is separated from the
// running of ZAP so it can be tested against a canned report with no host and
// no Docker, which is the whole point of keeping the engine host-free.
// Text fields go through cleanText: ZAP copies strings from the scanned site, and the cli prints them.
func ParseReport(target string, raw []byte) (Report, error) {
	var zr zapReport
	if err := json.Unmarshal(raw, &zr); err != nil {
		return Report{}, fmt.Errorf("parse zap report: %w", err)
	}

	rep := Report{Target: target, Findings: []Finding{}, Counts: map[string]int{}}
	for _, site := range zr.Site {
		for _, a := range site.Alerts {
			sev, ok := severityByRiskcode[a.Riskcode]
			if !ok {
				sev = SeverityInformational
			}
			f := Finding{
				Name:       cleanText(a.Alert),
				Severity:   sev.String(),
				Confidence: cleanText(a.Confidence),
				Count:      atoiOr(a.Count, len(a.Instances)),
				CWE:        cleanText(cwe(a.CWEID)),
				Solution:   cleanText(a.Solution),
				severity:   sev,
			}
			for i, inst := range a.Instances {
				if i >= maxURLs {
					break
				}
				if inst.URI != "" {
					f.URLs = append(f.URLs, cleanText(inst.URI))
				}
			}
			rep.Findings = append(rep.Findings, f)
			rep.Counts[sev.String()]++
		}
	}

	sortFindings(rep.Findings)
	return rep, nil
}

// cwe formats a CWE id as "CWE-79", dropping ZAP's "-1"/"0"/"" non-values.
func cwe(id string) string {
	switch strings.TrimSpace(id) {
	case "", "-1", "0":
		return ""
	default:
		return "CWE-" + id
	}
}

// atoiOr parses a base-10 int, falling back to def when the string is not a
// number. ZAP writes count as a string; a malformed one should not lose the
// finding, so we fall back to the instance count.
func atoiOr(s string, def int) int {
	n := 0
	if s == "" {
		return def
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return def
		}
		n = n*10 + int(r-'0')
	}
	return n
}
