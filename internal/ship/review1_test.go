// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package ship

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func withVersion(r *rig, v string) {
	r.files["src/Lib.csproj"] = strings.Replace(csproj, "<Version>1.3.0</Version>", "<Version>"+v+"</Version>", 1)
}

// ---- 1. the publish step is matched by its own exact name ----

func TestPublishStepMatchIsExactAndEveryMatchMustSucceed(t *testing.T) {
	cases := []struct {
		name  string
		step  string
		job   string
		steps []Step
		want  string // empty: must pass
	}{
		{"a job named publish does not make its checkout the publish step", "publish", "",
			[]Step{{Job: "publish", Name: "Set up job", Conclusion: "success"}, {Job: "publish", Name: "Checkout", Conclusion: "success"}},
			"no step in run 7 is named"},
		{"a skipped push step in a job named publish", "Push", "",
			[]Step{{Job: "publish", Name: "Checkout", Conclusion: "success"}, {Job: "publish", Name: "Push", Conclusion: "skipped"}},
			`concluded "skipped"`},
		{"a decoy Verify publish step beside a skipped real one", "Publish", "",
			[]Step{{Job: "build", Name: "Verify publish", Conclusion: "success"}, {Job: "build", Name: "Skip publish if exists", Conclusion: "success"}, {Job: "build", Name: "Publish", Conclusion: "skipped"}},
			`concluded "skipped"`},
		{"a matrix with one leg skipped", "Publish", "",
			[]Step{{Job: "pack (linux)", Name: "Publish", Conclusion: "success"}, {Job: "pack (windows)", Name: "Publish", Conclusion: "skipped"}},
			`concluded "skipped"`},
		{"a matrix with every leg green", "publish", "",
			[]Step{{Job: "pack (linux)", Name: "Publish", Conclusion: "success"}, {Job: "pack (windows)", Name: "publish", Conclusion: "success"}}, ""},
		{"the job selector picks the job", "Publish", "release",
			[]Step{{Job: "build", Name: "Publish", Conclusion: "skipped"}, {Job: "release", Name: "Publish", Conclusion: "success"}}, ""},
		{"the job selector does not pass for another job", "Publish", "release",
			[]Step{{Job: "build", Name: "Publish", Conclusion: "success"}}, "no step in run 7 is named"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig()
			r.gh.steps = c.steps
			s := spec()
			s.PublishStep, s.PublishJob = c.step, c.job
			rec, err := Run(context.Background(), r.d, s, Options{Name: "lib"})
			if c.want == "" {
				if err != nil {
					t.Fatalf("%v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want %q, got %v", c.want, err)
			}
			if r.gh.created != 0 || rec.OK {
				t.Error("a release was created after a publish step that did not run")
			}
		})
	}
}

// ---- 2. a 404 is not success ----

func ghServer(t *testing.T, h http.HandlerFunc) APIGitHub {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return APIGitHub{Base: srv.URL, Token: "t", H: srv.Client()}
}

func TestCreateReleaseRefusesA404AndAnEmptyURL(t *testing.T) {
	status, body := 404, ""
	gh := ghServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
	if u, err := gh.CreateRelease(context.Background(), "o/r", "v1", "v1", "n"); err == nil || u != "" {
		t.Errorf("a 404 on the POST must be an error, got %q %v", u, err)
	}
	status, body = 201, `{}`
	if _, err := gh.CreateRelease(context.Background(), "o/r", "v1", "v1", "n"); err == nil || !strings.Contains(err.Error(), "without a release URL") {
		t.Errorf("an empty html_url must be an error, got %v", err)
	}
	status, body = 201, `{"html_url":"https://github.com/o/r/releases/tag/v1"}`
	if u, err := gh.CreateRelease(context.Background(), "o/r", "v1", "v1", "n"); err != nil || u == "" {
		t.Errorf("got %q %v", u, err)
	}
	// The GET that asks whether a release exists is where 404 means "no".
	status, body = 404, ""
	if u, ok, err := gh.ReleaseURL(context.Background(), "o/r", "v1"); err != nil || ok || u != "" {
		t.Errorf("ReleaseURL 404 = %q %v %v", u, ok, err)
	}
	if _, err := gh.GetRun(context.Background(), "o/r", 9); err == nil {
		t.Error("a missing run must be an error")
	}
}

