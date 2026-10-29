// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package repoinit

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

var usesRe = regexp.MustCompile(`(?m)^\s*-?\s*uses:\s*(\S+)`)
var shaRe = regexp.MustCompile(`^[^@\s]+@[0-9a-f]{40}$`)

func builtinNames(t *testing.T) []string {
	t.Helper()
	n := Names("")
	if len(n) != 2 || n[0] != "dotnet-nuget" || n[1] != "node-npm" {
		t.Fatalf("built-in templates: %v", n)
	}
	return n
}

func TestBuiltinTemplatesLoadAndKeepTheOIDCExchange(t *testing.T) {
	for _, name := range builtinNames(t) {
		tpl, err := Load(name, "")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		wf := string(tpl.Files[".github/workflows/"+tpl.Manifest.Workflow])
		if !strings.Contains(wf, "id-token: write") {
			t.Errorf("%s: publish workflow lost id-token: write", name)
		}
		for _, step := range tpl.Manifest.OIDCSteps {
			if !strings.Contains(wf, "name: "+step) {
				t.Errorf("%s: publish workflow has no step named %q", name, step)
			}
		}
		for _, f := range []string{".github/CODEOWNERS", ".github/ISSUE_TEMPLATE/bug_report.md", "CONTRIBUTING.md", ".github/workflows/ci.yml"} {
			if _, ok := tpl.Files[f]; !ok {
				t.Errorf("%s: no %s", name, f)
			}
		}
		if len(tpl.Manifest.RequiredChecks) == 0 {
			t.Errorf("%s: no required checks", name)
		}
	}
}

// The OIDC exchange must run on a dry run, or a dry run proves nothing.
func TestOIDCStepsHaveNoConditionThatSkipsADryRun(t *testing.T) {
	for _, name := range builtinNames(t) {
		tpl, _ := Load(name, "")
		wf := string(tpl.Files[".github/workflows/"+tpl.Manifest.Workflow])
		for _, step := range tpl.Manifest.OIDCSteps {
			i := strings.Index(wf, "name: "+step)
			rest := wf[i:]
			if j := strings.Index(rest[1:], "\n      - "); j > 0 {
				rest = rest[:j+1]
			}
			if strings.Contains(rest, "\n        if:") {
				t.Errorf("%s: step %q is conditional", name, step)
			}
		}
	}
}

func TestEveryThirdPartyActionIsPinnedToAFullCommitSha(t *testing.T) {
	for _, name := range builtinNames(t) {
		tpl, _ := Load(name, "")
		n := 0
		for p, b := range tpl.Files {
			if !strings.HasPrefix(p, ".github/workflows/") {
				continue
			}
			for _, m := range usesRe.FindAllStringSubmatch(string(b), -1) {
				n++
				if !shaRe.MatchString(m[1]) {
					t.Errorf("%s %s: %q is not pinned to a 40 character sha", name, p, m[1])
				}
			}
		}
		if n == 0 {
			t.Errorf("%s: found no uses: lines, the check proved nothing", name)
		}
	}
}

func TestRenderLeavesNoTokenAndKeepsGitHubExpressions(t *testing.T) {
	for _, name := range builtinNames(t) {
		tpl, _ := Load(name, "")
		out := tpl.Render(Vars{Owner: "acme", Repo: "widgets", CodeOwner: "@acme/core"})
		for p, b := range out {
			if strings.Contains(string(b), "@@") {
				t.Errorf("%s %s: a token was not replaced", name, p)
			}
		}
		if got := string(out[".github/CODEOWNERS"]); !strings.Contains(got, "* @acme/core") {
			t.Errorf("CODEOWNERS: %q", got)
		}
		if !strings.Contains(string(out["CONTRIBUTING.md"]), "widgets") {
			t.Error("CONTRIBUTING.md has no repo name")
		}
		wf := string(out[".github/workflows/"+tpl.Manifest.Workflow])
		if !strings.Contains(wf, "${{ github.ref_name }}") {
			t.Errorf("%s: a ${{ }} expression did not survive rendering", name)
		}
	}
}

