// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package ship

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

const head = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

const csproj = `<Project><PropertyGroup><Version>1.3.0</Version><PackageId>Acme.Lib</PackageId>
<PackageReleaseNotes>1.3.0: moves EF to 8.0.30</PackageReleaseNotes></PropertyGroup></Project>`

const changelog = "# Changelog\n\n## [1.3.0] - 2026-10-30\n- moves EF to 8.0.30\n\n## [1.2.0]\n- old\n"

// fakeGit answers git by its first words and records every call, so a test can assert that
// nothing mutating ran.
type fakeGit struct {
	status, branch, remoteHead, remoteURL string
	localTag, remoteTag                   string // sha, "" for none
	calls                                 []string
}

func newGit() *fakeGit {
	return &fakeGit{branch: "main", remoteHead: head, remoteURL: "git@github.com:acme/lib.git"}
}

func (g *fakeGit) Run(_ context.Context, _ string, args ...string) (string, error) {
	cmd := strings.Join(args, " ")
	g.calls = append(g.calls, cmd)
	switch {
	case args[0] == "status":
		return g.status, nil
	case cmd == "rev-parse HEAD":
		return head + "\n", nil
	case cmd == "rev-parse --abbrev-ref HEAD":
		return g.branch + "\n", nil
	case args[0] == "symbolic-ref":
		return "origin/main\n", nil
	case args[0] == "ls-remote" && strings.Contains(cmd, "refs/heads/"):
		return g.remoteHead + "\trefs/heads/main\n", nil
	case args[0] == "ls-remote" && strings.Contains(cmd, "--tags"):
		if g.remoteTag == "" {
			return "", nil
		}
		return g.remoteTag + "\trefs/tags/v1.3.0\n", nil
	case args[0] == "rev-parse" && args[1] == "-q":
		if g.localTag == "" {
			return "", errors.New("exit 1")
		}
		return g.localTag + "\n", nil
	case args[0] == "remote":
		return g.remoteURL + "\n", nil
	case args[0] == "describe":
		return "v1.2.0\n", nil
	case args[0] == "diff" && args[1] == "--shortstat":
		return " 3 files changed\n", nil
	case args[0] == "diff":
		return "M\tsrc/a.cs\n", nil
	case args[0] == "log":
		return "abc1234 move EF\n", nil
	case args[0] == "tag":
		g.localTag = head
		return "", nil
	case args[0] == "push":
		g.remoteTag = head
		return "", nil
	}
	return "", fmt.Errorf("fakeGit: unexpected %q", cmd)
}

func (g *fakeGit) mutations() []string {
	var m []string
	for _, c := range g.calls {
		if strings.HasPrefix(c, "tag ") || strings.HasPrefix(c, "push ") {
			m = append(m, c)
		}
	}
	return m
}

type fakeReg struct {
	published map[string]bool // by package
	meta      string
	queries   int
	failAfter int // published becomes true after this many Published calls, when > 0
}

func (r *fakeReg) Name() string { return NuGet }
func (r *fakeReg) Version(f []byte) (string, error) {
	return nugetRegistry{}.Version(f)
}
func (r *fakeReg) PackageName(p string, f []byte) string { return nugetRegistry{}.PackageName(p, f) }
func (r *fakeReg) ReleaseNotes(f []byte) (string, bool)  { return nugetRegistry{}.ReleaseNotes(f) }
func (r *fakeReg) Published(_ context.Context, pkg, _ string) (bool, error) {
	r.queries++
	if r.failAfter > 0 && r.queries > r.failAfter {
		return true, nil
	}
	return r.published[pkg], nil
}
func (r *fakeReg) Metadata(context.Context, string, string) ([]byte, error) {
	return []byte(r.meta), nil
}
func (r *fakeReg) MetadataVersion(m []byte) (string, error) {
	return nugetRegistry{}.MetadataVersion(m)
}

type fakeGH struct {
	run       *WorkflowRun
	steps     []Step
	created   int
	existing  bool
	findCalls int
	getCalls  int
}

