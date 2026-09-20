// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package recipe

import (
	"strings"
	"testing"
)

func withBackup(t *testing.T, backup string) (*Recipe, error) {
	t.Helper()
	return Parse([]byte(`{"schema":1,"name":"x","files":[{"from":"a/","to":"/b"}],"backup":`+backup+`}`), "test.json")
}

// The point of the list: a stack with a database and an uploads directory has two strategies, which
// the single DBContainer/DBName pair could never express.
func TestSeveralStrategiesOnOneStack(t *testing.T) {
	r, err := withBackup(t, `[
	  {"kind":"postgres-container","container":"db","database":"app"},
	  {"kind":"files","paths":["/srv/app/uploads"]}
	]`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(r.Backup) != 2 {
		t.Fatalf("want 2 strategies, got %d", len(r.Backup))
	}
}

func TestMongoAndMySQLAreKnownKinds(t *testing.T) {
	for _, kind := range []string{"mongo-container", "mysql-container"} {
		if _, err := withBackup(t, `[{"kind":"`+kind+`","container":"c","database":"d"}]`); err != nil {
			t.Errorf("%s should be a known kind: %v", kind, err)
		}
	}
}

// The escape hatch. MSSQL, SQLite, a Redis snapshot: none of them should wait for a release.
func TestCommandKindCoversAnythingElse(t *testing.T) {
	r, err := withBackup(t, `[{
	  "kind":"command",
	  "dump":"sqlcmd -Q \"BACKUP DATABASE app TO DISK='/tmp/app.bak'\"",
	  "restore":"sqlcmd -Q \"RESTORE DATABASE app FROM DISK='/tmp/app.bak'\""
	}]`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Backup[0].Kind != KindCommand {
		t.Errorf("kind = %q", r.Backup[0].Kind)
	}
}

// A backup nobody can restore is hope, stated in a schema.
func TestCommandKindWithoutRestoreIsRefused(t *testing.T) {
	_, err := withBackup(t, `[{"kind":"command","dump":"pg_dump app"}]`)
	if err == nil {
		t.Fatal("a dump with no restore must be refused")
	}
	if !strings.Contains(err.Error(), "hope") {
		t.Errorf("the error should say why: %v", err)
	}
}

// An unknown kind is refused rather than skipped: a strategy that does not run is a backup that
// does not exist, and a restore is the worst moment to find out.
func TestUnknownKindIsRefusedAndNamesTheHatch(t *testing.T) {
	_, err := withBackup(t, `[{"kind":"cassandra-container","container":"c","database":"d"}]`)
	if err == nil {
		t.Fatal("an unknown kind must be refused")
	}
	if !strings.Contains(err.Error(), "command") {
		t.Error("the error should point at the command kind rather than leaving the user stuck")
	}
}

func TestEmptyBackupListIsLegitimate(t *testing.T) {
	r, err := withBackup(t, `[]`)
	if err != nil {
		t.Fatalf("an explicit empty list is how a stack says it has nothing to back up: %v", err)
	}
	if r.HasBackup() {
		t.Error("an empty list means no backup")
	}
}

func TestContainerKindNeedsContainerAndDatabase(t *testing.T) {
	if _, err := withBackup(t, `[{"kind":"postgres-container","database":"app"}]`); err == nil {
		t.Error("no container must be refused")
	}
	if _, err := withBackup(t, `[{"kind":"postgres-container","container":"db"}]`); err == nil {
		t.Error("no database must be refused")
	}
}

func TestFilesKindNeedsAbsolutePaths(t *testing.T) {
	if _, err := withBackup(t, `[{"kind":"files","paths":["srv/uploads"]}]`); err == nil {
		t.Error("a relative path on the VM must be refused")
	}
	if _, err := withBackup(t, `[{"kind":"files","paths":[]}]`); err == nil {
		t.Error("kind files with no paths backs up nothing")
	}
}

func TestDefaultUserPerKind(t *testing.T) {
	if got := KindPostgresContainer.DefaultUser(); got != "postgres" {
		t.Errorf("postgres default user = %q", got)
	}
	if got := KindMySQLContainer.DefaultUser(); got != "root" {
		t.Errorf("mysql default user = %q", got)
	}
}