func writeDirTemplate(t *testing.T, dir, name, wf string) {
	t.Helper()
	root := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Join(root, "files", ".github", "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := `{"registry":"example.org","workflow":"` + wf + `","oidcSteps":["Get key"],"requiredChecks":["ci"]}`
	if err := os.WriteFile(filepath.Join(root, "template.json"), []byte(m), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "files", ".github", "workflows", wf), []byte("name: mine @@REPO@@\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestADirectoryTemplateOverridesABuiltinOneAndStandsAlone(t *testing.T) {
	dir := t.TempDir()
	writeDirTemplate(t, dir, "node-npm", "release.yml")
	writeDirTemplate(t, dir, "mine", "pub.yml")
	tpl, err := Load("node-npm", dir)
	if err != nil {
		t.Fatal(err)
	}
	if tpl.Manifest.Workflow != "release.yml" || tpl.Source == "built-in" {
		t.Errorf("override not used: %+v", tpl.Manifest)
	}
	if len(tpl.Files) != 1 {
		t.Errorf("a directory template got the shared furniture: %v", len(tpl.Files))
	}
	if got := Names(dir); strings.Join(got, ",") != "dotnet-nuget,mine,node-npm" {
		t.Errorf("names: %v", got)
	}
	if _, err := Load("mine", dir); err != nil {
		t.Error(err)
	}
	other, _ := Load("dotnet-nuget", dir)
	if other == nil || other.Source != "built-in" {
		t.Error("a built-in not in the directory should still load")
	}
}

func TestBadTemplatesAreRefused(t *testing.T) {
	dir := t.TempDir()
	writeDirTemplate(t, dir, "bad", "pub.yml")
	if err := os.Remove(filepath.Join(dir, "bad", "files", ".github", "workflows", "pub.yml")); err != nil {
		t.Fatal(err)
	}
	if _, err := Load("bad", dir); err == nil || !strings.Contains(err.Error(), "pub.yml") {
		t.Errorf("manifest naming a missing workflow: %v", err)
	}
	for _, n := range []string{"../x", "Up", "a/b", ""} {
		if _, err := Load(n, dir); err == nil {
			t.Errorf("name %q accepted", n)
		}
	}
	if _, err := Load("nope", ""); err == nil || !strings.Contains(err.Error(), "dotnet-nuget") {
		t.Errorf("unknown template should list the available ones: %v", err)
	}
	// a link in a template could read a file outside it
	writeDirTemplate(t, dir, "linked", "pub.yml")
	if err := os.Symlink("/etc/hosts", filepath.Join(dir, "linked", "files", "leak")); err != nil {
		t.Fatal(err)
	}
	if _, err := Load("linked", dir); err == nil {
		t.Error("a symlink in a template was accepted")
	}
}

func TestParseRepoRefusesAnythingThatCouldChangeAURLPath(t *testing.T) {
	if r, err := ParseRepo("acme/widgets.net"); err != nil || r.Owner != "acme" || r.Name != "widgets.net" {
		t.Errorf("%v %v", r, err)
	}
	for _, s := range []string{"acme", "acme/", "/x", "a/b/c", "acme/..", "acme/a?b", "ac me/x", "acme/x#y", "-bad/x"} {
		if _, err := ParseRepo(s); err == nil {
			t.Errorf("%q accepted", s)
		}
	}
}

// fakeGH records every call in order.
type fakeGH struct {
	calls      []string
	branch     string
	protectErr error
	protected  Protection
	dispatchEr error
	inputs     map[string]string
	runs       [][]WorkflowRun // one answer per Runs call, the last repeats
	runsCalls  int
	jobs       []Job
	hasProt    bool
	hasProtErr error
}

func (f *fakeGH) DefaultBranch(_ context.Context, _ Repo) (string, error) {
	f.calls = append(f.calls, "default-branch")
	return f.branch, nil
}
func (f *fakeGH) HasProtection(_ context.Context, _ Repo, b string) (bool, error) {
	f.calls = append(f.calls, "has-protection "+b)
	return f.hasProt, f.hasProtErr
}
func (f *fakeGH) ProtectBranch(_ context.Context, _ Repo, b string, p Protection) error {
	f.calls = append(f.calls, "protect "+b)
	f.protected = p
	return f.protectErr
}
func (f *fakeGH) Dispatch(_ context.Context, _ Repo, wf, ref string, in map[string]string) error {
	f.calls = append(f.calls, "dispatch "+wf+" "+ref)
	f.inputs = in
	return f.dispatchEr
}
func (f *fakeGH) Runs(_ context.Context, _ Repo, _, _ string) ([]WorkflowRun, error) {
	f.calls = append(f.calls, "runs")
	i := f.runsCalls
	f.runsCalls++
	if i >= len(f.runs) {
		i = len(f.runs) - 1
	}
	return f.runs[i], nil
}
func (f *fakeGH) Jobs(_ context.Context, _ Repo, _ int64) ([]Job, error) {
	f.calls = append(f.calls, "jobs")
	return f.jobs, nil
}

func opts(t *testing.T, dir string) Options {
	t.Helper()
	tpl, err := Load("dotnet-nuget", "")
	if err != nil {
		t.Fatal(err)
	}
	return Options{Repo: Repo{Owner: "acme", Name: "widgets"}, Template: tpl, Dir: dir}
}

func TestInitDefaultsToAPlanThatWritesNothingAndCallsNothing(t *testing.T) {
	dir := t.TempDir()
	gh := &fakeGH{}
	rep, err := Run(context.Background(), opts(t, dir), gh, false)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.DryRun || len(gh.calls) != 0 {
		t.Errorf("dry %v calls %v", rep.DryRun, gh.calls)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 0 {
		t.Errorf("a plan wrote %d entries", len(ents))
	}
	for _, f := range rep.Files {
		if f.Action != FileCreate || f.Written {
			t.Errorf("%+v", f)
		}
	}
	tp := rep.TrustedPublishing
	if tp.Owner != "acme" || tp.Repository != "widgets" || tp.Workflow != "publish-nuget.yml" || tp.Registry != "nuget.org" {
		t.Errorf("values: %+v", tp)
	}
	if !rep.Protection.Default || rep.Protection.Applied {
		t.Errorf("protection: %+v", rep.Protection)
	}
	if len(rep.NextSteps) != 3 || !strings.Contains(rep.NextSteps[0], "push") || !strings.Contains(rep.NextSteps[1], "by hand") {
		t.Errorf("the ordering is not spelled out: %v", rep.NextSteps)
	}
}

func TestInitWritesFilesThenProtectsTheDefaultBranch(t *testing.T) {
	dir := t.TempDir()
	gh := &fakeGH{branch: "main"}
	o := opts(t, dir)
	o.Approvals = 1
	rep, err := Run(context.Background(), o, gh, true)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, ".github", "workflows", "publish-nuget.yml"))
	if err != nil || !strings.Contains(string(b), "id-token: write") {
		t.Fatalf("workflow not written: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, ".github", "CODEOWNERS")); !strings.Contains(string(got), "* @acme") {
		t.Errorf("CODEOWNERS: %q", got)
	}
	if strings.Join(gh.calls, ",") != "default-branch,has-protection main,protect main" {
		t.Errorf("calls: %v", gh.calls)
	}
	p := gh.protected
	if !p.Strict || !p.CodeOwner || p.Approvals != 1 || len(p.Checks) != 1 || p.Checks[0] != "Build and test" {
		t.Errorf("protection: %+v", p)
	}
	if !rep.Protection.Applied || rep.Protection.Branch != "main" || rep.DryRun {
		t.Errorf("report: %+v", rep.Protection)
	}
	for _, f := range rep.Files {
		if !f.Written {
			t.Errorf("%s not written", f.Path)
		}
	}
}

func TestInitNeverOverwritesAnExistingFileWithoutForce(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, ".github", "workflows", "ci.yml")
	if err := os.MkdirAll(filepath.Dir(wf), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wf, []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gh := &fakeGH{branch: "main"}
	rep, err := Run(context.Background(), opts(t, dir), gh, true)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(wf); string(b) != "mine\n" {
		t.Errorf("existing file overwritten: %q", b)
	}
	var saw bool
	for _, f := range rep.Files {
		if f.Path == ".github/workflows/ci.yml" {
			saw = true
			if f.Action != FileExists || f.Written {
				t.Errorf("%+v", f)
			}
		}
	}
	if !saw {
		t.Error("existing file not reported")
	}

	o := opts(t, dir)
	o.Force = true
	rep, err = Run(context.Background(), o, gh, true)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(wf); string(b) == "mine\n" {
		t.Error("--force did not overwrite")
	}
	for _, f := range rep.Files {
		if f.Path == ".github/workflows/ci.yml" && f.Action != FileOverwrite {
			t.Errorf("%+v", f)
		}
	}
}

func TestInitRefusesToWriteThroughASymlinkEvenWithForce(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(outside, []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "x"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "x")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "CONTRIBUTING.md")); err != nil {
		t.Fatal(err)
	}
	o := opts(t, dir)
	o.Force = true
	_, err := Run(context.Background(), o, &fakeGH{branch: "main"}, true)
	if err == nil {
		t.Fatal("wrote through a symlink")
	}
	if b, _ := os.ReadFile(outside); string(b) != "keep\n" {
		t.Errorf("target changed: %q", b)
	}
}

