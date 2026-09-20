// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package recipe

import (
	"errors"
	"strings"
	"testing"
)

func dump(t *testing.T, s Strategy, sudo bool) string {
	t.Helper()
	got, err := s.DumpCmd(0, sudo)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	return got
}

func TestPostgresDumpUsesCustomFormatAndTheDefaultUser(t *testing.T) {
	got := dump(t, Strategy{Kind: KindPostgresContainer, Container: "db", Database: "app"}, false)
	for _, want := range []string{"docker exec 'db'", "pg_dump", "-U 'postgres'", "-Fc", "'app'", "$BK/db-0-$ts.dump"} {
		if !strings.Contains(got, want) {
			t.Errorf("want %q in:\n%s", want, got)
		}
	}
}

func TestMySQLDumpIsConsistentWithoutLocking(t *testing.T) {
	got := dump(t, Strategy{Kind: KindMySQLContainer, Container: "db", Database: "wp"}, false)
	if !strings.Contains(got, "--single-transaction") {
		t.Error("a dump that locks the tables a live site is reading is not usable in production")
	}
	if !strings.Contains(got, "-u 'root'") {
		t.Errorf("mysql should default to root:\n%s", got)
	}
}

func TestMongoDumpWritesAnArchive(t *testing.T) {
	got := dump(t, Strategy{Kind: KindMongoContainer, Container: "mongo", Database: "app"}, false)
	for _, want := range []string{"mongodump", "--archive", "--gzip", "--db 'app'"} {
		if !strings.Contains(got, want) {
			t.Errorf("want %q in:\n%s", want, got)
		}
	}
}

// Absolute paths inside an archive are how a restore writes somewhere nobody expected.
func TestFilesDumpStripsLeadingSlashes(t *testing.T) {
	got := dump(t, Strategy{Kind: KindFiles, Paths: []string{"/srv/app/uploads", "/etc/app.conf"}}, false)
	if !strings.Contains(got, "-C /") {
		t.Errorf("want a tar rooted at /:\n%s", got)
	}
	if strings.Contains(got, "'/srv/app/uploads'") {
		t.Errorf("the leading slash should be stripped so the archive holds relative paths:\n%s", got)
	}
	if !strings.Contains(got, "'srv/app/uploads'") {
		t.Errorf("want the relative path:\n%s", got)
	}
}

func TestCommandKindRunsWhatTheRecipeGave(t *testing.T) {
	got := dump(t, Strategy{
		Kind:    KindCommand,
		Dump:    "sqlcmd -Q \"BACKUP DATABASE app TO DISK='/tmp/a.bak'\"",
		Restore: "sqlcmd -Q \"RESTORE DATABASE app\"",
	}, false)
	if !strings.Contains(got, "BACKUP DATABASE") {
		t.Errorf("the recipe's own command should survive:\n%s", got)
	}
	if !strings.Contains(got, "$BK/cmd-0-$ts.dump") {
		t.Errorf("it should still write into the backup dir:\n%s", got)
	}
}

// The injection boundary CLAUDE.md names. A container name arrives from a recipe published by
// somebody else, so it is quoted rather than trusted.
func TestEveryInterpolatedValueIsQuoted(t *testing.T) {
	nasty := "db'; rm -rf /; echo '"
	got := dump(t, Strategy{Kind: KindPostgresContainer, Container: nasty, Database: "app"}, false)
	if strings.Contains(got, "; rm -rf /; echo ") && !strings.Contains(got, `'\''`) {
		t.Errorf("a container name must not break out of its quoting:\n%s", got)
	}
	if !strings.Contains(got, `'\''`) {
		t.Errorf("want the escaped single quote sshx.Quote produces:\n%s", got)
	}
}

func TestDatabaseNameIsQuotedToo(t *testing.T) {
	got := dump(t, Strategy{Kind: KindPostgresContainer, Container: "db", Database: "app'; DROP DATABASE x; --"}, false)
	if !strings.Contains(got, `'\''`) {
		t.Errorf("want the database name quoted:\n%s", got)
	}
}

func TestFilePathsAreQuoted(t *testing.T) {
	got := dump(t, Strategy{Kind: KindFiles, Paths: []string{"/srv/a b/c'd"}}, false)
	if !strings.Contains(got, `'\''`) {
		t.Errorf("a path with a quote in it must stay one argument:\n%s", got)
	}
}

// A stack whose compose runs as root has containers only root can exec into.
func TestSudoReachesTheDockerCall(t *testing.T) {
	got := dump(t, Strategy{Kind: KindPostgresContainer, Container: "db", Database: "app"}, true)
	if !strings.HasPrefix(got, "sudo -n docker") {
		t.Errorf("want the docker call elevated:\n%s", got)
	}
}

// A bare sudo prefix covers only up to the first &&, which is the half-applied failure SudoShell
// exists for.
func TestSudoWrapsACompoundCommandWhole(t *testing.T) {
	got := dump(t, Strategy{
		Kind: KindCommand, Dump: "a && b", Restore: "c",
	}, true)
	if !strings.Contains(got, "-c ") {
		t.Errorf("a compound command must run under one root shell:\n%s", got)
	}
}

