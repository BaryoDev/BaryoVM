// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"testing"

	"github.com/BaryoDev/BaryoVM/internal/fleet"
)

// configuredStackFixture registers a stack the way an operator ends up with one: added, then given
// an update policy by `stack set-update`, which `stack add` has no flags for.
func configuredStackFixture(t *testing.T) {
	t.Helper()
	t.Setenv("BARYOVM_HOME", t.TempDir())
	store := &fleet.Store{
		VMs: []fleet.VM{{Name: "vm1", Host: "10.0.0.1", User: "opc", KeyPath: "/keys/id"}},
		Stacks: []fleet.Stack{{
			Name: "app", VM: "vm1", Dir: "/opt/app",
			EnvFile: ".env", Keep: 7, ReleaseFile: "/src/app/release.json", Sudo: true,
			AutoUpdate: true, HealthURL: "http://127.0.0.1:8080/health",
			UpdateServices: []string{"api", "web"},
		}},
	}
	if err := store.Save(); err != nil {
		t.Fatalf("seed fleet: %v", err)
	}
}

func reloadStack(t *testing.T, name string) fleet.Stack {
	t.Helper()
	store, err := fleet.Load()
	if err != nil {
		t.Fatalf("load fleet: %v", err)
	}
	st := store.FindStack(name)
	if st == nil {
		t.Fatalf("stack %q is gone", name)
	}
	if len(store.Stacks) != 1 {
		t.Fatalf("re-adding should update the stack in place, got %d stacks", len(store.Stacks))
	}
	return *st
}

// The reported bug: the backup error tells you to add the database with `stack add`, and doing
// that wiped the update policy, so the next `stack update --auto` refused the stack.
func TestReAddingAStackToAddItsDatabaseKeepsEverythingElse(t *testing.T) {
	configuredStackFixture(t)

	runCmd(t, newStackAddCmd(), nil, true,
		"app", "--vm", "vm1", "--path", "/opt/app", "--db-container", "app-postgres-1", "--db-name", "appdb")

	got := reloadStack(t, "app")
	if got.DBContainer != "app-postgres-1" || got.DBName != "appdb" {
		t.Errorf("the flags that were passed did not land: dbContainer %q, dbName %q", got.DBContainer, got.DBName)
	}
	if !got.AutoUpdate {
		t.Error("AutoUpdate was cleared: `stack update --auto` now refuses this stack")
	}
	if got.HealthURL != "http://127.0.0.1:8080/health" {
		t.Errorf("HealthURL was cleared: got %q", got.HealthURL)
	}
	if len(got.UpdateServices) != 2 || got.UpdateServices[0] != "api" || got.UpdateServices[1] != "web" {
		t.Errorf("UpdateServices was cleared: got %v", got.UpdateServices)
	}
	if !got.Sudo {
		t.Error("Sudo was cleared: a root-owned stack would now run unelevated")
	}
	if got.EnvFile != ".env" || got.Keep != 7 || got.ReleaseFile != "/src/app/release.json" {
		t.Errorf("fields the flags did not name were cleared: envFile %q, keep %d, releaseFile %q",
			got.EnvFile, got.Keep, got.ReleaseFile)
	}
}

// Keeping unnamed fields must not make a named one impossible to turn off.
func TestReAddingAStackStillAppliesAFlagSetToItsZeroValue(t *testing.T) {
	configuredStackFixture(t)

	runCmd(t, newStackAddCmd(), nil, true,
		"app", "--vm", "vm1", "--path", "/srv/app", "--sudo=false", "--env-file", "", "--keep", "0")

	got := reloadStack(t, "app")
	if got.Sudo {
		t.Error("--sudo=false was ignored")
	}
	if got.EnvFile != "" || got.Keep != 0 {
		t.Errorf("explicit empty values were ignored: envFile %q, keep %d", got.EnvFile, got.Keep)
	}
	if got.Dir != "/srv/app" {
		t.Errorf("--path did not move the stack: got %q", got.Dir)
	}
	if !got.AutoUpdate {
		t.Error("AutoUpdate was cleared by a re-add that did not mention it")
	}
}

func TestAddingANewStackTakesOnlyItsFlags(t *testing.T) {
	configuredStackFixture(t)

	runCmd(t, newStackAddCmd(), nil, true, "other", "--vm", "vm1", "--path", "/opt/other", "--db-name", "otherdb")

	store, err := fleet.Load()
	if err != nil {
		t.Fatalf("load fleet: %v", err)
	}
	if len(store.Stacks) != 2 {
		t.Fatalf("want 2 stacks, got %d", len(store.Stacks))
	}
	got := store.FindStack("other")
	if got == nil {
		t.Fatal("new stack was not registered")
	}
	want := fleet.Stack{Name: "other", VM: "vm1", Dir: "/opt/other", DBName: "otherdb"}
	if got.Name != want.Name || got.VM != want.VM || got.Dir != want.Dir || got.DBName != want.DBName ||
		got.Sudo || got.AutoUpdate || got.HealthURL != "" || got.EnvFile != "" || got.Keep != 0 {
		t.Errorf("new stack picked up settings it was not given: %+v", *got)
	}
}
