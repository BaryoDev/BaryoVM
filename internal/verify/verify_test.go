// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package verify

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// served builds the output the fetch command prints for a response: the body, then the status.
func served(code int, body string) string {
	return body + "\n" + statusMarker + itoa(code)
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func planFor(t *testing.T, spec *Spec, ctx Context) Plan {
	t.Helper()
	if ctx.DefaultAttempts == 0 {
		ctx.DefaultAttempts = 5
		ctx.DefaultDelay = 3 * time.Second
	}
	p, err := spec.Plan(ctx)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	return p
}

// judge runs one check's verdict on a canned response.
func judge(t *testing.T, c Check, out string, err error) (bool, string) {
	t.Helper()
	spec := &Spec{BaseURL: "http://127.0.0.1:8091", Checks: []Check{c}}
	p := planFor(t, spec, Context{})
	return p.Steps[0].Judge(out, err)
}

const umbracoStock = "<html><h1>Welcome to your Umbraco installation</h1></html>"
const realSite = `<link rel="manifest" href="/m.json"><script src="baryodev-pwa.js"></script>` +
	"................................................................................................" +
	"................................................................................................"

// #25 and #89: the placeholder page answers 200, and a body marker is what tells it from the site.
func TestAPlaceholderPageAnswering200FailsWhenAMarkerIsSet(t *testing.T) {
	c := Check{Path: "/", Contains: []string{"baryodev-pwa.js"}}

	ok, why := judge(t, c, served(200, umbracoStock), nil)
	if ok {
		t.Fatal("a 200 with the wrong document passed a check that names a marker")
	}
	if !strings.Contains(why, "contains") || !strings.Contains(why, "baryodev-pwa.js") {
		t.Errorf("the failure should name the assertion and the marker: %q", why)
	}

	if ok, why := judge(t, c, served(200, realSite), nil); !ok {
		t.Errorf("the real site failed: %s", why)
	}
}

// Without a marker the same placeholder passes: that is what "2xx alone" is, and why a stack that
// only has a healthUrl cannot catch it. The spec is how a stack opts into more.
func TestWithoutAMarkerThePlaceholderStillPasses(t *testing.T) {
	if ok, _ := judge(t, Check{Path: "/"}, served(200, umbracoStock), nil); !ok {
		t.Error("a status-only check should accept any 2xx")
	}
}

func TestAbsentFailsNamingTheAssertion(t *testing.T) {
	c := Check{Path: "/", Contains: []string{"baryodev-pwa.js"}, Absent: []string{"Welcome to your Umbraco installation"}}

	if ok, why := judge(t, c, served(200, realSite), nil); !ok {
		t.Fatalf("contains matches and absent does not: want a pass, got %s", why)
	}
	// Both the marker missing and the stock text present are failures; absent is what names the
	// stock page when the marker happens to be there too.
	both := realSite + umbracoStock
	ok, why := judge(t, c, served(200, both), nil)
	if ok || !strings.Contains(why, "absent") {
		t.Errorf("want a failure naming absent, got ok=%v %q", ok, why)
	}
}

// A 502, an empty body and a refused connection all satisfy "must not contain". Absent must never
// report on them.
func TestAbsentIsNotEvaluatedUntilStatusAndSizePass(t *testing.T) {
	c := Check{Path: "/", Status: 200, MinBytes: 20, Absent: []string{"No Published Content"}}

	ok, why := judge(t, c, served(502, "Bad gateway"), nil)
	if ok || !strings.Contains(why, "status 502") || strings.Contains(why, "absent") {
		t.Errorf("a 502 should fail on status and say nothing of absent: ok=%v %q", ok, why)
	}

	ok, why = judge(t, c, served(200, "twelve bytes"), nil)
	if ok || !strings.Contains(why, "minBytes") || strings.Contains(why, "absent") {
		t.Errorf("a 12-byte body should fail on minBytes: ok=%v %q", ok, why)
	}

	ok, why = judge(t, c, "", errors.New("exit status 7"))
	if ok || strings.Contains(why, "absent") {
		t.Errorf("a refused connection should fail as no answer: ok=%v %q", ok, why)
	}

	// No minBytes at all: an empty body still cannot satisfy absent.
	ok, why = judge(t, Check{Path: "/", Absent: []string{"x"}}, served(200, ""), nil)
	if ok || !strings.Contains(why, "empty") {
		t.Errorf("an empty body must not pass an absent check: ok=%v %q", ok, why)
	}
}

func TestStatusIsExactWhenSetAnd2xxWhenNot(t *testing.T) {
	if ok, _ := judge(t, Check{Path: "/", Status: 204}, served(200, "x"), nil); ok {
		t.Error("status 204 wanted, 200 passed")
	}
	if ok, _ := judge(t, Check{Path: "/", Status: 404}, served(404, "gone"), nil); !ok {
		t.Error("status 404 wanted and served should pass")
	}
	if ok, why := judge(t, Check{Path: "/"}, served(301, ""), nil); ok || !strings.Contains(why, "301") {
		t.Errorf("a redirect is not a 2xx: ok=%v %q", ok, why)
	}
	if ok, _ := judge(t, Check{Path: "/"}, "no status here", nil); ok {
		t.Error("output without a status must fail")
	}
}

func TestJSONMatchAssertsAVersion(t *testing.T) {
	c := Check{Path: "/build-info", JSONMatch: map[string]string{"version": "0.3.0"}}

	if ok, why := judge(t, c, served(200, `{"version":"0.3.0"}`), nil); !ok {
		t.Errorf("matching version failed: %s", why)
	}
	ok, why := judge(t, c, served(200, `{"version":"0.2.0"}`), nil)
	if ok || !strings.Contains(why, "jsonMatch") || !strings.Contains(why, "0.3.0") {
		t.Errorf("a stale version should fail naming it: ok=%v %q", ok, why)
	}
	if ok, _ := judge(t, c, served(200, `<html>not json</html>`), nil); ok {
		t.Error("a non-JSON body passed a jsonMatch")
	}
	nested := Check{Path: "/", JSONMatch: map[string]string{"build.number": "7"}}
	if ok, why := judge(t, nested, served(200, `{"build":{"number":7}}`), nil); !ok {
		t.Errorf("dotted path failed: %s", why)
	}
	if ok, _ := judge(t, nested, served(200, `{"build":{}}`), nil); ok {
		t.Error("a missing key passed")
	}
}

// A stack with only a healthUrl is a one-check spec and verifies as it did before: release by
// `curl --fail` exit code, update by a 2xx status code.
func TestAHealthURLAloneIsAOneCheckSpecThatVerifiesAsBefore(t *testing.T) {
	if got := FromHealthURL("http://127.0.0.1:8080/health"); len(got.Checks) != 1 {
		t.Fatalf("want one check, got %d", len(got.Checks))
	}
	const url = "http://127.0.0.1:8080/health?a=1&b=2"

	rel := planFor(t, nil, Context{Mode: ForRelease, HealthURL: url})
	if rel.Source != SourceHealthURL || len(rel.Steps) != 1 {
		t.Fatalf("release plan = %+v", rel)
	}
	if got, want := rel.Steps[0].Cmd, "curl --fail --silent --show-error --max-time 10 -o /dev/null 'http://127.0.0.1:8080/health?a=1&b=2'"; got != want {
		t.Errorf("release probe changed:\n got  %s\n want %s", got, want)
	}
	if ok, _ := rel.Steps[0].Judge("", nil); !ok {
		t.Error("exit zero should pass")
	}
	if ok, _ := rel.Steps[0].Judge("", errors.New("exit status 22")); ok {
		t.Error("curl --fail exiting 22 should fail")
	}
	if rel.Budget.Attempts != 5 || rel.Budget.Delay != 3*time.Second {
		t.Errorf("release defaults changed: %+v", rel.Budget)
	}

	upd := planFor(t, nil, Context{Mode: ForUpdate, HealthURL: url, DefaultAttempts: 20, DefaultDelay: 3 * time.Second})
	if got, want := upd.Steps[0].Cmd, "curl -s -o /dev/null -w '%{http_code}' --max-time 10 --max-redirs 0 'http://127.0.0.1:8080/health?a=1&b=2'"; got != want {
		t.Errorf("update probe changed:\n got  %s\n want %s", got, want)
	}
	for out, want := range map[string]bool{"200": true, "204": true, "200\n": true, "404": false, "500": false, "": false, "000": false} {
		if got, _ := upd.Steps[0].Judge(out, nil); got != want {
			t.Errorf("update judge(%q) = %v, want %v", out, got, want)
		}
	}
	if ok, _ := upd.Steps[0].Judge("", errors.New("exit status 7")); ok {
		t.Error("a refused connection must not be healthy")
	}
	if upd.Budget.Attempts != 20 {
		t.Errorf("update default attempts = %d", upd.Budget.Attempts)
	}
}

func TestNoSpecAndNoHealthURLIsAnEmptyPlan(t *testing.T) {
	p := planFor(t, nil, Context{})
	if !p.Empty() || p.Source != SourceNone {
		t.Errorf("plan = %+v", p)
	}
	p = planFor(t, &Spec{Attempts: 9}, Context{})
	if !p.Empty() {
		t.Error("a spec with only attempts has nothing to run")
	}
	// ...but with a healthUrl it tunes the patience of that single check.
	p = planFor(t, &Spec{Attempts: 9, DelaySeconds: 2}, Context{HealthURL: "http://x/h"})
	if p.Source != SourceHealthURL || p.Budget.Attempts != 9 || p.Budget.Delay != 2*time.Second {
		t.Errorf("plan = %+v", p)
	}
}

// A command check is a step like any other, run in the stack's directory and as root when the
// stack is.
func TestACommandCheckRunsInTheStackDirAndPassesOnExitZero(t *testing.T) {
	spec := &Spec{Checks: []Check{{Command: "sh scripts/check.sh"}}}

	p := planFor(t, spec, Context{Dir: "/opt/shop"})
	if got, want := p.Steps[0].Cmd, "timeout 300 sh -c 'cd '\\''/opt/shop'\\'' && sh scripts/check.sh'"; got != want {
		t.Errorf("cmd = %q, want %q", got, want)
	}
	if ok, _ := p.Steps[0].Judge("", nil); !ok {
		t.Error("exit zero should pass")
	}
	if ok, why := p.Steps[0].Judge("boom\n", errors.New("exit status 1")); ok || !strings.Contains(why, "boom") {
		t.Errorf("a failing command: ok=%v %q", ok, why)
	}

	root := planFor(t, spec, Context{Dir: "/opt/shop", Sudo: true})
	if !strings.HasPrefix(root.Steps[0].Cmd, `sudo -n "${SHELL:-/bin/sh}" -c `) {
		t.Errorf("a sudo stack's command check does not use sudo: %q", root.Steps[0].Cmd)
	}
}

// URL checks fetch only the stack's own configured origin.
func TestAPathCannotLeaveTheStacksOrigin(t *testing.T) {
	for _, bad := range []string{
		"//evil.example/x", "http://evil.example/", "https://evil.example/", "evil.example/x",
		"/ok\\@evil", "/a b", "/a\nb", "file:///etc/passwd",
	} {
		spec := &Spec{BaseURL: "http://127.0.0.1:8091", Checks: []Check{{Path: bad}}}
		if _, err := spec.Plan(Context{DefaultAttempts: 1}); err == nil {
			t.Errorf("path %q was accepted", bad)
		}
	}
}

func TestEveryFetchTargetsTheStacksOwnOriginAndNeverFollowsARedirect(t *testing.T) {
	spec := &Spec{
		BaseURL: "http://127.0.0.1:8091",
		Checks: []Check{
			{Path: "/", Contains: []string{"x"}},
			{Path: "/build-info?v=1", JSONMatch: map[string]string{"version": "1"}},
			{Path: "/health"},
		},
	}
	for _, mode := range []Mode{ForRelease, ForUpdate} {
		p := planFor(t, spec, Context{Mode: mode, HealthURL: "https://elsewhere.example/health"})
		for _, s := range p.Steps {
			if !strings.Contains(s.Cmd, "'http://127.0.0.1:8091/") {
				t.Errorf("%s does not fetch the stack's origin", s.Cmd)
			}
			if strings.Contains(s.Cmd, "elsewhere.example") {
				t.Errorf("%s reached for the healthUrl's host instead of baseUrl", s.Cmd)
			}
			for _, flag := range []string{" -L", "--location", "--location-trusted"} {
				if strings.Contains(s.Cmd, flag) {
					t.Errorf("%s follows redirects (%s)", s.Cmd, flag)
				}
			}
		}
		if !strings.Contains(p.Steps[0].Cmd, "--max-redirs 0") && mode == ForRelease {
			t.Errorf("assertion fetch should refuse redirects explicitly: %s", p.Steps[0].Cmd)
		}
	}
	// With no baseUrl the origin is the healthUrl's, so the spec still cannot name another host.
	p := planFor(t, &Spec{Checks: []Check{{Path: "/x", Contains: []string{"y"}}}}, Context{HealthURL: "http://127.0.0.1:5005/health"})
	if !strings.Contains(p.Steps[0].Cmd, "'http://127.0.0.1:5005/x'") {
		t.Errorf("origin of the healthUrl not used: %s", p.Steps[0].Cmd)
	}
}

func TestThereIsNoFieldThatPointsAFetchElsewhere(t *testing.T) {
	// A recipe or project file that writes "url" gets an error, not a fetch.
	var s Spec
	if err := json.Unmarshal([]byte(`{"checks":[{"url":"http://evil.example/","contains":["x"]}]}`), &s); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Plan(Context{HealthURL: "http://127.0.0.1:1/h", DefaultAttempts: 1}); err == nil || !strings.Contains(err.Error(), "url") {
		t.Errorf("an unknown url field should be refused, got %v", err)
	}
}

func TestSpecValidation(t *testing.T) {
	cases := map[string]Spec{
		"neither path nor command": {Checks: []Check{{}}},
		"both path and command":    {Checks: []Check{{Path: "/", Command: "true"}}},
		"command with assertions":  {Checks: []Check{{Command: "true", Contains: []string{"x"}}}},
		"bad status":               {Checks: []Check{{Path: "/", Status: 7}}},
		"negative minBytes":        {Checks: []Check{{Path: "/", MinBytes: -1}}},
		"empty contains string":    {Checks: []Check{{Path: "/", Contains: []string{""}}}},
		"negative attempts":        {Attempts: -1},
		"baseUrl with credentials": {BaseURL: "http://u:p@h/", Checks: []Check{{Path: "/"}}},
		"baseUrl not http":         {BaseURL: "ftp://h/", Checks: []Check{{Path: "/"}}},
		"path with no base":        {Checks: []Check{{Path: "/"}}},
	}
	for name, spec := range cases {
		spec := spec
		if _, err := spec.Plan(Context{DefaultAttempts: 1}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestUnknownFieldsRoundTripButAreNotRunnable(t *testing.T) {
	in := `{"checks":[{"path":"/","contain":["typo"],"futureThing":1}],"attempts":3,"retryStrategy":"x"}`
	var s Spec
	if err := json.Unmarshal([]byte(in), &s); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"contain":["typo"]`, `"futureThing":1`, `"retryStrategy":"x"`, `"attempts":3`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("round trip lost %s: %s", want, out)
		}
	}
	// A mistyped assertion key must not become a check that asserts nothing.
	if _, err := s.Plan(Context{HealthURL: "http://h/x", DefaultAttempts: 1}); err == nil {
		t.Error("a spec with an unknown assertion key was run")
	}
}

type mapRunner struct {
	out  map[string]string
	err  map[string]error
	seen []string
}

func (m *mapRunner) Run(cmd string) (string, error) {
	m.seen = append(m.seen, cmd)
	for k, e := range m.err {
		if strings.Contains(cmd, k) {
			return "", e
		}
	}
	for k, o := range m.out {
		if strings.Contains(cmd, k) {
			return o, nil
		}
	}
	return "", nil
}

func TestPassStopsAtTheFirstFailureAndNamesIt(t *testing.T) {
	spec := &Spec{BaseURL: "http://127.0.0.1:1", Checks: []Check{
		{Path: "/a", Contains: []string{"ok"}},
		{Path: "/b", Contains: []string{"ok"}},
	}}
	p := planFor(t, spec, Context{})
	r := &mapRunner{out: map[string]string{"/a": served(200, "nope"), "/b": served(200, "ok")}}

	ok, why := Pass(r, p.Steps)

	if ok || !strings.Contains(why, "GET /a") || !strings.Contains(why, "contains") {
		t.Errorf("ok=%v why=%q", ok, why)
	}
	if len(r.seen) != 1 {
		t.Errorf("the second check ran after the first failed: %v", r.seen)
	}
}

type fakeClock struct{ t time.Time }

func (f *fakeClock) clock() Clock {
	return Clock{Now: func() time.Time { return f.t }, Sleep: func(d time.Duration) { f.t = f.t.Add(d) }}
}

func TestRetryReturnsOnTheFirstPass(t *testing.T) {
	fc := &fakeClock{t: time.Unix(0, 0)}
	n := 0
	out := Retry(Budget{Attempts: 5, Delay: time.Second}, fc.clock(), func() (bool, string) {
		n++
		return n == 3, "not yet"
	})
	if !out.OK || out.Attempts != 3 || out.Err() != nil {
		t.Errorf("outcome = %+v", out)
	}
}

func TestRetryIsBoundedByAttempts(t *testing.T) {
	fc := &fakeClock{t: time.Unix(0, 0)}
	n := 0
	out := Retry(Budget{Attempts: 4, Delay: time.Second}, fc.clock(), func() (bool, string) { n++; return false, "down" })
	if out.OK || n != 4 || out.TimedOut || out.Err() == nil {
		t.Errorf("n=%d outcome=%+v", n, out)
	}
}

// However it is configured, verify cannot retry without end.
func TestRetryClampsAbsurdBudgets(t *testing.T) {
	b := Budget{Attempts: 1 << 30, Delay: 24 * time.Hour, Timeout: 1000 * time.Hour}.Bounded()
	if b.Attempts != MaxAttempts || b.Delay != MaxDelay || b.Timeout != MaxTimeout {
		t.Errorf("not clamped: %+v", b)
	}
	if got := (Budget{}).Bounded(); got.Attempts != 1 || got.Timeout != DefaultTimeout {
		t.Errorf("zero budget: %+v", got)
	}

	fc := &fakeClock{t: time.Unix(0, 0)}
	n := 0
	out := Retry(Budget{Attempts: 1 << 30, Delay: 24 * time.Hour}, fc.clock(), func() (bool, string) { n++; return false, "down" })
	if n > MaxAttempts {
		t.Errorf("ran %d attempts", n)
	}
	if out.OK {
		t.Error("an exhausted budget passed")
	}
}

func TestATimeoutIsReportedNeverASilentPass(t *testing.T) {
	fc := &fakeClock{t: time.Unix(0, 0)}
	n := 0
	// 60s apart against a 150s budget: the loop must stop on time, not on attempts.
	out := Retry(Budget{Attempts: 50, Delay: 60 * time.Second, Timeout: 150 * time.Second}, fc.clock(), func() (bool, string) {
		n++
		return false, "GET /: status 502, want 2xx"
	})

	if out.OK || !out.TimedOut {
		t.Fatalf("outcome = %+v", out)
	}
	if n >= 50 {
		t.Errorf("ran %d attempts against a 150s budget", n)
	}
	var te *TimeoutError
	if !errors.As(out.Err(), &te) {
		t.Fatalf("Err() = %v, want a *TimeoutError", out.Err())
	}
	if !strings.Contains(out.Err().Error(), "timed out") || !strings.Contains(out.Err().Error(), "status 502") {
		t.Errorf("the timeout should say so and keep the last failure: %v", out.Err())
	}
}

// The fetch command runs against a real server here, through a shell and curl, so the redirect rule
// is proved on the wire and not only in the command text.
func TestARedirectToAnotherHostIsNotFollowed(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not installed")
	}
	var hits atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("baryodev-pwa.js"))
	}))
	defer other.Close()
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/", http.StatusFound)
	}))
	defer site.Close()

	spec := &Spec{BaseURL: site.URL, Checks: []Check{{Path: "/", Contains: []string{"baryodev-pwa.js"}}}}
	p := planFor(t, spec, Context{})

	ok, why := Pass(shRunner{}, p.Steps)

	if ok {
		t.Fatalf("a redirect to another host satisfied the marker: %s", why)
	}
	if hits.Load() != 0 {
		t.Errorf("the redirect was followed: the other host got %d request(s)", hits.Load())
	}
	if !strings.Contains(why, "302") {
		t.Errorf("the failure should name the status: %q", why)
	}
}

func TestTheFetchCommandWorksAgainstARealServer(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not installed")
	}
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/build-info" {
			_, _ = w.Write([]byte(`{"version":"0.3.0"}`))
			return
		}
		_, _ = w.Write([]byte(realSite))
	}))
	defer site.Close()

	spec := &Spec{BaseURL: site.URL, Checks: []Check{
		{Path: "/", Contains: []string{"baryodev-pwa.js"}, Absent: []string{"Umbraco"}, MinBytes: 100},
		{Path: "/build-info", JSONMatch: map[string]string{"version": "0.3.0"}},
	}}
	p := planFor(t, spec, Context{})
	if ok, why := Pass(shRunner{}, p.Steps); !ok {
		t.Errorf("a healthy site failed: %s", why)
	}

	spec.Checks[1].JSONMatch["version"] = "9.9.9"
	p = planFor(t, spec, Context{})
	if ok, why := Pass(shRunner{}, p.Steps); ok || !strings.Contains(why, "jsonMatch") {
		t.Errorf("a stale version passed: ok=%v %q", ok, why)
	}
}

type shRunner struct{}

func (shRunner) Run(cmd string) (string, error) {
	out, err := exec.Command("sh", "-c", cmd).Output()
	return string(out), err
}

func TestACommandCheckRunsUnderTimeoutAndExit124IsATimeout(t *testing.T) {
	spec := &Spec{Checks: []Check{{Command: "sh scripts/check.sh"}}, TimeoutSeconds: 90}
	p := planFor(t, spec, Context{Dir: "/srv/app"})
	step := p.Steps[0]
	if !strings.Contains(step.Cmd, "timeout 90 sh -c ") {
		t.Errorf("the command check is not wrapped in timeout: %q", step.Cmd)
	}
	ok, why := step.Judge("", errors.New("exit status 124"))
	if ok || !strings.Contains(why, "timed out") || !strings.Contains(why, "scripts/check.sh") {
		t.Errorf("exit 124 should be a timeout naming the check: ok=%v %q", ok, why)
	}
	if ok, why := step.Judge("", errors.New("exit status 1")); ok || strings.Contains(why, "timed out") {
		t.Errorf("exit 1 is a plain failure: ok=%v %q", ok, why)
	}
	def := planFor(t, &Spec{Checks: []Check{{Command: "true"}}}, Context{}).Steps[0]
	if !strings.Contains(def.Cmd, "timeout 300 sh -c ") {
		t.Errorf("default timeout missing: %q", def.Cmd)
	}
}

// An unset timeout grows with attempts x delay (up to MaxTimeout) instead of stopping at 5 minutes,
// and a plan leaves it unset so a flag or the manifest can still change attempts and delay.
func TestUnsetTimeoutFollowsAttemptsTimesDelay(t *testing.T) {
	b := Budget{Attempts: 60, Delay: 10 * time.Second}.Bounded()
	if b.Timeout < 10*time.Minute || b.Timeout > MaxTimeout {
		t.Errorf("60 x 10s got timeout %v", b.Timeout)
	}
	if b := (Budget{Attempts: 100, Delay: time.Minute}).Bounded(); b.Timeout != MaxTimeout {
		t.Errorf("cap: %v", b.Timeout)
	}
	p := planFor(t, &Spec{Checks: []Check{{Command: "true"}}}, Context{DefaultAttempts: 5, DefaultDelay: time.Second})
	if p.Budget.Timeout != 0 {
		t.Errorf("a spec with no timeoutSeconds should leave Timeout unset, got %v", p.Budget.Timeout)
	}
	p = planFor(t, &Spec{Checks: []Check{{Command: "true"}}, TimeoutSeconds: 90}, Context{})
	if p.Budget.Timeout != 90*time.Second {
		t.Errorf("explicit timeout lost: %v", p.Budget.Timeout)
	}
}

// A path like /api?filter[type]=x is a literal URL, not a curl glob range.
func TestTheFetchCommandTurnsOffURLGlobbing(t *testing.T) {
	cmd := fetchCmd("http://127.0.0.1:8080/api?filter[type]=x")
	if !strings.Contains(cmd, "--globoff") {
		t.Errorf("fetch command can glob: %s", cmd)
	}
	p := planFor(t, &Spec{BaseURL: "http://127.0.0.1:8080", Checks: []Check{{Path: "/api?filter[type]=x", Contains: []string{"x"}}}}, Context{})
	if !strings.Contains(p.Steps[0].Cmd, "--globoff") {
		t.Errorf("planned step can glob: %s", p.Steps[0].Cmd)
	}
}
