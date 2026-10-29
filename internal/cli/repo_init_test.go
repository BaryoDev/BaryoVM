// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BaryoDev/BaryoVM/internal/repoinit"
)

// initGH is the fake GitHub behind the repo commands. It records every call.
type initGH struct {
	calls []string
	jobs  []repoinit.Job
	runs  [][]repoinit.WorkflowRun
	n     int
}

func (g *initGH) DefaultBranch(context.Context, repoinit.Repo) (string, error) {
	g.calls = append(g.calls, "default-branch")
	return "main", nil
}
func (g *initGH) HasProtection(_ context.Context, _ repoinit.Repo, b string) (bool, error) {
	g.calls = append(g.calls, "has-protection "+b)
	return false, nil
}
func (g *initGH) ProtectBranch(_ context.Context, _ repoinit.Repo, b string, _ repoinit.Protection) error {
	g.calls = append(g.calls, "protect "+b)
	return nil
}
func (g *initGH) Dispatch(_ context.Context, _ repoinit.Repo, wf, ref string, in map[string]string) error {
	g.calls = append(g.calls, "dispatch "+wf+" "+ref+" dry_run="+in["dry_run"])
	return nil
}
func (g *initGH) Runs(context.Context, repoinit.Repo, string, string) ([]repoinit.WorkflowRun, error) {
	g.calls = append(g.calls, "runs")
	i := g.n
	g.n++
	if i >= len(g.runs) {
		i = len(g.runs) - 1
	}
	return g.runs[i], nil
}
func (g *initGH) Jobs(context.Context, repoinit.Repo, int64) ([]repoinit.Job, error) {
	g.calls = append(g.calls, "jobs")
	return g.jobs, nil
}

// repoInitWorld fakes the GitHub client and the token lookup, and counts token reads.
func repoInitWorld(t *testing.T, gh *initGH) *int {
	t.Helper()
	pg, pt, ps := repoGitHub, repoToken, repoSleep
	reads := 0
	repoGitHub = func(string) repoinit.GitHub { return gh }
	repoToken = func(ref string) (string, error) { reads++; return "tok", nil }
	repoSleep = func(context.Context, time.Duration) error { return nil }
	t.Cleanup(func() { repoGitHub, repoToken, repoSleep = pg, pt, ps })
	return &reads
}

// dispatched is a run verify accepts as its own: a dispatch on main created after the clock read.
func dispatched(id int64, status, conclusion, url string) repoinit.WorkflowRun {
	return repoinit.WorkflowRun{ID: id, Status: status, Conclusion: conclusion, URL: url,
		Event: "workflow_dispatch", Branch: "main", CreatedAt: time.Now().Add(time.Minute)}
}

type repoEnvelope struct {
	OK     bool            `json:"ok"`
	Action string          `json:"action"`
	Data   json.RawMessage `json:"data"`
	Error  string          `json:"error"`
}

func TestRepoInitIsAPlanByDefaultWithNoTokenReadNoCallAndNoFile(t *testing.T) {
	gh := &initGH{}
	reads := repoInitWorld(t, gh)
	dir := t.TempDir()
	stdout, _, err := runCmdE(t, newRepoInitCmd(), nil, true, "acme/widgets", "--template", "dotnet-nuget", "--path", dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(gh.calls) != 0 || *reads != 0 {
		t.Errorf("calls %v, token reads %d", gh.calls, *reads)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Errorf("wrote %d entries", len(ents))
	}
	var env repoEnvelope
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("%v: %q", err, stdout)
	}
	var rep repoinit.Report
	if err := json.Unmarshal(env.Data, &rep); err != nil {
		t.Fatal(err)
	}
	if !env.OK || env.Action != "repo init" || !rep.DryRun {
		t.Errorf("%+v", env)
	}
	tp := rep.TrustedPublishing
	if tp.Owner != "acme" || tp.Repository != "widgets" || tp.Workflow != "publish-nuget.yml" {
		t.Errorf("the four values: %+v", tp)
	}
	// --yes with --dry-run is still a plan
	_, _, err = runCmdE(t, newRepoInitCmd(), nil, true, "acme/widgets", "--template", "dotnet-nuget", "--path", dir, "--yes", "--dry-run")
	if err != nil || len(gh.calls) != 0 {
		t.Errorf("--dry-run did not win: %v %v", err, gh.calls)
	}
}

func TestRepoInitYesWritesAndProtects(t *testing.T) {
	gh := &initGH{}
	reads := repoInitWorld(t, gh)
	dir := t.TempDir()
	stdout, _, err := runCmdE(t, newRepoInitCmd(), nil, true, "acme/widgets", "--template", "node-npm", "--path", dir, "--yes", "--approvals", "1")
	if err != nil {
		t.Fatal(err)
	}
	if *reads != 1 || strings.Join(gh.calls, ",") != "default-branch,has-protection main,protect main" {
		t.Errorf("reads %d calls %v", *reads, gh.calls)
	}
	if _, err := os.Stat(filepath.Join(dir, ".github", "workflows", "publish.yml")); err != nil {
		t.Error(err)
	}
	var env repoEnvelope
	if err := json.Unmarshal([]byte(stdout), &env); err != nil || !env.OK {
		t.Fatalf("%v %q", err, stdout)
	}
	if strings.Contains(stdout, `"tok"`) {
		t.Error("the token is in the repoEnvelope")
	}
}