func TestInitProtectionFailureKeepsTheFilesAndARerunSkipsThem(t *testing.T) {
	dir := t.TempDir()
	gh := &fakeGH{branch: "main", protectErr: errors.New("403 forbidden")}
	rep, err := Run(context.Background(), opts(t, dir), gh, true)
	if err == nil || !strings.Contains(err.Error(), "files are written") {
		t.Fatalf("want a protection error, got %v", err)
	}
	if rep == nil || rep.Protection.Applied {
		t.Errorf("report: %+v", rep)
	}
	gh.protectErr = nil
	rep, err = Run(context.Background(), opts(t, dir), gh, true)
	if err != nil || !rep.Protection.Applied {
		t.Fatalf("rerun: %v", err)
	}
	for _, f := range rep.Files {
		if f.Written || f.Action != FileExists {
			t.Errorf("rerun rewrote %+v", f)
		}
	}
}

func TestInitCustomChecksAndBranchAreUsed(t *testing.T) {
	dir := t.TempDir()
	gh := &fakeGH{}
	o := opts(t, dir)
	o.Checks, o.Branch, o.CodeOwner = []string{"a", "b"}, "release", "@acme/core"
	if _, err := Run(context.Background(), o, gh, true); err != nil {
		t.Fatal(err)
	}
	if strings.Join(gh.calls, ",") != "has-protection release,protect release" || strings.Join(gh.protected.Checks, ",") != "a,b" {
		t.Errorf("%v %+v", gh.calls, gh.protected)
	}
	o.CodeOwner = "acme"
	if _, err := Run(context.Background(), o, gh, false); err == nil {
		t.Error("a codeowner without @ was accepted")
	}
	o.Dir = filepath.Join(dir, "missing")
	if _, err := Run(context.Background(), o, gh, false); err == nil {
		t.Error("a missing --path was accepted")
	}
}

