// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package fleet

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/BaryoDev/BaryoVM/internal/backup"
)

func loadFleetJSON(t *testing.T, body string) *Store {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("BARYOVM_HOME", dir)
	if err := os.WriteFile(filepath.Join(dir, "fleet.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// An existing fleet.json with dbContainer, dbName and dbUser loads as the first strategy and
// behaves as before.
func TestLegacyDatabaseFieldsLoadAsAPostgresContainerStrategy(t *testing.T) {
	s := loadFleetJSON(t, `{"vms":[],"stacks":[{"name":"a","vm":"v","dir":"/opt/a","dbContainer":"pg","dbName":"appdb","dbUser":"app","envFile":".env"}]}`)
	list, declared := s.FindStack("a").BackupStrategies()
	want := backup.Strategy{Kind: backup.KindPostgresContainer, Container: "pg", Database: "appdb", User: "app"}
	if !declared || len(list) != 1 || !reflect.DeepEqual(list[0], want) {
		t.Errorf("got %+v declared=%v", list, declared)
	}
}

// noDatabase loads as an explicit empty list, which is what --auto accepts.
func TestLegacyNoDatabaseLoadsAsAnEmptyDeclaredList(t *testing.T) {
	s := loadFleetJSON(t, `{"vms":[],"stacks":[{"name":"site","vm":"v","dir":"/srv","noDatabase":true}]}`)
	list, declared := s.FindStack("site").BackupStrategies()
	if !declared || len(list) != 0 {
		t.Errorf("got %+v declared=%v", list, declared)
	}
}

// A stack that says nothing is undeclared, and --auto refuses it. So does one with half a database.
func TestAStackWithNoBackupDeclarationIsUndeclared(t *testing.T) {
	s := loadFleetJSON(t, `{"vms":[],"stacks":[{"name":"a","vm":"v","dir":"/x"},{"name":"b","vm":"v","dir":"/y","dbName":"only"}]}`)
	for _, n := range []string{"a", "b"} {
		if list, declared := s.FindStack(n).BackupStrategies(); declared || len(list) != 0 {
			t.Errorf("%s: got %+v declared=%v", n, list, declared)
		}
	}
}

// "backup": [] is a statement, and it must survive a save: an omitted empty slice would turn
// "nothing to back up" back into "never said".
func TestAnExplicitEmptyBackupListSurvivesASave(t *testing.T) {
	s := loadFleetJSON(t, `{"vms":[],"stacks":[{"name":"site","vm":"v","dir":"/srv","backup":[]}]}`)
	if _, declared := s.FindStack("site").BackupStrategies(); !declared {
		t.Fatal("an explicit empty list must load as declared")
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(os.Getenv("BARYOVM_HOME"), "fleet.json"))
	if !strings.Contains(string(raw), `"backup": []`) {
		t.Errorf("the empty list was dropped on save:\n%s", raw)
	}
	again, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if list, declared := again.FindStack("site").BackupStrategies(); !declared || len(list) != 0 {
		t.Errorf("after reload: %+v declared=%v", list, declared)
	}
}

func TestAStrategyListAndUnknownFieldsRoundTrip(t *testing.T) {
	s := loadFleetJSON(t, `{"vms":[],"stacks":[{"name":"a","vm":"v","dir":"/x","futureField":{"k":1},
		"backup":[{"kind":"postgres-remote","host":"db.example.com","database":"app","envFile":".env"},
		{"kind":"files","paths":["/srv/up"]}]}]}`)
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	again, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	st := again.FindStack("a")
	list, declared := st.BackupStrategies()
	if !declared || len(list) != 2 || list[0].Kind != backup.KindPostgresRemote || list[0].Host != "db.example.com" || list[1].Paths[0] != "/srv/up" {
		t.Errorf("strategies lost: %+v", list)
	}
	raw, _ := os.ReadFile(filepath.Join(os.Getenv("BARYOVM_HOME"), "fleet.json"))
	if !strings.Contains(string(raw), `"futureField"`) {
		t.Errorf("an unknown field was dropped:\n%s", raw)
	}
}

// When both are present the list is the truth; a stale legacy field must not add a second dump.
func TestADeclaredListWinsOverLegacyFields(t *testing.T) {
	empty := []backup.Strategy{}
	st := Stack{DBContainer: "pg", DBName: "appdb", Backup: &empty}
	if list, declared := st.BackupStrategies(); !declared || len(list) != 0 {
		t.Errorf("got %+v declared=%v", list, declared)
	}
}
