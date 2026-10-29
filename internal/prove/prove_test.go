// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package prove

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BaryoDev/BaryoVM/internal/ship"
)

func goodRecord() *ship.Record {
	return &ship.Record{
		Schema: 1, Name: "lib", Stage: ship.StageFull, Version: "1.2.4", Tag: "v1.2.4",
		Commit: "fcaac08aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Registry: "nuget",
		StartedAt: "2026-08-19T10:00:00Z", FinishedAt: "2026-08-19T10:05:00Z",
		Checks:     []ship.Check{{Name: ship.CheckDeployed, OK: true, Observed: "not required"}},
		Diff:       &ship.DiffSummary{From: "v1.2.3", To: "v1.2.4", ShortStat: "1 file changed", Files: []string{"src/a.csproj"}},
		Steps:      []ship.StepResult{{Name: "tag", Status: "ok"}},
		Publish:    &ship.StepResult{Name: "Publish", Status: "ok"},
		Published:  []ship.Published{{Package: "Acme.Lib", Version: "1.2.4"}},
		Assertions: []ship.Assertion{{Package: "Acme.Lib", Assert: "nuspec declares 8.0.30", OK: true, Observed: "8.0.30"}},
		OK:         true,
	}
}

func write(t *testing.T, home string, r *ship.Record) {
	t.Helper()
	p, err := ship.RecordPath(home, r.Name, r.Version)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(r)
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestNoDeployCheckRendersNotVerifiedNotOmitted(t *testing.T) {
	for _, tc := range []struct {
		name   string
		checks []ship.Check
	}{
		{"required: not required", []ship.Check{{Name: ship.CheckDeployed, OK: true, Observed: "not required"}}},
		{"no deployed check at all", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := goodRecord()
			r.Checks = tc.checks
			p, err := Build(r)
			if err != nil {
				t.Fatal(err)
			}
			if p.Deployed.Status != NotChecked || !strings.Contains(p.Deployed.Note, "deployment not verified") {
				t.Errorf("deployed: %+v", p.Deployed)
			}
			txt := p.Text()
			if !strings.Contains(txt, "DEPLOYED (not checked)") || !strings.Contains(txt, "deployment not verified") {
				t.Errorf("the section is missing:\n%s", txt)
			}
			if !p.OK {
				t.Error("an unverified deployment is not a failure")
			}
		})
	}
}

func TestVerifiedDeploymentShowsWhatTheGateSaw(t *testing.T) {
	r := goodRecord()
	r.Checks = []ship.Check{{Name: ship.CheckDeployed, OK: true, Observed: "shop released abcd1234"}}
	p, _ := Build(r)
	if p.Deployed.Status != OK || !strings.Contains(p.Deployed.Note, "shop released abcd1234") {
		t.Errorf("deployed: %+v", p.Deployed)
	}
}

func TestFailedAssertionAndStepRenderAsFailed(t *testing.T) {
	r := goodRecord()
	r.Assertions = append(r.Assertions, ship.Assertion{Package: "Acme.Lib", Assert: "no vulnerable packages", OK: false, Observed: "GHSA-x"})
	r.Steps = append(r.Steps, ship.StepResult{Name: "github-release", Status: "failed", Detail: "403"})
	p, _ := Build(r)
	if p.OK || p.Published.Status != Failed {
		t.Fatalf("ok=%v published=%s", p.OK, p.Published.Status)
	}
	txt := p.Text()
	for _, want := range []string{"PUBLISHED (FAILED)", "FAILED: Acme.Lib: no vulnerable packages (observed: GHSA-x)", "RESULT  FAILED", "step github-release failed: 403"} {
		if !strings.Contains(txt, want) {
			t.Errorf("missing %q in:\n%s", want, txt)
		}
	}
}

func TestUnfinishedReleaseIsAFailure(t *testing.T) {
	r := goodRecord()
	r.OK, r.Error = false, "workflow run failed"
	p, _ := Build(r)
	if p.OK || !strings.Contains(p.Failures[0], "workflow run failed") {
		t.Errorf("%+v", p.Failures)
	}
}

func TestTagOnlyRecordSaysThePublishWasNotChecked(t *testing.T) {
	r := goodRecord()
	r.Stage, r.Publish, r.Published, r.Assertions, r.Diff = ship.StageTag, nil, nil, nil, nil
	p, _ := Build(r)
	if p.Published.Status != NotChecked || p.Shipped.Status != NotChecked {
		t.Errorf("published=%s shipped=%s", p.Published.Status, p.Shipped.Status)
	}
	if !strings.Contains(p.Text(), "SHIPPED (not checked)") {
		t.Error(p.Text())
	}
}