// ---- 3. the version is validated before it reaches git, a URL or a file name ----

func TestAHostileVersionIsRefusedBeforeAnyGitCommand(t *testing.T) {
	for _, v := range []string{"--delete", "../../x", "a/b", "1.2.3;rm", "-1.0.0", "1..2"} {
		r := newRig()
		withVersion(r, v)
		rec, err := r.ship(t, Options{})
		var re *RefusedError
		if !errors.As(err, &re) || !strings.Contains(reasonOf(rec, CheckVersion), "is not a version") {
			t.Errorf("%q: err=%v reason=%q", v, err, reasonOf(rec, CheckVersion))
		}
		if len(r.git.mutations()) != 0 {
			t.Errorf("%q: mutated: %v", v, r.git.mutations())
		}
	}
	for _, v := range []string{"1.3.0", "1.3.0-beta.1", "2.0.0+build.5", "1.0.0.1"} {
		if err := validVersion(v); err != nil {
			t.Errorf("%q refused: %v", v, err)
		}
	}
}

func TestRecordPathStaysUnderHome(t *testing.T) {
	for _, c := range [][2]string{{"lib", "../../x"}, {"../lib", "1.0.0"}, {"lib", "a/b"}, {"", "1.0.0"}, {"lib", ""}, {"a/../..", "1.0.0"}} {
		if p, err := RecordPath("/h", c[0], c[1]); err == nil {
			t.Errorf("%v accepted: %s", c, p)
		}
	}
	if p, err := RecordPath("/h", "lib", "1.3.0"); err != nil || p != "/h/ship/lib/1.3.0.json" {
		t.Errorf("got %q %v", p, err)
	}
}

// ---- 4. release notes name the exact version ----

func TestMentionsVersionUsesABoundary(t *testing.T) {
	yes := []string{"1.3.0: moves EF", "v1.3.0 adds x", "Fixed in 1.3.0.", "see (1.3.0)", "1.3.0"}
	no := []string{"11.3.0 only", "1.3.0-beta", "1.3.01", "21.3.0", "1.3.0.1", "1.3.0+build", "x1.3.0"}
	for _, s := range yes {
		if !mentionsVersion(s, "1.3.0") {
			t.Errorf("%q should mention 1.3.0", s)
		}
	}
	for _, s := range no {
		if mentionsVersion(s, "1.3.0") {
			t.Errorf("%q should not mention 1.3.0", s)
		}
	}
}

func TestReleaseNotesForAnotherVersionAreRefused(t *testing.T) {
	r := newRig()
	r.files["src/Lib.csproj"] = strings.Replace(csproj, "1.3.0: moves", "11.3.0 and 1.3.0-beta: moves", 1)
	rec, err := r.ship(t, Options{})
	if err == nil || !strings.Contains(reasonOf(rec, CheckNotes), "does not mention 1.3.0") {
		t.Fatalf("err=%v", err)
	}
}

// ---- 5. a deployed record with no real commit ----

func TestAnEmptyOrShortDeployedCommitIsRefused(t *testing.T) {
	for _, c := range []string{"", "abc", "abcdef"} {
		r := newRig()
		r.d.Deployed = func(context.Context, string) (string, bool, error) { return c, true, nil }
		s := spec()
		s.RequireDeployed = "prod"
		rec, err := Run(context.Background(), r.d, s, Options{Name: "lib"})
		if err == nil || !strings.Contains(reasonOf(rec, CheckDeployed), "no usable commit") {
			t.Errorf("%q: err=%v reason=%q", c, err, reasonOf(rec, CheckDeployed))
		}
	}
}

// ---- 6. URL credentials never reach output ----