func (g *fakeGH) FindRun(context.Context, string, string, string, string) (*WorkflowRun, error) {
	g.findCalls++
	return g.run, nil
}
func (g *fakeGH) GetRun(context.Context, string, int64) (*WorkflowRun, error) {
	g.getCalls++
	r := *g.run
	r.Status, r.Conclusion = "completed", "success"
	return &r, nil
}
func (g *fakeGH) Steps(context.Context, string, int64) ([]Step, error) { return g.steps, nil }
func (g *fakeGH) ReleaseURL(context.Context, string, string) (string, bool, error) {
	return "https://github.com/acme/lib/releases/tag/v1.3.0", g.existing, nil
}
func (g *fakeGH) CreateRelease(context.Context, string, string, string, string) (string, error) {
	g.created++
	return "https://github.com/acme/lib/releases/tag/v1.3.0", nil
}

const nuspec = `<package><metadata><id>Acme.Lib</id><version>1.3.0</version>
<dependency id="Microsoft.EntityFrameworkCore" version="8.0.30" /></metadata></package>`

type rig struct {
	git      *fakeGit
	reg      *fakeReg
	gh       *fakeGH
	d        Deps
	ghCalled int
	files    map[string]string
}

func newRig() *rig {
	r := &rig{git: newGit(), reg: &fakeReg{published: map[string]bool{}, meta: nuspec},
		gh: &fakeGH{
			run:   &WorkflowRun{ID: 7, URL: "https://github.com/acme/lib/actions/runs/7", Status: "in_progress"},
			steps: []Step{{Name: "build / Checkout", Conclusion: "success"}, {Name: "build / Publish to NuGet", Conclusion: "success"}},
		},
		files: map[string]string{"src/Lib.csproj": csproj, "CHANGELOG.md": changelog}}
	now := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	r.d = Deps{
		Dir: "/repo", Git: r.git, Registry: r.reg,
		ReadFile: func(p string) ([]byte, error) {
			for k, v := range r.files {
				if strings.HasSuffix(p, "/"+k) {
					return []byte(v), nil
				}
			}
			return nil, fmt.Errorf("no file %s", p)
		},
		GitHub: func() (GitHub, error) { r.ghCalled++; return r.gh, nil },
		Now:    func() time.Time { return now },
		Sleep:  func(d time.Duration) { now = now.Add(d) },
	}
	return r
}

func spec() Spec {
	return Spec{Registry: NuGet, VersionFrom: "src/Lib.csproj", Assert: []string{`Microsoft.EntityFrameworkCore" version="8.0.30"`}}
}

func (r *rig) ship(t *testing.T, o Options) (*Record, error) {
	t.Helper()
	o.Name = "lib"
	return Run(context.Background(), r.d, spec(), o)
}

func reasonOf(rec *Record, name string) string {
	for _, c := range rec.Checks {
		if c.Name == name && !c.OK {
			return c.Reason
		}
	}
	return ""
}