func noSleep(context.Context, time.Duration) error { return nil }

func vopts() VerifyOptions {
	return VerifyOptions{
		Repo: Repo{Owner: "acme", Name: "widgets"}, Workflow: "publish-nuget.yml",
		Steps:  []string{"Exchange", "Confirm"},
		Inputs: map[string]string{"dry_run": "true"}, Timeout: time.Minute, Poll: 10 * time.Second,
		Values: TrustedPublishing{Registry: "nuget.org", Owner: "acme"},
	}
}

var t0 = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

// wr is a run that was dispatched on main at t0, the shape verify accepts as its own.
func wr(id int64, status, conclusion, url string) WorkflowRun {
	return WorkflowRun{ID: id, Status: status, Conclusion: conclusion, URL: url, Event: "workflow_dispatch", Branch: "main", CreatedAt: t0}
}

func vdeps(gh *fakeGH) VerifyDeps {
	return VerifyDeps{GH: gh, Sleep: noSleep, Now: func() time.Time { return t0 }}
}

func steps(ex, conf string) []Job {
	return []Job{{Name: "publish", Steps: []Step{{Name: "Exchange", Conclusion: ex}, {Name: "Confirm", Conclusion: conf}}}}
}

func TestVerifyReportsTookWhenEveryOIDCStepSucceededAndIgnoresOlderRuns(t *testing.T) {
	old := wr(1, "completed", "failure", "")
	gh := &fakeGH{branch: "main", jobs: steps("success", "success"), runs: [][]WorkflowRun{
		{old},
		{old},
		{wr(2, "in_progress", "", ""), old},
		{wr(2, "completed", "success", "u"), old},
	}}
	rep, err := Verify(context.Background(), vopts(), vdeps(gh))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Verdict != Took || rep.RunID != 2 || rep.Ref != "main" {
		t.Errorf("%+v", rep)
	}
	if gh.inputs["dry_run"] != "true" {
		t.Errorf("the dry run switch was not sent: %v", gh.inputs)
	}
	// the dispatch comes after the first look at the runs and before the next
	want := "default-branch,runs,dispatch publish-nuget.yml main,runs,runs,runs,jobs"
	if strings.Join(gh.calls, ",") != want {
		t.Errorf("order:\n got %v\nwant %s", gh.calls, want)
	}
}

