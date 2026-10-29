// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"github.com/BaryoDev/BaryoVM/internal/update"
	"github.com/BaryoDev/BaryoVM/internal/verify"
)

// withVerifySpec puts a verify spec on the fixture's stack.
func withVerifySpec(t *testing.T, spec *verify.Spec) {
	t.Helper()
	store, err := fleet.Load()
	if err != nil {
		t.Fatal(err)
	}
	store.FindStack("demo").Verify = spec
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
}

func noSleep(t *testing.T) {
	t.Helper()
	prev := releaseClock
	releaseClock = verify.Clock{Now: time.Now, Sleep: func(time.Duration) {}}
	t.Cleanup(func() { releaseClock = prev })
}

// #89: a stack whose check is a command is verified by release. update_test in internal/update
// proves the same spec on the update side.
func TestReleaseRunsACommandCheckFromTheStackSpec(t *testing.T) {
	releasePathFixture(t, map[string]any{"noCompose": true}, "")
	withVerifySpec(t, &verify.Spec{Checks: []verify.Check{{Command: "sh scripts/shop-check.sh"}}})
	f := &fakeRunner{answers: map[string]string{"pg_dump": "", "shop-check.sh": ""}}
	var local []string

	stdout, _, err := releaseOn(t, f, &local, true)
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	v := verifyField(t, releaseData(t, stdout))
	if v["source"] != "verify" || v["checks"] != float64(1) {
		t.Errorf("data.verify = %v", v)
	}
	ran := false
	for _, c := range f.seen {
		if strings.Contains(c, "timeout 300 sh -c 'cd '\\''/home/opc/umbraco-pwa'\\'' && sh scripts/shop-check.sh'") {
			ran = true
		}
	}
	if !ran {
		t.Errorf("the command check never ran on the VM: %v", f.seen)
	}

	f2 := &fakeRunner{answers: map[string]string{"pg_dump": "", "shop-check.sh": ""}}
	_, stderr, err := releaseOn(t, f2, &local, false)
	if err != nil || !strings.Contains(stderr, "verified by the stack verify spec") {
		t.Errorf("err=%v stderr:\n%s", err, stderr)
	}
}

// The placeholder page answers 200, and with a marker set the release fails and says which
// assertion it was.
func TestReleaseFailsWhenAPlaceholderAnswers200(t *testing.T) {
	noSleep(t)
	releasePathFixture(t, map[string]any{"noCompose": true, "verifyAttempts": 2}, "http://127.0.0.1:8080/health")
	withVerifySpec(t, &verify.Spec{Checks: []verify.Check{{Path: "/", Contains: []string{"baryodev-pwa.js"}}}})
	placeholder := "<h1>Welcome to your Umbraco installation</h1>\n--baryovm-status:200"
	f := &fakeRunner{answers: map[string]string{"pg_dump": "", "curl": placeholder}}
	var local []string

	_, stderr, err := releaseOn(t, f, &local, false)

	if err == nil {
		t.Fatalf("a placeholder 200 passed verify; stderr:\n%s", stderr)
	}
	if !strings.Contains(err.Error(), "baryodev-pwa.js") || !strings.Contains(err.Error(), "failed verification") {
		t.Errorf("the failure should name the marker: %v", err)
	}

	f2 := &fakeRunner{answers: map[string]string{"pg_dump": "", "curl": `<script src="baryodev-pwa.js"></script>` + "\n--baryovm-status:200"}}
	if _, stderr, err := releaseOn(t, f2, &local, false); err != nil {
		t.Errorf("the real site failed: %v\n%s", err, stderr)
	}
}

// A stack with only a healthUrl releases as before: one `curl --fail` probe, source healthUrl.
func TestReleaseWithOnlyAHealthURLIsUnchanged(t *testing.T) {
	releasePathFixture(t, map[string]any{"noCompose": true}, "http://127.0.0.1:8080/health")
	f := &fakeRunner{answers: map[string]string{"pg_dump": "", "curl --fail": ""}}
	var local []string

	stdout, _, err := releaseOn(t, f, &local, true)
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if v := verifyField(t, releaseData(t, stdout)); v["source"] != "healthUrl" || v["checks"] != float64(1) {
		t.Errorf("data.verify = %v", v)
	}
}

// A timeout is a failure with its own words.
func TestReleaseReportsAVerifyTimeout(t *testing.T) {
	prev := releaseClock
	now := time.Unix(0, 0)
	releaseClock = verify.Clock{Now: func() time.Time { return now }, Sleep: func(d time.Duration) { now = now.Add(d) }}
	t.Cleanup(func() { releaseClock = prev })

	releasePathFixture(t, map[string]any{"noCompose": true}, "http://127.0.0.1:8080/health")
	withVerifySpec(t, &verify.Spec{
		Checks:   []verify.Check{{Path: "/", Contains: []string{"never"}}},
		Attempts: 50, DelaySeconds: 60, TimeoutSeconds: 100,
	})
	f := &fakeRunner{answers: map[string]string{"pg_dump": "", "curl": "x\n--baryovm-status:200"}}
	var local []string

	_, _, err := releaseOn(t, f, &local, false)

	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("want a timeout failure, got %v", err)
	}
}

