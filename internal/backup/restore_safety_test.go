// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package backup

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runRestore runs a restore script against a fake docker that logs every call. The fake fails
// `pg_restore -l` when BAD_DUMP is set and `pg_dump` when NO_DUMP is set.
func runRestore(t *testing.T, script string, env ...string) (log, out string, err error) {
	t.Helper()
	sh, lerr := exec.LookPath("sh")
	if lerr != nil {
		t.Skip("no sh on this machine")
	}
	dir := t.TempDir()
	bk := filepath.Join(dir, "my backups")
	if mkerr := os.MkdirAll(bk, 0o700); mkerr != nil {
		t.Fatal(mkerr)
	}
	for _, n := range []string{"db-20260101-010101.dump", "db1-20260101-010101.dump"} {
		if werr := os.WriteFile(filepath.Join(bk, n), []byte("PGDMP"), 0o600); werr != nil {
			t.Fatal(werr)
		}
	}
	bin := filepath.Join(dir, "bin")
	if mkerr := os.MkdirAll(bin, 0o755); mkerr != nil {
		t.Fatal(mkerr)
	}
	logPath := filepath.Join(dir, "docker.log")
	fake := "#!/bin/sh\necho \"$*\" >> \"" + logPath + "\"\n" +
		"case \"$*\" in\n" +
		"  *\"pg_restore -l\"*) [ -z \"$BAD_DUMP\" ] || exit 1 ;;\n" +
		"  *\"-tAc\"*) [ -z \"$NO_CHECK\" ] || exit 1; [ -n \"$NO_DB\" ] || echo 1 ;;\n" +
		"  *pg_dump*) [ -z \"$NO_DUMP\" ] || exit 1; echo DUMPDATA ;;\n" +
		"  *dropdb*) [ -z \"$NO_DROP\" ] || exit 1 ;;\n" +
		"esac\n" +
		"cat >/dev/null 2>&1 < /dev/null; exit 0\n"
	if werr := os.WriteFile(filepath.Join(bin, "docker"), []byte(fake), 0o755); werr != nil {
		t.Fatal(werr)
	}
	script = strings.Replace(script, `BK="$HOME/app-backups"`, "BK='"+bk+"'", 1)
	cmd := exec.Command(sh, "-c", script)
	cmd.Env = append([]string{"PATH=" + bin + ":/usr/bin:/bin", "HOME=" + dir}, env...)
	b, err := cmd.CombinedOutput()
	raw, _ := os.ReadFile(logPath)
	return string(raw), string(b), err
}

func singleCfg() Config {
	return Config{Name: "app", DBContainer: "pg", DBName: "appdb", DBUser: "postgres"}
}

func TestACorruptDumpAbortsBeforeDropdb(t *testing.T) {
	log, out, err := runRestore(t, RestoreScript(singleCfg(), ""), "BAD_DUMP=1")
	if err == nil {
		t.Fatalf("a corrupt dump must fail the restore:\n%s", out)
	}
	for _, bad := range []string{"dropdb", "createdb", "pg_dump"} {
		if strings.Contains(log, bad) {
			t.Errorf("%s ran after the dump failed verification:\n%s", bad, log)
		}
	}
	if !strings.Contains(out, "nothing was changed") {
		t.Errorf("the message should say nothing changed:\n%s", out)
	}
}

func TestTheSafetyDumpRunsBeforeDropdb(t *testing.T) {
	log, out, err := runRestore(t, RestoreScript(singleCfg(), ""))
	if err != nil {
		t.Fatalf("restore failed: %v\n%s\n%s", err, out, log)
	}
	dump := strings.Index(log, "pg_dump")
	drop := strings.Index(log, "dropdb")
	if dump < 0 || drop < 0 || dump > drop {
		t.Errorf("pg_dump must run before dropdb:\n%s", log)
	}
	if !strings.Contains(out, "safety dump of the current database:") || !strings.Contains(out, "pre-restore-") {
		t.Errorf("the safety dump path is not printed:\n%s", out)
	}
}

func TestAFailedSafetyDumpAbortsBeforeDropdb(t *testing.T) {
	log, _, err := runRestore(t, RestoreScript(singleCfg(), ""), "NO_DUMP=1")
	if err == nil || strings.Contains(log, "dropdb") {
		t.Errorf("no safety dump must mean no drop (err %v):\n%s", err, log)
	}
}

func TestAFailureAfterTheDropPrintsTheSafetyDumpAndRestoreCommand(t *testing.T) {
	_, out, err := runRestore(t, RestoreScript(singleCfg(), ""), "NO_DROP=1")
	if err == nil {
		t.Fatalf("expected a failure:\n%s", out)
	}
	for _, want := range []string{"restore failed", "Put it back with", "pg_restore -U 'postgres' -d 'appdb' < \"", "pre-restore-"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestASetWithACorruptSecondDumpRunsNothing(t *testing.T) {
	cfg := Config{Name: "app", Strategies: []Strategy{
		{Kind: KindPostgresContainer, Container: "pg", Database: "one"},
		{Kind: KindPostgresContainer, Container: "pg2", Database: "two"},
	}}
	script, err := RestoreScriptE(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	log, out, rerr := runRestore(t, script, "BAD_DUMP=1")
	if rerr == nil {
		t.Fatalf("a corrupt dump must fail the set:\n%s", out)
	}
	for _, bad := range []string{"dropdb", "createdb", "pg_dump"} {
		if strings.Contains(log, bad) {
			t.Errorf("%s ran although a dump was unreadable:\n%s", bad, log)
		}
	}
}

func TestAnErrorCheckingTheDatabaseStopsBeforeDropdb(t *testing.T) {
	log, out, err := runRestore(t, RestoreScript(singleCfg(), ""), "NO_CHECK=1")
	if err == nil || strings.Contains(log, "dropdb") || strings.Contains(log, "pg_dump") {
		t.Errorf("a failed existence check must stop the restore (err %v):\n%s\n%s", err, out, log)
	}
}

func TestAMissingDatabaseGetsNoSafetyDumpButStillRestores(t *testing.T) {
	log, out, err := runRestore(t, RestoreScript(singleCfg(), ""), "NO_DB=1")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if strings.Contains(log, "pg_dump") || !strings.Contains(log, "pg_restore -U") {
		t.Errorf("want a restore and no safety dump:\n%s", log)
	}
}

// set -e is ignored inside a subshell on the left of ||, so a failed dropdb used to fall through to
// pg_restore in a set restore.
func TestASetStopsWhenDropdbFailsAndRunsTheHandler(t *testing.T) {
	cfg := Config{Name: "app", Strategies: []Strategy{
		{Kind: KindPostgresContainer, Container: "pg", Database: "one"},
		{Kind: KindPostgresContainer, Container: "pg2", Database: "two"},
	}}
	script, err := RestoreScriptE(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	log, out, rerr := runRestore(t, script, "NO_DROP=1")
	if rerr == nil {
		t.Fatalf("expected failure:\n%s", out)
	}
	if strings.Contains(log, "createdb") || strings.Contains(log, "pg_restore -U") {
		t.Errorf("createdb or pg_restore ran after dropdb failed:\n%s", log)
	}
	if !strings.Contains(out, "restore failed") || !strings.Contains(out, "Put it back with") {
		t.Errorf("the handler did not run:\n%s", out)
	}
}
