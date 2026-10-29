// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package update

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/BaryoDev/BaryoVM/internal/compose"
	"github.com/BaryoDev/BaryoVM/internal/verify"
)

// cmdHost stands in for the VM: it answers the commands a verify plan runs.
type cmdHost struct {
	answers map[string]string
	fail    map[string]error
	ran     []string
}

func (h *cmdHost) Run(cmd string) (string, error) {
	h.ran = append(h.ran, cmd)
	for k, e := range h.fail {
		if strings.Contains(cmd, k) {
			return "", e
		}
	}
	for k, o := range h.answers {
		if strings.Contains(cmd, k) {
			return o, nil
		}
	}
	return "", nil
}

// planRunner is a fakeRunner whose Healthy is the real one: the shared verify plan run on cmdHost.
type planRunner struct {
	*fakeRunner
	host *cmdHost
	plan verify.Plan
}

func (p planRunner) Healthy() (bool, error) {
	p.calls = append(p.calls, "health")
	return healthy(p.host, p.plan)
}

func updatePlan(t *testing.T, spec *verify.Spec, healthURL string) verify.Plan {
	t.Helper()
	p, err := spec.Plan(verify.Context{
		Mode: verify.ForUpdate, HealthURL: healthURL, Dir: "/opt/shop",
		DefaultAttempts: 20, DefaultDelay: 3 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func pending() *fakeRunner {
	return &fakeRunner{
		before: []compose.Image{img("web", "web:latest", "old", "new")},
		after:  []compose.Image{img("web", "web:latest", "old", "new")},
	}
}

// #89: update verifies a stack whose check is a command, which it never could before. The same spec
// is read by release (see internal/release and internal/cli tests).
func TestUpdateVerifiesACommandCheck(t *testing.T) {
	spec := &verify.Spec{Checks: []verify.Check{{Command: "sh scripts/check.sh"}}}
	host := &cmdHost{}
	r := planRunner{fakeRunner: pending(), host: host, plan: updatePlan(t, spec, "")}
	o := Options{HasHealthCheck: true, HasBackup: true, HealthAttempts: 2, HealthDelay: time.Millisecond}

	res, err := Run(r, o)

	if err != nil || !res.Updated || res.Health != "healthy" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if len(host.ran) != 1 || host.ran[0] != "timeout 300 sh -c 'cd '\\''/opt/shop'\\'' && sh scripts/check.sh'" {
		t.Errorf("the command check did not run on the VM: %v", host.ran)
	}
}

func TestAFailingCommandCheckRollsTheUpdateBack(t *testing.T) {
	spec := &verify.Spec{Checks: []verify.Check{{Command: "sh scripts/check.sh"}}}
	host := &cmdHost{fail: map[string]error{"check.sh": errors.New("exit status 1")}}
	f := pending()
	r := planRunner{fakeRunner: f, host: host, plan: updatePlan(t, spec, "")}
	o := Options{HasHealthCheck: true, HasBackup: true, HealthAttempts: 2, HealthDelay: time.Millisecond}

	res, err := Run(r, o)

	if err == nil || !res.RolledBack || res.Updated {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if !strings.Contains(strings.Join(f.calls, " "), "retag:old") {
		t.Errorf("no rollback: %v", f.calls)
	}
}

// The defect of #25 on the update path: a placeholder page answers 200, and with a marker set the
// update is not healthy and goes back.
func TestUpdateRollsBackWhenAPlaceholderAnswers200(t *testing.T) {
	spec := &verify.Spec{Checks: []verify.Check{{Path: "/", Contains: []string{"baryodev-pwa.js"}}}}
	body := "<h1>Welcome to your Umbraco installation</h1>\n--baryovm-status:200"
	host := &cmdHost{answers: map[string]string{"curl": body}}
	f := pending()
	r := planRunner{fakeRunner: f, host: host, plan: updatePlan(t, spec, "http://127.0.0.1:8091/health")}
	o := Options{HasHealthCheck: true, HasBackup: true, HealthAttempts: 2, HealthDelay: time.Millisecond}

	res, err := Run(r, o)

	if err == nil || !res.RolledBack {
		t.Fatalf("a placeholder 200 was accepted: res=%+v err=%v", res, err)
	}
	if !strings.Contains(err.Error(), "baryodev-pwa.js") {
		t.Errorf("the error should name the assertion that failed: %v", err)
	}

	// And the real site is healthy under the same spec.
	host.answers["curl"] = `<script src="baryodev-pwa.js"></script>` + "\n--baryovm-status:200"
	r = planRunner{fakeRunner: pending(), host: host, plan: r.plan}
	if res, err := Run(r, o); err != nil || !res.Updated {
		t.Errorf("the real site failed verify: res=%+v err=%v", res, err)
	}
}

// A stack with only a healthUrl is judged by the old rule: any 2xx status code.
func TestAHealthURLOnlyStackIsStillJudgedBy2xx(t *testing.T) {
	plan := updatePlan(t, nil, "http://127.0.0.1:8091/health")
	host := &cmdHost{answers: map[string]string{"http_code": "200"}}
	if ok, err := healthy(host, plan); !ok || err != nil {
		t.Errorf("200: ok=%v err=%v", ok, err)
	}
	host = &cmdHost{answers: map[string]string{"http_code": "503"}}
	ok, err := healthy(host, plan)
	var failed *verify.FailedError
	if ok || !errors.As(err, &failed) {
		t.Errorf("503: ok=%v err=%v", ok, err)
	}
	if ok, _ := healthy(&cmdHost{}, verify.Plan{}); ok {
		t.Error("an empty plan must not read as healthy")
	}
}

func fakeClock() (verify.Clock, *time.Time) {
	now := time.Unix(0, 0)
	return verify.Clock{
		Now:   func() time.Time { return now },
		Sleep: func(d time.Duration) { now = now.Add(d) },
	}, &now
}

// Attempts and total time are bounded, and running out of time is reported as a timeout.
func TestWaitHealthyIsBoundedAndReportsATimeout(t *testing.T) {
	clk, _ := fakeClock()
	prev := clock
	clock = clk
	t.Cleanup(func() { clock = prev })

	calls := 0
	f := &fakeRunner{}
	r := countingRunner{fakeRunner: f, n: &calls}
	ok, err := waitHealthy(r, Options{HealthAttempts: 1000, HealthDelay: 30 * time.Second, HealthTimeout: 100 * time.Second})

	if ok {
		t.Fatal("an unhealthy stack passed")
	}
	var te *verify.TimeoutError
	if !errors.As(err, &te) {
		t.Fatalf("want a timeout error, got %v", err)
	}
	if calls >= 1000 || calls > 5 {
		t.Errorf("%d attempts inside a 100s budget with 30s delays", calls)
	}
	if got := healthWhy(err); !strings.Contains(got, "timed out") {
		t.Errorf("healthWhy = %q", got)
	}

	// With no timeout configured the attempts are still capped.
	calls = 0
	if ok, _ := waitHealthy(r, Options{HealthAttempts: 1 << 30, HealthDelay: time.Millisecond}); ok || calls > verify.MaxAttempts {
		t.Errorf("unbounded wait: ok=%v calls=%d", ok, calls)
	}
}

type countingRunner struct {
	*fakeRunner
	n *int
}

func (c countingRunner) Healthy() (bool, error) { *c.n++; return false, nil }

// 60 attempts x 10s delays is 10 minutes of waiting by the caller's own choice. The default 5
// minute budget must not cut it short and roll the update back.
func TestWaitHealthyHonoursAttemptsTimesDelayWithoutATimeout(t *testing.T) {
	clk, _ := fakeClock()
	prev := clock
	clock = clk
	t.Cleanup(func() { clock = prev })

	calls := 0
	r := countingRunner{fakeRunner: &fakeRunner{}, n: &calls}
	ok, err := waitHealthy(r, Options{HealthAttempts: 60, HealthDelay: 10 * time.Second})

	var te *verify.TimeoutError
	if ok || errors.As(err, &te) || calls != 60 {
		t.Errorf("ok=%v calls=%d err=%v, want all 60 attempts and no timeout", ok, calls, err)
	}
}