// A spec that cannot be read is refused before the sync, not after it landed.
func TestReleaseRefusesAnInvalidSpecBeforeTransferring(t *testing.T) {
	releasePathFixture(t, map[string]any{"noCompose": true}, "http://127.0.0.1:8080/health")
	withVerifySpec(t, &verify.Spec{Checks: []verify.Check{{Path: "http://evil.example/"}}})
	f := &fakeRunner{answers: map[string]string{}}
	var local []string

	_, _, err := releaseOn(t, f, &local, false)

	if err == nil || !strings.Contains(err.Error(), "check 1") {
		t.Fatalf("err = %v", err)
	}
	if len(local) != 0 {
		t.Errorf("something was transferred: %v", local)
	}
}

func seedUpdateStack(t *testing.T, st fleet.Stack) {
	t.Helper()
	t.Setenv("BARYOVM_HOME", t.TempDir())
	s := &fleet.Store{}
	s.Upsert(fleet.VM{Name: "oracle", Host: "127.0.0.1", Port: 1, User: "deploy", KeyPath: "/nonexistent/key"})
	st.VM = "oracle"
	s.UpsertStack(st)
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
}

// --auto needs a way to verify, and a spec is one: a stack with a command check and no healthUrl is
// not refused as unverifiable.
func TestAutoAcceptsAVerifySpecInPlaceOfAHealthURL(t *testing.T) {
	seedUpdateStack(t, fleet.Stack{
		Name: "shop", Dir: "/opt/shop", AutoUpdate: true, NoDatabase: true,
		Verify: &verify.Spec{Checks: []verify.Check{{Command: "true"}}},
	})
	err := runStackUpdate("shop", nil, true, false, false, 1, time.Millisecond)
	if errors.Is(err, update.ErrNoHealthCheck) {
		t.Fatalf("a stack with a verify spec was refused as unverifiable: %v", err)
	}

	seedUpdateStack(t, fleet.Stack{Name: "shop", Dir: "/opt/shop", AutoUpdate: true, NoDatabase: true})
	err = runStackUpdate("shop", nil, true, false, false, 1, time.Millisecond)
	if !errors.Is(err, update.ErrNoHealthCheck) {
		t.Fatalf("a stack with neither healthUrl nor spec must be refused, got %v", err)
	}
}

func TestUpdateRefusesAnInvalidSpecBeforeDialling(t *testing.T) {
	seedUpdateStack(t, fleet.Stack{
		Name: "shop", Dir: "/opt/shop", HealthURL: "http://127.0.0.1:8080/",
		Verify: &verify.Spec{Checks: []verify.Check{{Path: "//evil.example/"}}},
	})
	err := runStackUpdate("shop", nil, false, true, false, 1, time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "check 1") {
		t.Fatalf("err = %v", err)
	}
}

func TestSetVerifyStoresAndClearsTheSpec(t *testing.T) {
	stackFixture(t)
	spec := filepath.Join(t.TempDir(), "verify.json")
	body := `{"checks":[{"command":"true"}],"attempts":3}`
	if err := os.WriteFile(spec, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, _, err := runCmdE(t, newStackSetVerifyCmd(), &fakeRunner{}, false, "app", "--file", spec); err != nil {
		t.Fatalf("set-verify: %v", err)
	}
	store, _ := fleet.Load()
	got := store.FindStack("app").Verify
	if got == nil || got.Attempts != 3 || len(got.Checks) != 1 || got.Checks[0].Command != "true" {
		t.Fatalf("spec not stored: %+v", got)
	}

	if _, _, err := runCmdE(t, newStackSetVerifyCmd(), &fakeRunner{}, false, "app", "--clear"); err != nil {
		t.Fatalf("clear: %v", err)
	}
	store, _ = fleet.Load()
	if store.FindStack("app").Verify != nil {
		t.Error("--clear left the spec")
	}
}

func TestSetVerifyRefusesAnInvalidSpecAndNeedsOneFlag(t *testing.T) {
	stackFixture(t)
	spec := filepath.Join(t.TempDir(), "verify.json")
	if err := os.WriteFile(spec, []byte(`{"checks":[{"path":"/","contain":["typo"]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCmdE(t, newStackSetVerifyCmd(), &fakeRunner{}, false, "app", "--file", spec); err == nil {
		t.Error("a spec with a mistyped key was stored")
	}
	if _, _, err := runCmdE(t, newStackSetVerifyCmd(), &fakeRunner{}, false, "app"); err == nil {
		t.Error("set-verify with neither flag succeeded")
	}
	store, _ := fleet.Load()
	if store.FindStack("app").Verify != nil {
		t.Error("an invalid spec was saved")
	}
}