func TestRemoteCredentialsAreRedacted(t *testing.T) {
	r := newRig()
	r.git.remoteURL = "https://user:tok-secret@gitlab.example/o/r.git"
	rec, err := r.ship(t, Options{DryRun: true})
	if err == nil {
		t.Fatal("a non-GitHub remote must refuse")
	}
	b, _ := json.Marshal(rec)
	if strings.Contains(string(b), "tok-secret") || strings.Contains(err.Error(), "tok-secret") {
		t.Errorf("the credential leaked: %s / %v", b, err)
	}
	if !strings.Contains(reasonOf(rec, CheckRepo), "gitlab.example/o/r.git") {
		t.Errorf("the redacted remote should still be named: %q", reasonOf(rec, CheckRepo))
	}

	r2 := newRig()
	r2.git.pushErr = "fatal: unable to access 'https://user:tok-secret@github.com/o/r/': 403"
	rec2, err2 := r2.ship(t, Options{})
	if err2 == nil {
		t.Fatal("a failed push must fail")
	}
	b2, _ := json.Marshal(rec2)
	if strings.Contains(string(b2), "tok-secret") || strings.Contains(err2.Error(), "tok-secret") {
		t.Errorf("the credential leaked through the push error: %s / %v", b2, err2)
	}
	if got := redact("x https://a:b@h/p and ssh://git@h2/q"); strings.Contains(got, "a:b") || strings.Contains(got, "git@") {
		t.Errorf("redact: %q", got)
	}
}

// ---- 7. the GitHub remote pattern is anchored on the host ----

func TestGitHubRemotePatternIsAnchoredOnTheHost(t *testing.T) {
	yes := map[string]string{
		"git@github.com:o/r.git":              "o/r",
		"https://github.com/o/r":              "o/r",
		"https://user:tok@github.com/o/r.git": "o/r",
		"ssh://git@github.com:22/o/r.git":     "o/r",
		"git@github.com:org-1/my.repo":        "org-1/my.repo",
	}
	for u, want := range yes {
		m := githubRemoteRe.FindStringSubmatch(u)
		if m == nil || m[1]+"/"+m[2] != want {
			t.Errorf("%s: %v", u, m)
		}
	}
	for _, u := range []string{"https://evilgithub.com/o/r", "git@evilgithub.com:o/r", "https://github.com.evil.example/o/r", "https://evil.example/github.com/o/r", "file:///x/github.com/o/r"} {
		if githubRemoteRe.MatchString(u) {
			t.Errorf("%s matched", u)
		}
	}
}

// ---- 8. cancellation ----

func TestPollReturnsAtOnceOnACancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	d := Deps{Now: time.Now, Sleep: func(time.Duration) { t.Error("slept after cancel") }, Timings: Timings{Poll: time.Hour}.withDefaults()}
	d.Timings.Poll = time.Hour
	start := time.Now()
	err := (&Record{}).poll(ctx, d, time.Hour, "x", func() (bool, error) { calls++; return false, nil })
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "run the same command again") || calls != 0 || time.Since(start) > time.Second {
		t.Errorf("err=%v calls=%d", err, calls)
	}
	// With no Sleep seam, the real wait also ends on cancel instead of sitting out the interval.
	ctx2, cancel2 := context.WithCancel(context.Background())
	d2 := Deps{Now: time.Now, Timings: Timings{Poll: time.Hour}}
	go func() { time.Sleep(20 * time.Millisecond); cancel2() }()
	start = time.Now()
	err = (&Record{}).poll(ctx2, d2, 2*time.Hour, "x", func() (bool, error) { return false, nil })
	if !errors.Is(err, context.Canceled) || time.Since(start) > 5*time.Second {
		t.Errorf("err=%v after %v", err, time.Since(start))
	}
}

func TestInterruptedAfterTheTagStopsWatching(t *testing.T) {
	r := newRig()
	ctx, cancel := context.WithCancel(context.Background())
	r.d.GitHub = func() (GitHub, error) { return r.gh, nil }
	r.d.Confirm = func(string) (bool, error) { cancel(); return true, nil }
	_, err := Run(ctx, r.d, spec(), Options{Name: "lib"})
	if !errors.Is(err, context.Canceled) || r.gh.findCalls != 0 {
		t.Errorf("err=%v find=%d", err, r.gh.findCalls)
	}
}

