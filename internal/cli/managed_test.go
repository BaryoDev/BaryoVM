// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"github.com/BaryoDev/BaryoVM/internal/managed"
)

// fakeCloud replaces the CLI seam. fail maps a substring of the joined argv to an exit code.
type fakeCloud struct {
	seen []string
	fail map[string]int
	log  *[]string // shared with the ssh fake, to assert order across both
}

func (f *fakeCloud) exec(argv []string) (string, int, error) {
	line := managed.Join(argv)
	f.seen = append(f.seen, line)
	if f.log != nil {
		*f.log = append(*f.log, "cloud: "+line)
	}
	for part, code := range f.fail {
		if strings.Contains(line, part) {
			if code == -1 {
				return "", -1, fmt.Errorf("exec: %q: executable file not found in $PATH", argv[0])
			}
			return "boom\nExpiredToken: sign in again", code, fmt.Errorf("exit status %d", code)
		}
	}
	return "ok", 0, nil
}

func withFakeCloud(t *testing.T, f *fakeCloud) {
	t.Helper()
	prev, prevNow := managedExec, managedNow
	managedExec = f.exec
	managedNow = func() time.Time { return time.Date(2026, 10, 30, 1, 2, 3, 0, time.UTC) }
	t.Cleanup(func() { managedExec, managedNow = prev, prevNow })
}

func setManaged(t *testing.T, name string, spec *managed.Spec) {
	t.Helper()
	store, err := fleet.Load()
	if err != nil {
		t.Fatal(err)
	}
	store.FindStack(name).Managed = spec
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
}

func TestSetManagedStoresNamesOnly(t *testing.T) {
	configuredStackFixture(t)
	runCmd(t, newStackSetManagedCmd(), nil, true, "app", "--provider", "aws", "--profile", "prod",
		"--region", "ap-southeast-1", "--database", "shop-db")
	got := reloadStack(t, "app")
	if got.Managed == nil || got.Managed.Profile != "prod" || got.Managed.Database != "shop-db" {
		t.Fatalf("got %+v", got.Managed)
	}
	raw, _ := os.ReadFile(filepath.Join(os.Getenv("BARYOVM_HOME"), "fleet.json"))
	var doc struct {
		Stacks []struct {
			Managed map[string]any `json:"managed"`
		} `json:"stacks"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for k := range doc.Stacks[0].Managed {
		switch k {
		case "provider", "profile", "region", "database":
		default:
			t.Errorf("fleet.json holds an unexpected managed key %q", k)
		}
	}

	runCmd(t, newStackSetManagedCmd(), nil, true, "app", "--provider", "azure", "--subscription", "s1",
		"--resource-group", "rg", "--database", "pg")
	if m := reloadStack(t, "app").Managed; m == nil || m.Provider != managed.Azure || m.Profile != "" {
		t.Errorf("a second call replaces the first: %+v", m)
	}
	runCmd(t, newStackSetManagedCmd(), nil, true, "app", "--clear")
	if reloadStack(t, "app").Managed != nil {
		t.Error("--clear must remove it")
	}
}

func TestSetManagedRefusesBadInputAndStoresNothing(t *testing.T) {
	for name, args := range map[string][]string{
		"dash profile": {"--provider", "aws", "--profile", "--debug", "--region", "us-east-1", "--database", "d"},
		"no region":    {"--provider", "aws", "--database", "d"},
		"shell chars":  {"--provider", "azure", "--subscription", "s", "--resource-group", "rg;ls", "--database", "d"},
		"no provider":  {"--database", "d"},
		"clear+flag":   {"--clear", "--database", "d"},
	} {
		configuredStackFixture(t)
		_, _, err := runCmdE(t, newStackSetManagedCmd(), nil, false, append([]string{"app"}, args...)...)
		if err == nil {
			t.Errorf("%s: want a refusal", name)
		}
		if reloadStack(t, "app").Managed != nil {
			t.Errorf("%s: a refused spec was stored", name)
		}
	}
}

func TestManagedSnapshotRunsEachCallAndKeepsTheRecord(t *testing.T) {
	f := &fakeCloud{}
	withFakeCloud(t, f)
	id, calls, err := managedSnapshot(managed.Spec{Provider: managed.AWS, Profile: "p", Region: "us-east-1", Database: "db"})
	if err != nil {
		t.Fatal(err)
	}
	if id != "baryovm-20261030-010203" || len(calls) != 3 {
		t.Fatalf("id %q calls %d", id, len(calls))
	}
	if calls[2].Argv[len(calls[2].Argv)-4] != "wait" || calls[2].ExitCode != 0 || calls[2].Output != "ok" {
		t.Errorf("wait call: %+v", calls[2])
	}
}

func TestManagedSnapshotStopsAtFirstFailureWithTheHint(t *testing.T) {
	f := &fakeCloud{fail: map[string]int{"get-caller-identity": 255}}
	withFakeCloud(t, f)
	_, calls, err := managedSnapshot(managed.Spec{Provider: managed.AWS, Profile: "p", Region: "us-east-1", Database: "db"})
	if err == nil || !strings.Contains(err.Error(), "aws sso login --profile p") {
		t.Fatalf("want the sign-in hint, got %v", err)
	}
	if len(f.seen) != 1 || len(calls) != 1 || calls[0].ExitCode != 255 {
		t.Errorf("nothing may run after a failed sign-in: %v %+v", f.seen, calls)
	}
}

func TestReleaseSnapshotsBeforeTheBackupAndBlocksOnFailure(t *testing.T) {
	releaseFixture(t)
	setManaged(t, "demo", &managed.Spec{Provider: managed.AWS, Profile: "p", Region: "us-east-1", Database: "shop-db"})
	host := newStateHost(nil)
	inner := &fakeRunner{answers: stateAnswers(ownedDemo)}
	var order []string
	f := &fakeCloud{log: &order}
	withFakeCloud(t, f)

	stdout, err := stateRelease(t, host, inner, nil)
	if err != nil {
		t.Fatalf("release: %v\n%s", err, stdout)
	}
	// The snapshot calls came first: no ssh command ran before the wait call returned.
	if len(f.seen) != 3 || !strings.Contains(f.seen[1], "create-db-snapshot") || !strings.Contains(f.seen[2], "wait db-snapshot-available") {
		t.Fatalf("cloud calls: %v", f.seen)
	}
	var env struct {
		Data struct {
			Snapshot string         `json:"managedSnapshot"`
			Cloud    []managed.Call `json:"cloud"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatal(err)
	}
	if env.Data.Snapshot != "baryovm-20261030-010203" || len(env.Data.Cloud) != 3 ||
		env.Data.Cloud[1].Argv[0] != "aws" || env.Data.Cloud[1].Output != "ok" {
		t.Errorf("envelope: %+v", env.Data)
	}
	if !didRun(inner.seen, "pg_dump") {
		t.Error("the VM backup still runs after the snapshot")
	}

	// A failed wait stops the release before the VM backup or anything else.
	releaseFixture(t)
	setManaged(t, "demo", &managed.Spec{Provider: managed.AWS, Profile: "p", Region: "us-east-1", Database: "shop-db"})
	host = newStateHost(nil)
	inner = &fakeRunner{answers: stateAnswers(ownedDemo)}
	f2 := &fakeCloud{fail: map[string]int{"wait db-snapshot-available": 255}}
	withFakeCloud(t, f2)
	stdout, err = stateRelease(t, host, inner, nil)
	if err == nil {
		t.Fatal("a snapshot that did not finish must block the release")
	}
	if didRun(inner.seen, "pg_dump") || didRun(inner.seen, "up -d") {
		t.Errorf("nothing may run on the VM after a failed snapshot: %v", inner.seen)
	}
	e := parseFail(t, stdout)
	cloud, _ := e.Data["cloud"].([]any)
	if len(cloud) != 3 || e.Data["failedStep"] != "managed-snapshot" {
		t.Errorf("failure envelope should carry the calls and the step: %v", e.Data)
	}
	last, _ := cloud[2].(map[string]any)
	if last["exitCode"] != float64(255) || !strings.Contains(fmt.Sprint(last["output"]), "ExpiredToken") {
		t.Errorf("failing call: %v", last)
	}
}