func TestVerifyDidNotTakeWhenTheExchangeStepFailed(t *testing.T) {
	gh := &fakeGH{branch: "main", jobs: steps("failure", "skipped"), runs: [][]WorkflowRun{
		{}, {wr(5, "completed", "failure", "")},
	}}
	rep, err := Verify(context.Background(), vopts(), vdeps(gh))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Verdict != DidNotTake || !strings.Contains(rep.Reason, "nuget.org") {
		t.Errorf("%+v", rep)
	}
}

func TestVerifyIsInconclusiveWhenTheRunNeverReachedTheStep(t *testing.T) {
	gh := &fakeGH{branch: "main", jobs: steps("skipped", "skipped"), runs: [][]WorkflowRun{
		{}, {wr(6, "completed", "failure", "https://run")},
	}}
	rep, _ := Verify(context.Background(), vopts(), vdeps(gh))
	if rep.Verdict != Inconclusive || !strings.Contains(rep.Reason, "https://run") {
		t.Errorf("%+v", rep)
	}
	gh = &fakeGH{branch: "main", jobs: []Job{{Name: "publish"}}, runs: [][]WorkflowRun{{}, {wr(6, "completed", "", "")}}}
	rep, _ = Verify(context.Background(), vopts(), vdeps(gh))
	if rep.Verdict != Inconclusive || rep.Steps[0].Conclusion != "not-found" {
		t.Errorf("a step missing from the run must not read as success: %+v", rep)
	}
}

func TestVerifyGivesUpAfterTheTimeoutWithoutAWallClock(t *testing.T) {
	gh := &fakeGH{branch: "main", runs: [][]WorkflowRun{{}}}
	sleeps := 0
	o := vopts()
	rep, err := Verify(context.Background(), o, VerifyDeps{GH: gh, Now: func() time.Time { return t0 }, Sleep: func(context.Context, time.Duration) error { sleeps++; return nil }})
	if err != nil || rep.Verdict != Inconclusive {
		t.Fatalf("%v %+v", err, rep)
	}
	if sleeps != 6 {
		t.Errorf("polled %d times, want timeout/poll = 6", sleeps)
	}
	gh = &fakeGH{branch: "main", runs: [][]WorkflowRun{{}, {wr(9, "in_progress", "", "u")}}}
	rep, _ = Verify(context.Background(), o, vdeps(gh))
	if rep.Verdict != Inconclusive || !strings.Contains(rep.Reason, "had not finished") {
		t.Errorf("%+v", rep)
	}
}

