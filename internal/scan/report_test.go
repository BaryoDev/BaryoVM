// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package scan

import (
	"encoding/json"
	"strings"
	"testing"
)

const zapSample = `{"site":[{"alerts":[
 {"alert":"Missing Anti-clickjacking Header","riskcode":"2","confidence":"2","count":"3","cweid":"1021","solution":"add a header","instances":[{"uri":"https://example.com/"}]},
 {"alert":"Server Leaks Version","riskcode":"1","confidence":"3","count":"1","cweid":"200","instances":[]}
]}]}`

func TestWithFindingsMergesIntoOneList(t *testing.T) {
	zap, err := ParseReport("https://example.com", []byte(zapSample))
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(zap)

	edge, err := RunEdge(t.Context(), "https://example.com", EdgeOptions{Resolver: &fakeResolver{}, Fetcher: &fakeFetcher{}})
	if err != nil {
		t.Fatal(err)
	}
	merged := zap.WithFindings(edge)

	if len(merged.Findings) != len(zap.Findings)+len(edge) {
		t.Fatalf("merged %d findings, want %d", len(merged.Findings), len(zap.Findings)+len(edge))
	}
	after, _ := json.Marshal(zap)
	if string(before) != string(after) {
		t.Error("WithFindings changed the report it was called on")
	}
	if merged.Findings[0].Severity != "medium" {
		t.Errorf("first finding = %+v, want the medium ZAP alert first", merged.Findings[0])
	}
	total := 0
	for _, n := range merged.Counts {
		total += n
	}
	if total != len(merged.Findings) {
		t.Errorf("counts %v do not add up to %d", merged.Counts, len(merged.Findings))
	}
	// Same gate: a medium ZAP alert fails --fail-on medium, edge lows do not on their own.
	if !merged.FailsAt(SeverityMedium) {
		t.Error("medium threshold should fail")
	}
	if (Report{}).WithFindings(edge).FailsAt(SeverityMedium) {
		t.Error("edge findings of low severity must not fail a medium gate")
	}
}

func TestJSONEnvelopeKeepsExistingFields(t *testing.T) {
	zap, _ := ParseReport("https://example.com", []byte(zapSample))
	b, err := json.Marshal(zap)
	if err != nil {
		t.Fatal(err)
	}
	// A ZAP finding serialises exactly as it did before #107: no check or detail.
	if strings.Contains(string(b), `"check"`) || strings.Contains(string(b), `"detail"`) {
		t.Errorf("ZAP-only report grew new fields: %s", b)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"target", "findings", "counts"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("envelope lost %q", k)
		}
	}
	var fs []map[string]any
	_ = json.Unmarshal(raw["findings"], &fs)
	for _, k := range []string{"name", "severity", "count"} {
		if _, ok := fs[0][k]; !ok {
			t.Errorf("finding lost %q", k)
		}
	}

	// An edge finding keeps the same base fields and adds check and detail.
	edge := Report{}.WithFindings([]Finding{{Name: "HSTS header", Severity: "informational", Count: 1, Check: "hsts", Detail: "not set"}})
	b, _ = json.Marshal(edge)
	for _, k := range []string{`"name"`, `"severity"`, `"count"`, `"check":"hsts"`, `"detail":"not set"`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("edge JSON %s misses %s", b, k)
		}
	}
}