func TestTextAndJSONComeFromOneStruct(t *testing.T) {
	p, _ := Build(goodRecord())
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var back Proof
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.Text() != p.Text() {
		t.Errorf("text rendered from the decoded JSON differs:\n%s\n---\n%s", back.Text(), p.Text())
	}
	for _, want := range []string{"lib 1.2.4", "fcaac08", "19 Aug 2026", "Acme.Lib at 1.2.4", "observed: 8.0.30"} {
		if !strings.Contains(p.Text(), want) {
			t.Errorf("missing %q in:\n%s", want, p.Text())
		}
	}
}

func TestDryRunRecordIsRefused(t *testing.T) {
	r := goodRecord()
	r.DryRun = true
	if _, err := Build(r); err == nil || !strings.Contains(err.Error(), "dry run") {
		t.Errorf("err = %v", err)
	}
}

func TestMissingRecordIsRefusedNamingThePath(t *testing.T) {
	home := t.TempDir()
	write(t, home, goodRecord())
	_, err := Load(home, "lib", "9.9.9")
	want, _ := ship.RecordPath(home, "lib", "9.9.9")
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("err = %v, want it to name %s", err, want)
	}
	_, err = Load(home, "other", "")
	if err == nil || !strings.Contains(err.Error(), filepath.Join(home, "ship", "other")) {
		t.Errorf("err = %v", err)
	}
}

func TestLatestIsBySemverNotFileTime(t *testing.T) {
	home := t.TempDir()
	for _, v := range []string{"1.2.4", "1.10.0", "1.9.9", "1.10.0-beta.1", "2.0.0-rc.1"} {
		r := goodRecord()
		r.Version = v
		write(t, home, r)
	}
	// The highest version gets the oldest file time.
	old := time.Now().Add(-48 * time.Hour)
	p, _ := ship.RecordPath(home, "lib", "2.0.0-rc.1")
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	got, err := Latest(home, "lib")
	if err != nil || got != "2.0.0-rc.1" {
		t.Errorf("latest = %q, %v", got, err)
	}
	_ = os.Remove(p)
	if got, _ := Latest(home, "lib"); got != "1.10.0" {
		t.Errorf("latest = %q, want 1.10.0 (a release beats its prerelease)", got)
	}
	rec, err := Load(home, "lib", "")
	if err != nil || rec.Version != "1.10.0" {
		t.Errorf("Load default: %v %v", rec, err)
	}
	if rec, err := Load(home, "lib", "1.9.9"); err != nil || rec.Version != "1.9.9" {
		t.Errorf("Load pinned: %v %v", rec, err)
	}
}

func TestCompareVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"1.2.3", "1.2.3", 0}, {"1.2.3", "1.2.10", -1}, {"1.0", "1.0.0", 0}, {"1.0.0-alpha", "1.0.0", -1},
		{"1.0.0-alpha.2", "1.0.0-alpha.10", -1}, {"1.0.0-alpha", "1.0.0-alpha.1", -1}, {"1.0.0-1", "1.0.0-a", -1},
		{"1.0.0+b1", "1.0.0+b2", 0},
	} {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("Compare(%s,%s) = %d, want %d", c.a, c.b, got, c.want)
		}
		if got := CompareVersions(c.b, c.a); got != -c.want {
			t.Errorf("Compare(%s,%s) = %d, want %d", c.b, c.a, got, -c.want)
		}
	}
}

func TestPathEscapesAreRefused(t *testing.T) {
	home := t.TempDir()
	if _, err := Load(home, "..", "1.0.0"); err == nil {
		t.Error("name .. accepted")
	}
	if _, err := Load(home, "lib", "../../x"); err == nil {
		t.Error("version ../../x accepted")
	}
}

func TestRecordForAnotherNameOrVersionIsRefused(t *testing.T) {
	home := t.TempDir()
	r := goodRecord()
	p, _ := ship.RecordPath(home, "lib", "1.2.4")
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	r.Version = "1.2.5"
	b, _ := json.Marshal(r)
	_ = os.WriteFile(p, b, 0o600)
	if _, err := Load(home, "lib", "1.2.4"); err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Errorf("err = %v", err)
	}
}