func TestVerifyDispatchRefusedPointsAtPushingTheWorkflowFirst(t *testing.T) {
	gh := &fakeGH{branch: "main", runs: [][]WorkflowRun{{}}, dispatchEr: &StatusError{Status: 404, Method: "POST", Path: "/x", Message: "Not Found"}}
	_, err := Verify(context.Background(), vopts(), vdeps(gh))
	if err == nil || !strings.Contains(err.Error(), "pushed") || !strings.Contains(err.Error(), "before creating the registry policy") {
		t.Errorf("%v", err)
	}
}

func TestAPISendsTheRightRequestsWithATimeoutAndNeverEchoesTheToken(t *testing.T) {
	const token = "ghp_supersecret"
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.RequestURI())
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("auth header: %q", r.Header.Get("Authorization"))
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/protection"):
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"Resource not accessible"}`))
		case strings.HasSuffix(r.URL.Path, "/dispatches"):
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/runs"):
			_, _ = w.Write([]byte(`{"workflow_runs":[{"id":7,"event":"workflow_dispatch","head_branch":"main","created_at":"2026-10-30T12:00:00Z","status":"completed","conclusion":"success","html_url":"h"}]}`))
		case strings.HasSuffix(r.URL.Path, "/jobs"):
			_, _ = w.Write([]byte(`{"jobs":[{"name":"p","conclusion":"success","steps":[{"name":"s","status":"completed","conclusion":"success"}]}]}`))
		default:
			_, _ = w.Write([]byte(`{"default_branch":"trunk"}`))
		}
	}))
	defer srv.Close()
	a := &API{Token: token, Base: srv.URL}
	repo := Repo{Owner: "acme", Name: "widgets"}
	ctx := context.Background()
	if b, err := a.DefaultBranch(ctx, repo); err != nil || b != "trunk" {
		t.Fatalf("%q %v", b, err)
	}
	err := a.ProtectBranch(ctx, repo, "main", Protection{Checks: []string{"x"}})
	if err == nil || !strings.Contains(err.Error(), "403") || strings.Contains(err.Error(), token) {
		t.Errorf("%v", err)
	}
	if err := a.Dispatch(ctx, repo, "publish.yml", "main", map[string]string{"dry_run": "true"}); err != nil {
		t.Fatal(err)
	}
	if runs, err := a.Runs(ctx, repo, "publish.yml", "main"); err != nil || len(runs) != 1 || runs[0].ID != 7 || runs[0].Event != "workflow_dispatch" || runs[0].Branch != "main" || !runs[0].CreatedAt.Equal(t0) {
		t.Fatalf("%v %v", runs, err)
	}
	if jobs, err := a.Jobs(ctx, repo, 7); err != nil || jobs[0].Steps[0].Name != "s" {
		t.Fatalf("%v %v", jobs, err)
	}
	want := []string{
		"GET /repos/acme/widgets",
		"PUT /repos/acme/widgets/branches/main/protection",
		"POST /repos/acme/widgets/actions/workflows/publish.yml/dispatches",
	}
	for i, w := range want {
		if seen[i] != w {
			t.Errorf("request %d: %q want %q", i, seen[i], w)
		}
	}
}

func TestAPICallsTimeOut(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	a := &API{Token: "t", Base: srv.URL, Timeout: 50 * time.Millisecond}
	start := time.Now()
	_, err := a.DefaultBranch(context.Background(), Repo{Owner: "a", Name: "b"})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Errorf("%v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("the call did not stop at its timeout")
	}
}

func TestVerifyIgnoresARunThatIsNotOursAndRefusesToGuessBetweenTwo(t *testing.T) {
	decoys := []WorkflowRun{
		{ID: 20, Status: "completed", Conclusion: "success", Event: "push", Branch: "main", CreatedAt: t0},
		{ID: 21, Status: "completed", Conclusion: "success", Event: "workflow_dispatch", Branch: "other", CreatedAt: t0},
		{ID: 22, Status: "completed", Conclusion: "success", Event: "workflow_dispatch", Branch: "main", CreatedAt: t0.Add(-time.Hour)},
	}
	gh := &fakeGH{branch: "main", jobs: steps("success", "success"), runs: [][]WorkflowRun{{}, append([]WorkflowRun{wr(30, "completed", "success", "u")}, decoys...)}}
	rep, err := Verify(context.Background(), vopts(), vdeps(gh))
	if err != nil || rep.Verdict != Took || rep.RunID != 30 {
		t.Fatalf("a decoy was picked: %v %+v", err, rep)
	}
	// a run created a few seconds before our clock is still ours (clock skew)
	skewed := wr(31, "completed", "success", "u")
	skewed.CreatedAt = t0.Add(-5 * time.Second)
	gh = &fakeGH{branch: "main", jobs: steps("success", "success"), runs: [][]WorkflowRun{{}, {skewed}}}
	if rep, _ = Verify(context.Background(), vopts(), vdeps(gh)); rep.RunID != 31 {
		t.Errorf("skew: %+v", rep)
	}
	gh = &fakeGH{branch: "main", jobs: steps("success", "success"), runs: [][]WorkflowRun{{}, {wr(40, "completed", "success", ""), wr(41, "in_progress", "", "")}}}
	rep, err = Verify(context.Background(), vopts(), vdeps(gh))
	if err != nil || rep.Verdict != Inconclusive || !strings.Contains(rep.Reason, "more than one") || rep.RunID != 0 {
		t.Errorf("two candidates must be inconclusive: %v %+v", err, rep)
	}
}

func TestVerifyWithNoStepsToReadIsInconclusiveNotTook(t *testing.T) {
	gh := &fakeGH{branch: "main", runs: [][]WorkflowRun{{}, {wr(5, "completed", "success", "")}}}
	o := vopts()
	o.Steps = nil
	rep, err := Verify(context.Background(), o, vdeps(gh))
	if err != nil || rep.Verdict != Inconclusive {
		t.Errorf("%v %+v", err, rep)
	}
}

func TestInitLeavesExistingProtectionAloneUnlessToldTo(t *testing.T) {
	gh := &fakeGH{branch: "main", hasProt: true}
	rep, err := Run(context.Background(), opts(t, t.TempDir()), gh, true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(gh.calls, ","), "protect main") || rep.Protection.Applied || !strings.Contains(rep.Protection.Note, "--replace-protection") {
		t.Errorf("existing protection was touched: %v %+v", gh.calls, rep.Protection)
	}
	gh = &fakeGH{branch: "main", hasProt: true}
	o := opts(t, t.TempDir())
	o.ReplaceProtection = true
	rep, err = Run(context.Background(), o, gh, true)
	if err != nil || !rep.Protection.Applied || strings.Join(gh.calls, ",") != "default-branch,protect main" {
		t.Errorf("--replace-protection: %v %v %+v", err, gh.calls, rep.Protection)
	}
	gh = &fakeGH{branch: "main", hasProtErr: errors.New("boom")}
	if _, err = Run(context.Background(), opts(t, t.TempDir()), gh, true); err == nil || strings.Contains(strings.Join(gh.calls, ","), "protect main") {
		t.Errorf("a failed read must not fall through to a PUT: %v %v", err, gh.calls)
	}
}

func TestAPIHasProtectionTreatsOnlyNotProtectedAsNone(t *testing.T) {
	answers := map[string]struct {
		code int
		body string
	}{
		"none":  {404, `{"message":"Branch not protected"}`},
		"gone":  {404, `{"message":"Not Found"}`},
		"yes":   {200, `{"url":"x"}`},
		"nopes": {403, `{"message":"Resource not accessible"}`},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := answers[strings.Split(r.URL.Path, "/")[5]]
		w.WriteHeader(a.code)
		_, _ = w.Write([]byte(a.body))
	}))
	defer srv.Close()
	a := &API{Token: "t", Base: srv.URL}
	repo := Repo{Owner: "acme", Name: "widgets"}
	if ok, err := a.HasProtection(context.Background(), repo, "none"); ok || err != nil {
		t.Errorf("none: %v %v", ok, err)
	}
	if ok, err := a.HasProtection(context.Background(), repo, "yes"); !ok || err != nil {
		t.Errorf("yes: %v %v", ok, err)
	}
	for _, b := range []string{"gone", "nopes"} {
		if _, err := a.HasProtection(context.Background(), repo, b); err == nil {
			t.Errorf("%s: an error was read as unprotected", b)
		}
	}
}

func TestInitRefusesASymlinkedParentFolderInThePlanAndTheApply(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, ".github")); err != nil {
		t.Fatal(err)
	}
	rep, err := Run(context.Background(), opts(t, dir), &fakeGH{branch: "main"}, false)
	if err != nil {
		t.Fatal(err)
	}
	refused := 0
	for _, f := range rep.Files {
		if strings.HasPrefix(f.Path, ".github/") {
			if f.Action != FileRefused {
				t.Errorf("the plan shows %s as %q", f.Path, f.Action)
			}
			refused++
		}
	}
	if refused == 0 {
		t.Fatal("no .github files in the plan")
	}
	gh := &fakeGH{branch: "main"}
	if _, err := Run(context.Background(), opts(t, dir), gh, true); err == nil {
		t.Error("apply went through a symlinked folder")
	}
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Errorf("%d entries landed outside the checkout", len(ents))
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 || len(gh.calls) != 0 {
		t.Errorf("apply wrote or called before refusing: %d entries, calls %v", len(ents), gh.calls)
	}
	// even when the plan was clean, a folder swapped for a link before the write is caught
	dir2 := t.TempDir()
	_, files, err := Plan(opts(t, dir2))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir2, ".github")); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir2)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if err := writeFile(root, ".github/CODEOWNERS", files[".github/CODEOWNERS"], false); err == nil {
		t.Error("writeFile followed a symlinked parent")
	}
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Errorf("%d entries landed outside the checkout", len(ents))
	}
}

// The file appears after the plan and before the write: O_EXCL must keep it.
func TestInitDoesNotReplaceAFileThatAppearedSinceThePlan(t *testing.T) {
	dir := t.TempDir()
	_, files, err := Plan(opts(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "CONTRIBUTING.md"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	err = writeFile(root, "CONTRIBUTING.md", files["CONTRIBUTING.md"], false)
	if err == nil || !strings.Contains(err.Error(), "appeared since the plan") {
		t.Errorf("%v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "CONTRIBUTING.md")); string(b) != "mine\n" {
		t.Errorf("the file was replaced: %q", b)
	}
}

func TestTemplatesMayNotWriteUnderDotGit(t *testing.T) {
	dir := t.TempDir()
	writeDirTemplate(t, dir, "hooky", "pub.yml")
	hook := filepath.Join(dir, "hooky", "files", ".git", "hooks")
	if err := os.MkdirAll(hook, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hook, "pre-commit"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Load("hooky", dir); err == nil || !strings.Contains(err.Error(), ".git") {
		t.Errorf("a template writing .git/hooks was accepted: %v", err)
	}
}

func TestCodeOwnerWithAControlCharacterIsRefused(t *testing.T) {
	for _, bad := range []string{"@acme\n* @evil", "@acme\r", "@acme @evil", "@acme\t"} {
		o := opts(t, t.TempDir())
		o.CodeOwner = bad
		if _, err := Run(context.Background(), o, &fakeGH{}, false); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestEveryTemplateAndTheOutputSayWhoCanPublishByTag(t *testing.T) {
	for _, name := range builtinNames(t) {
		tpl, _ := Load(name, "")
		found := false
		for _, n := range tpl.Manifest.Notes {
			found = found || n == TagNote
		}
		if !found {
			t.Errorf("%s: manifest has no v* tag note", name)
		}
	}
	dir := t.TempDir()
	writeDirTemplate(t, dir, "mine", "pub.yml")
	tpl, err := Load("mine", dir)
	if err != nil {
		t.Fatal(err)
	}
	if v := Values(tpl, Repo{Owner: "a", Name: "b"}); len(v.Notes) != 1 || v.Notes[0] != TagNote {
		t.Errorf("a template without the note must still print it once: %v", v.Notes)
	}
}
