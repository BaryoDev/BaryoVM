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

	"github.com/BaryoDev/BaryoVM/internal/backup"
	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"github.com/BaryoDev/BaryoVM/internal/update"
)

func writeSpec(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "backup.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSetBackupStoresAValidatedListAndReplacesLegacyFields(t *testing.T) {
	configuredStackFixture(t)
	store, _ := fleet.Load()
	store.FindStack("app").DBContainer, store.FindStack("app").DBName = "old", "old"
	_ = store.Save()

	spec := writeSpec(t, `[{"kind":"postgres-remote","host":"db.example.com","database":"app","envFile":".env"},
		{"kind":"command","dump":"redis-cli --rdb -","restore":"cat > /data/dump.rdb"},
		{"kind":"files","paths":["/srv/uploads"]}]`)
	runCmd(t, newStackSetBackupCmd(), nil, true, "app", "--file", spec)

	got := reloadStack(t, "app")
	list, declared := got.BackupStrategies()
	if !declared || len(list) != 3 || list[0].Kind != backup.KindPostgresRemote {
		t.Errorf("list: %+v", list)
	}
	if got.DBContainer != "" || got.DBName != "" {
		t.Errorf("legacy fields must go so the two never disagree: %+v", got)
	}
}

func TestSetBackupRefusesBadSpecsAndStoresNothing(t *testing.T) {
	for name, body := range map[string]string{
		"no restore":        `[{"kind":"command","dump":"x"}]`,
		"no password":       `[{"kind":"postgres-remote","host":"h","database":"d"}]`,
		"unknown field":     `[{"kind":"files","paths":["/a"],"pth":"x"}]`,
		"unknown kind":      `[{"kind":"provider-snapshot"}]`,
		"not a list":        `{"kind":"files"}`,
		"relative file dir": `[{"kind":"files","paths":["srv"]}]`,
	} {
		configuredStackFixture(t)
		_, _, err := runCmdE(t, newStackSetBackupCmd(), nil, false, "app", "--file", writeSpec(t, body))
		if err == nil {
			t.Errorf("%s: want a refusal", name)
		}
		if _, declared := reloadStack(t, "app").BackupStrategies(); declared {
			t.Errorf("%s: a refused spec was stored", name)
		}
	}
}

func TestSetBackupEmptyListAndClear(t *testing.T) {
	configuredStackFixture(t)
	runCmd(t, newStackSetBackupCmd(), nil, true, "app", "--file", writeSpec(t, `[]`))
	got := reloadStack(t, "app")
	if list, declared := got.BackupStrategies(); !declared || len(list) != 0 || got.Backup == nil {
		t.Errorf("empty list: %+v declared=%v", list, declared)
	}
	runCmd(t, newStackSetBackupCmd(), nil, true, "app", "--clear")
	if _, declared := reloadStack(t, "app").BackupStrategies(); declared {
		t.Error("--clear must remove the declaration")
	}
}

// --auto refuses a stack that declared nothing, before dialling, and accepts an empty list.
func TestAutoRefusesUndeclaredBackupButAcceptsAnEmptyList(t *testing.T) {
	seedUpdateStack(t, fleet.Stack{Name: "s", Dir: "/x", AutoUpdate: true, HealthURL: "http://127.0.0.1:1/"})
	err := runStackUpdate("s", nil, true, false, false, 1, time.Millisecond)
	if !errors.Is(err, update.ErrNoBackup) {
		t.Fatalf("want the no-backup refusal, got %v", err)
	}

	empty := []backup.Strategy{}
	seedUpdateStack(t, fleet.Stack{Name: "s", Dir: "/x", AutoUpdate: true, HealthURL: "http://127.0.0.1:1/", Backup: &empty})
	err = runStackUpdate("s", nil, true, false, false, 1, time.Millisecond)
	if errors.Is(err, update.ErrNoBackup) {
		t.Fatalf("an explicit empty list must not be refused: %v", err)
	}
}

// A legacy stack and the list form built from it run the same script.
func TestLegacyAndMigratedStacksBackUpIdentically(t *testing.T) {
	legacy := &fleet.Stack{Name: "app", Dir: "/opt/app", EnvFile: ".env", DBContainer: "pg", DBName: "appdb", DBUser: "app"}
	list, _ := legacy.BackupStrategies()
	migrated := &fleet.Stack{Name: "app", Dir: "/opt/app", EnvFile: ".env", Backup: &list}

	a, err := backup.BackupScript(backupConfig(legacy))
	if err != nil {
		t.Fatal(err)
	}
	b, err := backup.BackupScript(backupConfig(migrated))
	if err != nil {
		t.Fatal(err)
	}
	if a != b || !strings.Contains(a, "pg_dump -U 'app' -Fc 'appdb' > \"$BK/db-$ts.dump\"") {
		t.Errorf("scripts differ:\n%s\n---\n%s", a, b)
	}
}

func TestStackAddFlagsEditTheListFormInPlace(t *testing.T) {
	configuredStackFixture(t)
	runCmd(t, newStackAddCmd(), nil, true, "app", "--vm", "vm1", "--path", "/opt/app", "--db-container", "pg", "--db-name", "one")
	runCmd(t, newStackAddCmd(), nil, true, "app", "--vm", "vm1", "--path", "/opt/app", "--db-name", "two", "--db-user", "u")
	list, _ := reloadStack(t, "app").BackupStrategies()
	if len(list) != 1 || list[0].Container != "pg" || list[0].Database != "two" || list[0].User != "u" {
		t.Errorf("got %+v", list)
	}
}

func TestSetUpdateNoDatabaseOnAListFormStackWritesTheEmptyList(t *testing.T) {
	configuredStackFixture(t)
	runCmd(t, newStackAddCmd(), nil, true, "app", "--vm", "vm1", "--path", "/opt/app", "--db-container", "pg", "--db-name", "one")
	runCmd(t, newStackSetUpdateCmd(), nil, true, "app", "--no-database")
	got := reloadStack(t, "app")
	if list, declared := got.BackupStrategies(); !declared || len(list) != 0 {
		t.Errorf("got %+v declared=%v", list, declared)
	}
}