func TestNoBackupSkipsTheManagedSnapshotToo(t *testing.T) {
	releaseFixture(t)
	setManaged(t, "demo", &managed.Spec{Provider: managed.Azure, Subscription: "s", ResourceGroup: "rg", Database: "pg"})
	host := newStateHost(nil)
	inner := &fakeRunner{answers: stateAnswers(ownedDemo)}
	f := &fakeCloud{}
	withFakeCloud(t, f)
	if stdout, err := stateRelease(t, host, inner, nil, "--no-backup"); err != nil {
		t.Fatalf("release: %v\n%s", err, stdout)
	}
	if len(f.seen) != 0 {
		t.Errorf("--no-backup must skip the snapshot: %v", f.seen)
	}
}

func TestDryRunListsTheSnapshotAndRunsNothing(t *testing.T) {
	releaseFixture(t)
	setManaged(t, "demo", &managed.Spec{Provider: managed.Azure, Subscription: "s", ResourceGroup: "rg", Database: "pg"})
	f := &fakeCloud{}
	withFakeCloud(t, f)
	withLookPath(t, hasRsync)
	stdout, _ := runCmd(t, newStackReleaseCmd(), nil, true, "demo", "--dry-run")
	if !strings.Contains(stdout, "az postgres flexible-server backup create") || len(f.seen) != 0 {
		t.Errorf("dry run: %s calls %v", stdout, f.seen)
	}
}

func TestDoctorReportsAMissingOrSignedOutCLI(t *testing.T) {
	stacks := []fleet.Stack{
		{Name: "a", Managed: &managed.Spec{Provider: managed.AWS, Profile: "p", Region: "us-east-1", Database: "db"}},
		{Name: "a2", Managed: &managed.Spec{Provider: managed.AWS, Profile: "p", Region: "us-east-1", Database: "db2"}},
		{Name: "z", Managed: &managed.Spec{Provider: managed.Azure, Subscription: "s", ResourceGroup: "rg", Database: "pg"}},
		{Name: "none"},
	}
	f := &fakeCloud{fail: map[string]int{"get-caller-identity": 255, "account show": -1}}
	withFakeCloud(t, f)
	got := managedDoctorChecks(stacks)
	if len(got) != 2 {
		t.Fatalf("one check per profile or subscription, none for a stack without one: %+v", got)
	}
	for _, c := range got {
		if c.Present || c.Optional {
			t.Errorf("%s: a stack that uses the CLI makes it required: %+v", c.Name, c)
		}
		if len(c.Calls) != 1 {
			t.Errorf("%s: the call belongs in the report: %+v", c.Name, c.Calls)
		}
	}
	if !strings.Contains(got[0].Hint, "aws sso login --profile p") || got[0].Calls[0].ExitCode != 255 {
		t.Errorf("aws: %+v", got[0])
	}
	if !strings.Contains(got[1].Hint, "az login") || got[1].Calls[0].ExitCode != -1 || !strings.Contains(got[1].Detail, "installed") {
		t.Errorf("az: %+v", got[1])
	}

	f.fail = nil
	for _, c := range managedDoctorChecks(stacks) {
		if !c.Present {
			t.Errorf("signed in should pass: %+v", c)
		}
	}
}