func TestPreflightRefusesEachCauseByName(t *testing.T) {
	cases := []struct {
		name  string
		check string
		want  string
		set   func(*rig)
	}{
		{"dirty tree", CheckTree, "working tree is not clean", func(r *rig) { r.git.status = " M src/a.cs\n" }},
		{"other branch", CheckBranch, `on branch "feature"`, func(r *rig) { r.git.branch = "feature" }},
		{"not synced", CheckSynced, "differs from origin/main", func(r *rig) { r.git.remoteHead = "bbbbbbbbbbbbbbbb" }},
		{"tag elsewhere", CheckTag, "already exists locally at cccccccc, not at HEAD", func(r *rig) { r.git.localTag = "cccccccccccccccc" }},
		{"remote tag elsewhere", CheckTag, "already exists on origin", func(r *rig) { r.git.remoteTag = "dddddddddddddddd" }},
		{"published", CheckPublished, "Acme.Lib 1.3.0 is already published", func(r *rig) { r.reg.published["Acme.Lib"] = true }},
		{"no changelog section", CheckChangelog, "has no section for 1.3.0", func(r *rig) { r.files["CHANGELOG.md"] = "# Changelog\n\n## [1.2.0]\n- old\n" }},
		{"empty changelog section", CheckChangelog, "nothing under it", func(r *rig) { r.files["CHANGELOG.md"] = "## 1.3.0\n\n## 1.2.0\n- x\n" }},
		{"prerelease heading is another version", CheckChangelog, "has no section for 1.3.0", func(r *rig) { r.files["CHANGELOG.md"] = "## 1.3.0-beta\n- x\n" }},
		{"notes do not mention it", CheckNotes, "does not mention 1.3.0", func(r *rig) {
			r.files["src/Lib.csproj"] = strings.Replace(csproj, "1.3.0: moves", "moves", 1)
		}},
		{"version differs", CheckVersion, "says 1.3.0 but 9.9.9 was asked for", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig()
			if c.set != nil {
				c.set(r)
			}
			o := Options{}
			if c.name == "version differs" {
				o.Version = "9.9.9"
			}
			rec, err := r.ship(t, o)
			var re *RefusedError
			if !errors.As(err, &re) {
				t.Fatalf("want a refusal, got %v", err)
			}
			if got := reasonOf(rec, c.check); !strings.Contains(got, c.want) {
				t.Errorf("%s reason = %q, want it to contain %q", c.check, got, c.want)
			}
			if len(r.git.mutations()) > 0 || r.ghCalled > 0 {
				t.Errorf("a refused release mutated: %v", r.git.mutations())
			}
		})
	}
}

func TestPreflightRefusesAStackThatReleasedAnotherCommit(t *testing.T) {
	r := newRig()
	r.d.Deployed = func(_ context.Context, stack string) (string, bool, error) { return "eeeeeeeeeeee", true, nil }
	s := spec()
	s.RequireDeployed = "playground"
	rec, err := Run(context.Background(), r.d, s, Options{Name: "lib"})
	if err == nil || !strings.Contains(reasonOf(rec, CheckDeployed), "playground last released eeeeeeee, not this source") {
		t.Fatalf("err=%v checks=%+v", err, rec.Checks)
	}
	r.d.Deployed = func(context.Context, string) (string, bool, error) { return head, true, nil }
	if _, err := Run(context.Background(), r.d, s, Options{Name: "lib", DryRun: true}); err != nil {
		t.Fatalf("a stack that released this commit must pass: %v", err)
	}
}