// ---- 9. finding the run, run conclusions, paging jobs ----

func TestFindRunMatchesTagCommitEventAndWorkflow(t *testing.T) {
	gh := ghServer(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"workflow_runs":[
		 {"id":1,"name":"CI","path":".github/workflows/ci.yml","head_branch":"v1.3.0","head_sha":"deadbeef","event":"push"},
		 {"id":2,"name":"CI","path":".github/workflows/ci.yml","head_branch":"v1.3.0","head_sha":"cafe","event":"workflow_dispatch"},
		 {"id":3,"name":"CI","path":".github/workflows/ci.yml","head_branch":"main","head_sha":"cafe","event":"push"},
		 {"id":4,"name":"Docs","path":".github/workflows/docs.yml","head_branch":"v1.3.0","head_sha":"cafe","event":"push"},
		 {"id":5,"name":"Release","path":".github/workflows/release.yml","head_branch":"v1.3.0","head_sha":"cafe","event":"push"}]}`)
	})
	got, err := gh.FindRun(context.Background(), "o/r", "v1.3.0", "cafe", "release")
	if err != nil || got == nil || got.ID != 5 {
		t.Fatalf("got %+v %v", got, err)
	}
	if got, _ := gh.FindRun(context.Background(), "o/r", "v1.3.0", "cafe", ""); got == nil || got.ID != 4 {
		t.Errorf("with no workflow set the first run of this tag and commit wins, got %+v", got)
	}
	if got, _ := gh.FindRun(context.Background(), "o/r", "v1.3.0", "feed", ""); got != nil {
		t.Errorf("a run of another commit was picked: %+v", got)
	}
	if got, _ := gh.FindRun(context.Background(), "o/r", "v1.3.0", "cafe", "nothing"); got != nil {
		t.Errorf("an unknown workflow matched: %+v", got)
	}
}

func TestARunThatDidNotSucceedRefuses(t *testing.T) {
	for _, c := range []string{"failure", "cancelled", "timed_out"} {
		r := newRig()
		r.gh.conclusion = c
		_, err := r.ship(t, Options{})
		if err == nil || !strings.Contains(err.Error(), "concluded "+c) {
			t.Errorf("%s: %v", c, err)
		}
		if r.gh.created != 0 {
			t.Errorf("%s: a release was created", c)
		}
	}
}

func TestStepsPagesPastOneHundredJobs(t *testing.T) {
	gh := ghServer(t, func(w http.ResponseWriter, r *http.Request) {
		n := 100
		if r.URL.Query().Get("page") == "2" {
			n = 30
		}
		var jobs []string
		for i := 0; i < n; i++ {
			jobs = append(jobs, fmt.Sprintf(`{"name":"j%s%d","status":"completed","conclusion":"skipped","steps":[]}`, r.URL.Query().Get("page"), i))
		}
		fmt.Fprintf(w, `{"jobs":[%s]}`, strings.Join(jobs, ","))
	})
	steps, err := gh.Steps(context.Background(), "o/r", 1)
	if err != nil || len(steps) != 130 {
		t.Fatalf("got %d steps, %v", len(steps), err)
	}
	if steps[0].Name != "" || steps[0].Job != "j10" {
		t.Errorf("a job with no steps should appear by job name: %+v", steps[0])
	}
}

// ---- 10. NuGet versions compare the way NuGet does ----

func TestNuGetVersionsAreNormalised(t *testing.T) {
	same := [][2]string{{"1.3.0", "1.3.0.0"}, {"1.3.0", "1.3.0+build.5"}, {"1.3", "1.3.0"}, {"1.03.0", "1.3.0"}, {"1.3.0-Beta", "1.3.0-beta"}}
	for _, p := range same {
		if !(nugetRegistry{}).SameVersion(p[0], p[1]) {
			t.Errorf("%s and %s should match", p[0], p[1])
		}
	}
	if (nugetRegistry{}).SameVersion("1.3.0", "1.3.0.1") || (nugetRegistry{}).SameVersion("1.3.0", "1.3.0-beta") {
		t.Error("different versions matched")
	}
	d := &fakeDoer{routes: map[string]string{
		"https://api.nuget.org/v3-flatcontainer/acme.lib/index.json":            `{"versions":["1.2.0","1.3.0"]}`,
		"https://api.nuget.org/v3-flatcontainer/acme.lib/1.3.0/acme.lib.nuspec": nuspec,
	}}
	reg, _ := NewRegistry(Spec{Registry: NuGet}, d)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, v := range []string{"1.3.0.0", "1.3.0+build"} {
		if ok, err := reg.Published(ctx, "Acme.Lib", v); err != nil || !ok {
			t.Errorf("%s: %v %v", v, ok, err)
		}
	}
	if _, err := reg.Metadata(ctx, "Acme.Lib", "1.3.0.0"); err != nil {
		t.Errorf("the nuspec URL must use the normalised version: %v", err)
	}
}

func TestAnArtifactDeclaringTheSameVersionInAnotherSpellingPasses(t *testing.T) {
	r := newRig()
	r.reg.meta = strings.Replace(nuspec, "<version>1.3.0", "<version>1.3.0.0", 1)
	if _, err := r.ship(t, Options{}); err != nil {
		t.Fatalf("%v", err)
	}
}

// ---- 11. a package name that is not one ----

func TestPackageNameFallbackIsRefused(t *testing.T) {
	n := nugetRegistry{}
	if got := n.PackageName("Directory.Build.props", []byte("<Version>1.0.0</Version>")); got != "" {
		t.Errorf("props file named a package: %q", got)
	}
	if got := n.PackageName("src/Lib.csproj", []byte("<PackageId>$(MSBuildProjectName)</PackageId>")); got != "" {
		t.Errorf("property reference named a package: %q", got)
	}
	if got := n.PackageName("src/Acme.Lib.csproj", []byte("<Version>1.0.0</Version>")); got != "Acme.Lib" {
		t.Errorf("a project keeps its own name, got %q", got)
	}

	r := newRig()
	r.files["Directory.Build.props"] = "<Project><PropertyGroup><Version>1.3.0</Version></PropertyGroup></Project>"
	s := Spec{Registry: NuGet, VersionFrom: "Directory.Build.props"}
	rec, err := Run(context.Background(), r.d, s, Options{Name: "lib"})
	if err == nil || !strings.Contains(reasonOf(rec, CheckPublished), "set publish.packages") {
		t.Fatalf("err=%v", err)
	}
	s.Packages = []string{"Acme.Lib"}
	r.reg.published["Acme.Lib"] = false
	if _, err := Run(context.Background(), r.d, s, Options{Name: "lib", DryRun: true}); err != nil {
		t.Errorf("publish.packages must satisfy the gate: %v", err)
	}
}

// ---- 12. spec validation ----

func TestSpecValidateEmptyAssertAndRegistryHost(t *testing.T) {
	base := Spec{Registry: NuGet, VersionFrom: "a.csproj"}
	bad := func(mut func(*Spec)) {
		s := base
		mut(&s)
		if s.Validate() == nil {
			t.Errorf("accepted: %+v", s)
		}
	}
	bad(func(s *Spec) { s.Assert = []string{"ok", "  "} })
	bad(func(s *Spec) { s.Assert = []string{""} })
	bad(func(s *Spec) { s.RegistryURL = "http://127.0.0.1.evil.com/v3" })
	bad(func(s *Spec) { s.RegistryURL = "http://127.0.0.1@evil.com/v3" })
	bad(func(s *Spec) { s.RegistryURL = "https://user:pw@feed.example/v3" })
	bad(func(s *Spec) { s.RegistryURL = "https://" })
	bad(func(s *Spec) { s.RegistryURL = "ftp://feed.example" })
	bad(func(s *Spec) { s.RegistryURL = "http://feed.example" })
	for _, u := range []string{"https://feed.example/v3-flatcontainer", "http://127.0.0.1:8080/v3"} {
		s := base
		s.RegistryURL = u
		if err := s.Validate(); err != nil {
			t.Errorf("%s refused: %v", u, err)
		}
	}
}
