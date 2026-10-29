// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package release

import (
	"strings"
	"testing"
	"time"

	"github.com/BaryoDev/BaryoVM/internal/verify"
)

func ctx(healthURL string) verify.Context {
	return verify.Context{HealthURL: healthURL, Dir: "/opt/shop"}
}

// A stack with only a healthUrl plans exactly as it did before the spec existed.
func TestAHealthURLOnlyStackPlansAsBefore(t *testing.T) {
	m := &Manifest{}
	old := m.Verification("http://127.0.0.1:5005/health")
	got, err := m.VerificationFor(nil, ctx("http://127.0.0.1:5005/health"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != "healthUrl" || len(got.Commands) != 1 || got.Commands[0] != old.Commands[0] || got.Attempts != 5 || got.Delay != 3*time.Second {
		t.Errorf("plan = %+v", got)
	}
	want := "curl --fail --silent --show-error --max-time 10 -o /dev/null 'http://127.0.0.1:5005/health'"
	if got.Commands[0] != want {
		t.Errorf("command = %q, want %q", got.Commands[0], want)
	}
}

func TestTheStackSpecIsThePlanWhenTheManifestHasNoVerify(t *testing.T) {
	spec := &verify.Spec{
		Checks:   []verify.Check{{Path: "/", Contains: []string{"marker"}}, {Command: "sh check.sh"}},
		Attempts: 8, DelaySeconds: 2,
	}
	p, err := (&Manifest{}).VerificationFor(spec, ctx("http://127.0.0.1:5005/health"))
	if err != nil {
		t.Fatal(err)
	}
	if p.Source != "verify" || len(p.Steps) != 2 || len(p.Commands) != 2 {
		t.Fatalf("plan = %+v", p)
	}
	if !strings.Contains(p.Commands[0], "'http://127.0.0.1:5005/'") {
		t.Errorf("first check should fetch the healthUrl's origin: %s", p.Commands[0])
	}
	if p.Commands[1] != "timeout 300 sh -c 'cd '\\''/opt/shop'\\'' && sh check.sh'" {
		t.Errorf("command check = %q", p.Commands[1])
	}
	if p.Attempts != 8 || p.Delay != 2*time.Second {
		t.Errorf("budget = %d x %s", p.Attempts, p.Delay)
	}
}

// A manifest's own verify still wins for a release.
func TestTheManifestVerifyStillWinsOverTheStackSpec(t *testing.T) {
	m := &Manifest{Verify: []string{"sh scripts/check.sh"}, RemoteRoot: "/srv/site"}
	spec := &verify.Spec{Checks: []verify.Check{{Path: "/", Contains: []string{"marker"}}}}

	p, err := m.VerificationFor(spec, ctx("http://127.0.0.1:5005/health"))
	if err != nil {
		t.Fatal(err)
	}
	if p.Source != "manifest" || len(p.Commands) != 1 || p.Commands[0] != "cd '/srv/site' && sh scripts/check.sh" {
		t.Errorf("plan = %+v", p)
	}
}

func TestAnInvalidStackSpecIsAnErrorNotAnEmptyPlan(t *testing.T) {
	spec := &verify.Spec{Checks: []verify.Check{{Path: "//evil.example/x"}}}
	if _, err := (&Manifest{}).VerificationFor(spec, ctx("http://127.0.0.1:5005/health")); err == nil {
		t.Error("an invalid spec was accepted")
	}
}

// Whatever the manifest says, the release verify cannot retry without end.
func TestReleaseAttemptsAreBounded(t *testing.T) {
	m := &Manifest{Verify: []string{"true"}, VerifyAttempts: 1 << 30, VerifyDelaySeconds: 1 << 30}
	p := m.Verification("")
	if p.Attempts > verify.MaxAttempts || p.Delay > verify.MaxDelay {
		t.Errorf("unbounded: %d x %s", p.Attempts, p.Delay)
	}
	if p.Budget().Bounded().Timeout <= 0 {
		t.Error("no total time budget")
	}
}