func TestDryRunCreatesNoTagPushesNothingAndCallsNoMutatingAPI(t *testing.T) {
	r := newRig()
	rec, err := r.ship(t, Options{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if !rec.OK || !rec.DryRun || len(rec.Plan) == 0 {
		t.Fatalf("record: %+v", rec)
	}
	if m := r.git.mutations(); len(m) != 0 {
		t.Errorf("dry run mutated git: %v", m)
	}
	if r.ghCalled != 0 || r.gh.created != 0 || r.gh.findCalls != 0 {
		t.Errorf("dry run reached the GitHub API: built=%d created=%d find=%d", r.ghCalled, r.gh.created, r.gh.findCalls)
	}
	if len(rec.Steps) != 0 {
		t.Errorf("dry run reports steps it did not take: %+v", rec.Steps)
	}
	if rec.Diff == nil || rec.Diff.From != "v1.2.0" || len(rec.Diff.Files) != 1 {
		t.Errorf("the shipped diff is missing: %+v", rec.Diff)
	}
}

func TestShipTagsPushesWatchesVerifiesAndReleases(t *testing.T) {
	r := newRig()
	rec, err := r.ship(t, Options{})
	if err != nil {
		t.Fatalf("%v\n%+v", err, rec.Steps)
	}
	if got := r.git.mutations(); len(got) != 2 || got[0] != "tag v1.3.0" || got[1] != "push origin refs/tags/v1.3.0" {
		t.Errorf("mutations = %v", got)
	}
	if r.gh.created != 1 || rec.Release == "" {
		t.Errorf("release not created: %+v", rec)
	}
	if len(rec.Published) != 1 || rec.Publish == nil || rec.Publish.Status != "ok" {
		t.Errorf("record lacks what was asserted: %+v", rec)
	}
	var sawEF bool
	for _, a := range rec.Assertions {
		if a.OK && strings.Contains(a.Observed, "8.0.30") {
			sawEF = true
		}
	}
	if !sawEF {
		t.Errorf("the observed dependency line is not recorded: %+v", rec.Assertions)
	}
}

func TestAWorkflowThatSucceedsWhileSkippingItsPublishStepFails(t *testing.T) {
	r := newRig()
	r.gh.steps = []Step{{Name: "build / Checkout", Conclusion: "success"}, {Name: "build / Publish to NuGet", Conclusion: "skipped"}}
	rec, err := r.ship(t, Options{})
	if err == nil || !strings.Contains(err.Error(), "concluded \"skipped\"") || !strings.Contains(err.Error(), "nothing was published") {
		t.Fatalf("a skipped publish must fail, got %v", err)
	}
	if rec.OK || r.gh.created != 0 {
		t.Errorf("a release was created after a skipped publish: %+v", rec)
	}
	// A skipped job, with no steps, is the same failure.
	r2 := newRig()
	r2.gh.steps = []Step{{Name: "publish", Conclusion: "skipped"}}
	if _, err := r2.ship(t, Options{}); err == nil {
		t.Error("a skipped publish job must fail")
	}
}

func TestAnArtifactThatDoesNotMatchTheAssertionFails(t *testing.T) {
	r := newRig()
	r.reg.meta = strings.Replace(nuspec, "8.0.30", "8.0.0", 1)
	rec, err := r.ship(t, Options{})
	if err == nil || !strings.Contains(err.Error(), "published artifact does not match") {
		t.Fatalf("got %v", err)
	}
	if r.gh.created != 0 {
		t.Error("a release was created for an artifact that did not match")
	}
	var failed bool
	for _, a := range rec.Assertions {
		if !a.OK {
			failed = true
		}
	}
	if !failed {
		t.Errorf("the failed assertion is not recorded: %+v", rec.Assertions)
	}
	// The metadata's own version is asserted even with no configured lines.
	r3 := newRig()
	r3.reg.meta = strings.Replace(nuspec, "<version>1.3.0", "<version>1.2.0", 1)
	if _, err := Run(context.Background(), r3.d, Spec{Registry: NuGet, VersionFrom: "src/Lib.csproj"}, Options{Name: "lib"}); err == nil {
		t.Error("an artifact declaring another version must fail")
	}
}

func TestRerunningAfterAnInterruptedWatchDoesNotTagAgain(t *testing.T) {
	r := newRig()
	r.git.localTag, r.git.remoteTag = head, head
	r.reg.published["Acme.Lib"] = true // the publish finished while we were away
	rec, err := r.ship(t, Options{})
	if err != nil {
		t.Fatalf("%v\n%+v", err, rec.Steps)
	}
	if m := r.git.mutations(); len(m) != 0 {
		t.Errorf("a resume must not tag or push: %v", m)
	}
	if !rec.Resumed || rec.Steps[0].Status != "skipped" {
		t.Errorf("not reported as a resume: %+v", rec.Steps)
	}
	if r.gh.findCalls == 0 || r.gh.created != 1 {
		t.Errorf("a resume must pick up at watch and finish: find=%d created=%d", r.gh.findCalls, r.gh.created)
	}
}

func TestResumeWithOnlyALocalTagPushesButDoesNotRetag(t *testing.T) {
	r := newRig()
	r.git.localTag = head
	if _, err := r.ship(t, Options{}); err != nil {
		t.Fatal(err)
	}
	if m := r.git.mutations(); len(m) != 1 || !strings.HasPrefix(m[0], "push ") {
		t.Errorf("mutations = %v", m)
	}
}

func TestStackPublishStageStopsAfterThePush(t *testing.T) {
	r := newRig()
	rec, err := r.ship(t, Options{Stage: StageTag})
	if err != nil || !rec.OK {
		t.Fatalf("%v", err)
	}
	if r.gh.findCalls != 0 || r.ghCalled != 0 {
		t.Error("stack publish must not watch")
	}
	if len(r.git.mutations()) != 2 {
		t.Errorf("mutations = %v", r.git.mutations())
	}
}

func TestNoRunFoundFailsAfterTheTagAndSaysSo(t *testing.T) {
	r := newRig()
	r.gh.run = nil
	_, err := r.ship(t, Options{})
	if err == nil || !strings.Contains(err.Error(), "no run found for tag v1.3.0") {
		t.Fatalf("got %v", err)
	}
}

func TestDecliningTheConfirmationTagsNothing(t *testing.T) {
	r := newRig()
	r.d.Confirm = func(string) (bool, error) { return false, nil }
	_, err := r.ship(t, Options{})
	if !errors.Is(err, ErrDeclined) || len(r.git.mutations()) != 0 {
		t.Fatalf("err=%v mutations=%v", err, r.git.mutations())
	}
}

func TestAMissingTokenStopsBeforeTheTag(t *testing.T) {
	r := newRig()
	r.d.GitHub = func() (GitHub, error) { return nil, errors.New("secret reference did not resolve") }
	if _, err := r.ship(t, Options{}); err == nil || len(r.git.mutations()) != 0 {
		t.Fatalf("err=%v mutations=%v", err, r.git.mutations())
	}
}

func TestRegistryPollsUntilEveryPackageResolves(t *testing.T) {
	r := newRig()
	r.reg.failAfter = 3 // the gate asks once, then two empty polls
	if _, err := r.ship(t, Options{}); err != nil {
		t.Fatal(err)
	}
	if r.reg.queries < 4 {
		t.Errorf("did not poll: %d queries", r.reg.queries)
	}
	r2 := newRig()
	r2.d.Timings = Timings{Index: time.Minute, Poll: 10 * time.Second}
	r2.reg.published["x"] = false
	r2.reg.failAfter = 0
	// published stays false after the tag, so the wait must end with the package named.
	_, err := r2.ship(t, Options{})
	if err == nil || !strings.Contains(err.Error(), "still missing: Acme.Lib") {
		t.Fatalf("got %v", err)
	}
}

func TestSpecValidateRefusesEscapesAndBadShapes(t *testing.T) {
	bad := []Spec{
		{Registry: "pypi", VersionFrom: "a"},
		{Registry: NuGet, VersionFrom: "../x.csproj"},
		{Registry: NuGet, VersionFrom: "/etc/passwd"},
		{Registry: NuGet, VersionFrom: "a", Changelog: "../../CHANGELOG.md"},
		{Registry: NuGet, VersionFrom: "a", Tag: "release"},
		{Registry: NuGet, VersionFrom: "a", Tag: "v{version};rm"},
		{Registry: NuGet, VersionFrom: "a", Repo: "not a repo"},
		{Registry: NuGet, VersionFrom: "a", RegistryURL: "http://evil.example"},
	}
	for i, s := range bad {
		if s.Validate() == nil {
			t.Errorf("case %d accepted: %+v", i, s)
		}
	}
	if err := (Spec{Registry: Npm, VersionFrom: "package.json"}).Validate(); err != nil {
		t.Error(err)
	}
}

func TestChangelogSections(t *testing.T) {
	text := "# C\n## [Unreleased]\n## v1.2.4 - 2026-08-19\n- a\n### Fixed\n- b\n## 1.2.40\n- c\n"
	body, ok := changelogSection(text, "1.2.4")
	if !ok || !strings.Contains(body, "- b") || strings.Contains(body, "- c") {
		t.Errorf("body=%q ok=%v", body, ok)
	}
	if _, ok := changelogSection("## 1.2.40\n- c\n", "1.2.4"); ok {
		t.Error("1.2.40 matched 1.2.4")
	}
}

// ---- registries over a fake HTTP seam ----

type fakeDoer struct {
	routes map[string]string // url to body; missing is 404; "!" prefix body is a 500
	seen   []string
}

func (f *fakeDoer) Do(r *http.Request) (*http.Response, error) {
	f.seen = append(f.seen, r.URL.String())
	if _, ok := r.Context().Deadline(); !ok {
		return nil, errors.New("outbound call with no timeout")
	}
	body, ok := f.routes[r.URL.String()]
	code := 200
	switch {
	case !ok:
		code = 404
	case strings.HasPrefix(body, "!"):
		code = 500
	}
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body))}, nil
}