func TestRestoreUsesCleanIfExistsRatherThanDropping(t *testing.T) {
	s := Strategy{Kind: KindPostgresContainer, Container: "db", Database: "app"}
	got, err := s.RestoreCmd("db-0-20260920-010101.dump", false)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	for _, want := range []string{"pg_restore", "--clean", "--if-exists", "-i"} {
		if !strings.Contains(got, want) {
			t.Errorf("want %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "dropdb") {
		t.Error("dropping a database something is connected to fails after the dump has been read")
	}
}

func TestRestoreQuotesTheArtifactName(t *testing.T) {
	s := Strategy{Kind: KindPostgresContainer, Container: "db", Database: "app"}
	got, err := s.RestoreCmd("db-0-$(whoami).dump", false)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if strings.Contains(got, "$(whoami)") && !strings.Contains(got, "'db-0-$(whoami).dump'") {
		t.Errorf("the artifact name must be quoted:\n%s", got)
	}
}

// One timestamp for the whole run. Each strategy calling date would drift across a slow dump and
// produce a set nobody can match up afterwards.
func TestBackupScriptStampsEveryArtifactOnce(t *testing.T) {
	got, err := BackupScript([]Strategy{
		{Kind: KindPostgresContainer, Container: "db", Database: "app"},
		{Kind: KindFiles, Paths: []string{"/srv/uploads"}},
	}, "", "myapp", 14, false)
	if err != nil {
		t.Fatalf("script: %v", err)
	}
	if n := strings.Count(got, "ts=$(date"); n != 1 {
		t.Errorf("want exactly one timestamp, got %d:\n%s", n, got)
	}
	if !strings.Contains(got, "set -e") {
		t.Error("a second dump running after the first failed produces a half-consistent set")
	}
	if !strings.Contains(got, "db-0-$ts.dump") || !strings.Contains(got, "files-1-$ts.tar.gz") {
		t.Errorf("both artifacts should be written:\n%s", got)
	}
}

// N of each kind, not N in total, or the older kind is silently dropped.
func TestRetentionIsPerArtifactShape(t *testing.T) {
	got, err := BackupScript([]Strategy{
		{Kind: KindPostgresContainer, Container: "db", Database: "app"},
		{Kind: KindFiles, Paths: []string{"/srv/uploads"}},
	}, "", "myapp", 3, false)
	if err != nil {
		t.Fatalf("script: %v", err)
	}
	if !strings.Contains(got, `"$BK"/db-0-*`) || !strings.Contains(got, `"$BK"/files-1-*`) {
		t.Errorf("each artifact shape needs its own retention:\n%s", got)
	}
	if !strings.Contains(got, "tail -n +4") {
		t.Errorf("keep 3 means dropping from the fourth:\n%s", got)
	}
}

// A script that writes no file must not report "backup ok".
func TestBackupScriptWithNoStrategiesIsAnError(t *testing.T) {
	_, err := BackupScript(nil, "", "myapp", 14, false)
	if !errors.Is(err, ErrNoStrategies) {
		t.Fatalf("want ErrNoStrategies, got %v", err)
	}
}

func TestBackupDirIsQuotedWhenGiven(t *testing.T) {
	got, err := BackupScript([]Strategy{{Kind: KindFiles, Paths: []string{"/a"}}}, "/mnt/backups", "myapp", 14, false)
	if err != nil {
		t.Fatalf("script: %v", err)
	}
	if !strings.Contains(got, "BK='/mnt/backups'") {
		t.Errorf("want the dir quoted:\n%s", got)
	}
}

func TestBackupDirDefaultsUnderHome(t *testing.T) {
	got, err := BackupScript([]Strategy{{Kind: KindFiles, Paths: []string{"/a"}}}, "", "myapp", 14, false)
	if err != nil {
		t.Fatalf("script: %v", err)
	}
	if !strings.Contains(got, `BK="$HOME/myapp-backups"`) {
		t.Errorf("want the default:\n%s", got)
	}
}

func TestExplicitUserBeatsTheKindDefault(t *testing.T) {
	got := dump(t, Strategy{Kind: KindPostgresContainer, Container: "db", Database: "app", User: "appuser"}, false)
	if !strings.Contains(got, "-U 'appuser'") {
		t.Errorf("want the declared user:\n%s", got)
	}
}

func TestKeepBelowOneFallsBackToTheDefault(t *testing.T) {
	got, err := BackupScript([]Strategy{{Kind: KindFiles, Paths: []string{"/a"}}}, "", "myapp", 0, false)
	if err != nil {
		t.Fatalf("script: %v", err)
	}
	if !strings.Contains(got, "tail -n +15") {
		t.Errorf("keep 0 would delete every backup, so it falls back to 14:\n%s", got)
	}
}