func TestRepoInitRefusesBadInputBeforeAnyCall(t *testing.T) {
	gh := &initGH{}
	repoInitWorld(t, gh)
	dir := t.TempDir()
	for _, args := range [][]string{
		{"acme", "--template", "dotnet-nuget"},
		{"acme/widgets", "--template", "nope"},
		{"acme/widgets", "--template", "dotnet-nuget", "--approvals", "9"},
		{"acme/widgets", "--template", "dotnet-nuget", "--token-ref", "nonsense"},
		{"acme/widgets", "--template", "dotnet-nuget", "--path", filepath.Join(dir, "missing")},
	} {
		stdout, _, err := runCmdE(t, newRepoInitCmd(), nil, true, args...)
		if err == nil {
			t.Errorf("%v accepted", args)
			continue
		}
		var env repoEnvelope
		if json.Unmarshal([]byte(stdout), &env) != nil || env.OK || env.Error == "" {
			t.Errorf("%v: no failure repoEnvelope: %q", args, stdout)
		}
	}
	if len(gh.calls) != 0 {
		t.Errorf("calls: %v", gh.calls)
	}
}

func TestRepoInitTemplatesDirOverridesBuiltin(t *testing.T) {
	gh := &initGH{}
	repoInitWorld(t, gh)
	td := t.TempDir()
	root := filepath.Join(td, "dotnet-nuget", "files", ".github", "workflows")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	m := `{"registry":"example.org","workflow":"mine.yml","oidcSteps":["Get key"],"requiredChecks":["ci"]}`
	if err := os.WriteFile(filepath.Join(td, "dotnet-nuget", "template.json"), []byte(m), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "mine.yml"), []byte("name: mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	stdout, _, err := runCmdE(t, newRepoInitCmd(), nil, true, "acme/w", "--template", "dotnet-nuget", "--templates-dir", td, "--path", dir)
	if err != nil || !strings.Contains(stdout, "mine.yml") || strings.Contains(stdout, "publish-nuget.yml") {
		t.Errorf("%v %q", err, stdout)
	}
}

func verifyGH(ex, conf string) *initGH {
	return &initGH{
		runs: [][]repoinit.WorkflowRun{{}, {dispatched(3, "completed", "success", "https://run/3")}},
		jobs: []repoinit.Job{{Name: "publish", Steps: []repoinit.Step{
			{Name: "Exchange OIDC token for a short-lived NuGet key", Conclusion: ex},
			{Name: "Confirm the key was issued", Conclusion: conf},
		}}},
	}
}

func TestRepoVerifyTriggersTheDryRunAndReportsTook(t *testing.T) {
	gh := verifyGH("success", "success")
	repoInitWorld(t, gh)
	stdout, _, err := runCmdE(t, newRepoVerifyCmd(), nil, true, "acme/widgets", "--template", "dotnet-nuget")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(gh.calls, ","), "dispatch publish-nuget.yml main dry_run=true") {
		t.Errorf("calls: %v", gh.calls)
	}
	var env repoEnvelope
	if err := json.Unmarshal([]byte(stdout), &env); err != nil || !env.OK || env.Action != "repo verify" {
		t.Fatalf("%v %q", err, stdout)
	}
	var rep repoinit.VerifyReport
	_ = json.Unmarshal(env.Data, &rep)
	if rep.Verdict != repoinit.Took || len(rep.Steps) != 2 {
		t.Errorf("%+v", rep)
	}
}

func TestRepoVerifyFailsWithTheValuesWhenTheExchangeFailed(t *testing.T) {
	gh := verifyGH("failure", "skipped")
	repoInitWorld(t, gh)
	stdout, _, err := runCmdE(t, newRepoVerifyCmd(), nil, true, "acme/widgets", "--template", "dotnet-nuget")
	if err == nil {
		t.Fatal("a failed exchange exited zero")
	}
	var env repoEnvelope
	if json.Unmarshal([]byte(stdout), &env) != nil || env.OK || !strings.Contains(env.Error, "did-not-take") {
		t.Fatalf("%q", stdout)
	}
	var rep repoinit.VerifyReport
	_ = json.Unmarshal(env.Data, &rep)
	if rep.Values.Workflow != "publish-nuget.yml" || rep.Values.Owner != "acme" {
		t.Errorf("the values to recheck are missing: %+v", rep.Values)
	}
}

func TestRepoVerifyNeedsATemplateOrAWorkflowAndStep(t *testing.T) {
	gh := verifyGH("success", "success")
	reads := repoInitWorld(t, gh)
	if _, _, err := runCmdE(t, newRepoVerifyCmd(), nil, true, "acme/widgets"); err == nil {
		t.Error("verify with nothing to read was accepted")
	}
	if *reads != 0 || len(gh.calls) != 0 {
		t.Errorf("reads %d calls %v", *reads, gh.calls)
	}
	gh2 := &initGH{
		runs: [][]repoinit.WorkflowRun{{}, {dispatched(4, "completed", "", "")}},
		jobs: []repoinit.Job{{Steps: []repoinit.Step{{Name: "Get key", Conclusion: "success"}}}},
	}
	repoInitWorld(t, gh2)
	_, _, err := runCmdE(t, newRepoVerifyCmd(), nil, true, "acme/widgets", "--workflow", "release.yml", "--step", "Get key")
	if err != nil {
		t.Errorf("custom workflow and step: %v", err)
	}
}

func TestRepoTokenResolvesFromTheEnvironmentAndNeverFromAnError(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ghp_fromenv")
	if v, err := repoToken(""); err != nil || v != "ghp_fromenv" {
		t.Errorf("%q %v", v, err)
	}
	t.Setenv("MY_TOK", "ghp_mine")
	if v, err := repoToken("env:MY_TOK"); err != nil || v != "ghp_mine" {
		t.Errorf("%q %v", v, err)
	}
	if _, err := repoToken("env:NOT_SET_ANYWHERE_52"); err == nil || strings.Contains(err.Error(), "ghp_") {
		t.Errorf("%v", err)
	}
}