func TestNuGetReadsTheFlatContainer(t *testing.T) {
	d := &fakeDoer{routes: map[string]string{
		"https://api.nuget.org/v3-flatcontainer/acme.lib/index.json":            `{"versions":["1.2.0","1.3.0"]}`,
		"https://api.nuget.org/v3-flatcontainer/acme.lib/1.3.0/acme.lib.nuspec": nuspec,
	}}
	reg, _ := NewRegistry(Spec{Registry: NuGet}, d)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if ok, err := reg.Published(ctx, "Acme.Lib", "1.3.0"); err != nil || !ok {
		t.Errorf("published: %v %v", ok, err)
	}
	if ok, _ := reg.Published(ctx, "Acme.Lib", "1.4.0"); ok {
		t.Error("1.4.0 reported published")
	}
	if ok, err := reg.Published(ctx, "Other", "1.0.0"); ok || err != nil {
		t.Errorf("an unknown package is not published, not an error: %v %v", ok, err)
	}
	m, err := reg.Metadata(ctx, "Acme.Lib", "1.3.0")
	if v, _ := reg.MetadataVersion(m); err != nil || v != "1.3.0" {
		t.Errorf("metadata: %v %q", err, v)
	}
}

func TestNpmReadsTheVersionDocumentAndEncodesScopes(t *testing.T) {
	d := &fakeDoer{routes: map[string]string{
		"https://registry.npmjs.org/@acme%2Fui/2.0.0": `{"name":"@acme/ui","version":"2.0.0","dependencies":{"x":"1.0.1"}}`,
		"https://registry.npmjs.org/broken/1.0.0":     "!boom",
	}}
	reg, _ := NewRegistry(Spec{Registry: Npm}, d)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if ok, err := reg.Published(ctx, "@acme/ui", "2.0.0"); err != nil || !ok {
		t.Errorf("published: %v %v (%v)", ok, err, d.seen)
	}
	if ok, err := reg.Published(ctx, "@acme/ui", "2.0.1"); ok || err != nil {
		t.Errorf("2.0.1: %v %v", ok, err)
	}
	if _, err := reg.Published(ctx, "broken", "1.0.0"); err == nil {
		t.Error("a registry error must not read as not published")
	}
	if v, err := reg.Version([]byte(`{"name":"a","version":"3.1.0"}`)); err != nil || v != "3.1.0" {
		t.Errorf("version: %v %q", err, v)
	}
}

func TestGateRefusesWhenTheRegistryCannotBeAsked(t *testing.T) {
	r := newRig()
	r.d.Registry = errReg{r.reg}
	rec, err := r.ship(t, Options{})
	if err == nil || !strings.Contains(reasonOf(rec, CheckPublished), "cannot ask the registry") {
		t.Fatalf("err=%v", err)
	}
}

type errReg struct{ *fakeReg }

func (errReg) Published(context.Context, string, string) (bool, error) {
	return false, errors.New("HTTP 503")
}

func TestGitHubClientSendsTheTokenOnlyAsAHeader(t *testing.T) {
	var auth string
	d := docFunc(func(r *http.Request) (*http.Response, error) {
		auth = r.Header.Get("Authorization")
		return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader("tok-secret-value"))}, nil
	})
	gh := APIGitHub{Token: "tok-secret-value", H: d}
	_, err := gh.GetRun(context.Background(), "a/b", 1)
	if auth != "Bearer tok-secret-value" {
		t.Errorf("header = %q", auth)
	}
	if err == nil || strings.Contains(err.Error(), "tok-secret-value") {
		t.Errorf("the token or the body reached an error: %v", err)
	}
}

type docFunc func(*http.Request) (*http.Response, error)

func (f docFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }
