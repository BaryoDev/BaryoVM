// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package backup

import (
	"strings"
	"testing"
)

func fourStrategies() []Strategy {
	return []Strategy{
		{Kind: KindPostgresContainer, Container: "db", Database: "app"},
		{Kind: KindPostgresRemote, Host: "mydb.example.com", Port: 5433, Database: "managed", User: "admin", EnvFile: ".env", PasswordVar: "DB_PASSWORD"},
		{Kind: KindCommand, Dump: "mongodump --archive && echo done", Restore: "mongorestore --archive"},
		{Kind: KindFiles, Paths: []string{"/srv/app/uploads", "/srv/app/my files"}},
	}
}

func multiCfg() Config {
	return Config{Name: "app", Dir: "/opt/app", EnvFile: ".env", Strategies: fourStrategies()}
}

func indexOf(t *testing.T, s, sub string) int {
	t.Helper()
	i := strings.Index(s, sub)
	if i < 0 {
		t.Fatalf("missing %q in:\n%s", sub, s)
	}
	return i
}

func TestMultiBackupRunsEveryStrategyInOrderUnderSetE(t *testing.T) {
	script, err := BackupScript(multiCfg())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(script, "set -e\n") {
		t.Errorf("the first failure must stop the script:\n%s", script)
	}
	pg := indexOf(t, script, "pg_dump -U 'postgres' -Fc 'app' > \"$BK/db-$ts.dump\"")
	remote := indexOf(t, script, "db1-$ts.dump")
	cmd := indexOf(t, script, "mongodump --archive && echo done")
	files := indexOf(t, script, "tar -czf - -C / 'srv/app/uploads' 'srv/app/my files' > \"$BK/files3-$ts.tar.gz\"")
	if pg >= remote || remote >= cmd || cmd >= files {
		t.Errorf("strategies out of order: %d %d %d %d", pg, remote, cmd, files)
	}
	if strings.Count(script, "ts=$(date") != 1 {
		t.Errorf("one timestamp for the whole set:\n%s", script)
	}
	if !strings.Contains(script, `echo "backup ok: db-$ts.dump (`) {
		t.Errorf("the backup ok line names the first artifact, which releasestate parses:\n%s", script)
	}
}

func TestPostgresRemoteNeverPutsThePasswordOnTheCommandLine(t *testing.T) {
	c := Config{Name: "app", Dir: "/opt/app", Sudo: true}
	s := fourStrategies()[1]
	dump, err := c.dumpCmd(s, `"$BK/db-$ts.dump"`)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"sudo -n docker run --rm -i --env-file '/opt/app/.env' 'postgres:17' sh -c ",
		`PGPASSWORD="${DB_PASSWORD}" exec pg_dump`,
		"sh 'mydb.example.com' '5433' 'admin' 'managed' > ",
	} {
		if !strings.Contains(dump, want) {
			t.Errorf("dump lacks %q:\n%s", want, dump)
		}
	}
	// The variable is expanded inside the container from its environment: no -e NAME=value, no
	// PGPASSWORD=literal outside the sh -c script.
	if strings.Contains(dump, "-e PGPASSWORD") {
		t.Errorf("password flag on the command line:\n%s", dump)
	}
	restore, err := c.restoreCmd(s, `"$BK/db-$ts.dump"`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(restore, "pg_restore -h") || !strings.Contains(restore, "--clean --if-exists --no-owner") || !strings.HasSuffix(restore, `< "$BK/db-$ts.dump"`) {
		t.Errorf("remote restore:\n%s", restore)
	}
}

func TestPostgresRemotePassFileIsMountedNotRead(t *testing.T) {
	c := Config{Name: "app", Dir: "/opt/app"}
	s := Strategy{Kind: KindPostgresRemote, Host: "h", Database: "d", PassFile: "/home/opc/.pgpass"}
	dump, err := c.dumpCmd(s, `"$BK/x"`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dump, "-v '/home/opc/.pgpass:/run/pgpass:ro' -e PGPASSFILE=/run/pgpass") || strings.Contains(dump, "--env-file") {
		t.Errorf("passfile dump:\n%s", dump)
	}
}

func TestCommandStrategyRunsThroughSudoShellWhole(t *testing.T) {
	s := Strategy{Kind: KindCommand, Dump: "a && b", Restore: "c && d"}
	dump, _ := Config{Sudo: true}.dumpCmd(s, "OUT")
	if want := `sudo -n "${SHELL:-/bin/sh}" -c 'a && b' > OUT`; dump != want {
		t.Errorf("sudo dump:\n got %s\nwant %s", dump, want)
	}
	restore, _ := Config{Sudo: true}.restoreCmd(s, "IN")
	if want := `sudo -n "${SHELL:-/bin/sh}" -c 'c && d' < IN`; restore != want {
		t.Errorf("sudo restore:\n got %s\nwant %s", restore, want)
	}
	plain, _ := Config{}.dumpCmd(s, "OUT")
	if !strings.HasPrefix(plain, "{ a && b\n}") {
		t.Errorf("without sudo the line is grouped so the redirect covers all of it: %q", plain)
	}
}

func TestFilesStrategyArchivesRelativePathsAndRestoresToRoot(t *testing.T) {
	c := Config{Sudo: true}
	s := Strategy{Kind: KindFiles, Paths: []string{"/srv/up"}}
	dump, _ := c.dumpCmd(s, "OUT")
	restore, _ := c.restoreCmd(s, "IN")
	if dump != "sudo -n tar -czf - -C / 'srv/up' > OUT" || restore != "sudo -n tar -xzf - -C / < IN" {
		t.Errorf("files:\n%s\n%s", dump, restore)
	}
}

func TestMultiRestoreChecksEveryArtifactBeforeReplacingAnything(t *testing.T) {
	script, err := RestoreScriptE(multiCfg(), "")
	if err != nil {
		t.Fatal(err)
	}
	lastCheck := strings.LastIndex(script, "backup not found: $BK/files3-$ts.tar.gz")
	firstRestore := indexOf(t, script, "dropdb")
	if lastCheck < 0 || lastCheck > firstRestore {
		t.Errorf("every artifact must be checked before the first restore:\n%s", script)
	}
	for _, want := range []string{"mongorestore --archive", "pg_restore -h", "tar -xzf - -C /", `ls -1t "$BK"/db-*.dump`} {
		indexOf(t, script, want)
	}
	named, _ := RestoreScriptE(multiCfg(), "db-20260101-010101.dump")
	indexOf(t, named, "f='db-20260101-010101.dump'")
}

func TestValidateRefusesStrategiesThatCannotRunSafely(t *testing.T) {
	cases := map[string]Strategy{
		"no kind":             {},
		"unknown kind":        {Kind: "snapshot"},
		"container no db":     {Kind: KindPostgresContainer, Container: "db"},
		"dash container":      {Kind: KindPostgresContainer, Container: "-x", Database: "d"},
		"remote no password":  {Kind: KindPostgresRemote, Host: "h", Database: "d"},
		"remote dash db":      {Kind: KindPostgresRemote, Host: "h", Database: "-d", EnvFile: ".env"},
		"remote bad host":     {Kind: KindPostgresRemote, Host: "h;id", Database: "d", EnvFile: ".env"},
		"remote bad envfile":  {Kind: KindPostgresRemote, Host: "h", Database: "d", EnvFile: "../x"},
		"remote bad var":      {Kind: KindPostgresRemote, Host: "h", Database: "d", EnvFile: ".env", PasswordVar: "A B"},
		"remote relative pf":  {Kind: KindPostgresRemote, Host: "h", Database: "d", PassFile: "pgpass"},
		"command no restore":  {Kind: KindCommand, Dump: "x"},
		"files relative":      {Kind: KindFiles, Paths: []string{"uploads"}},
		"files dotdot":        {Kind: KindFiles, Paths: []string{"/srv/../etc"}},
		"files none":          {Kind: KindFiles},
		"files root":          {Kind: KindFiles, Paths: []string{"/"}},
		"remote port too big": {Kind: KindPostgresRemote, Host: "h", Database: "d", EnvFile: ".env", Port: 70000},
	}
	for name, s := range cases {
		if err := s.Validate(); err == nil {
			t.Errorf("%s: want an error", name)
		}
		if _, err := BackupScript(Config{Strategies: []Strategy{s}}); err == nil {
			t.Errorf("%s: BackupScript must refuse it too", name)
		}
	}
	if err := ValidateStrategies(fourStrategies()); err != nil {
		t.Errorf("valid set refused: %v", err)
	}
}

type probe struct{ names string }

func (p probe) Run(cmd string) (string, error) { return p.names, nil }

func TestSkipMissingContainersLeavesOutOnlyTheMissingOnes(t *testing.T) {
	cfg := multiCfg()
	if skip := SkipMissingContainers(probe{names: "other\n"}, &cfg); skip {
		t.Fatal("a command and a files strategy still run, so the backup is not skipped")
	}
	if !cfg.Skip[0] || len(cfg.Skip) != 1 {
		t.Errorf("skip: %v", cfg.Skip)
	}
	script, err := BackupScript(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(script, "pg_dump -U") {
		t.Errorf("the missing container was still dumped:\n%s", script)
	}
	// The remote strategy keeps its index, so its name does not shift between runs.
	indexOf(t, script, "db1-$ts.dump")

	only := Config{Strategies: []Strategy{{Kind: KindPostgresContainer, Container: "db", Database: "app"}}}
	if !SkipMissingContainers(probe{}, &only) {
		t.Error("nothing left to run must skip the whole backup")
	}
	if _, err := BackupScript(only); err != ErrNothingToBackUp {
		t.Errorf("got %v", err)
	}
}

func TestListCoversEveryArtifactOfAStrategyList(t *testing.T) {
	script, _ := List(scriptOnly{}, multiCfg())
	for _, g := range []string{`"$BK"/db-*.dump`, `"$BK"/db1-*.dump`, `"$BK"/cmd2-*.out`, `"$BK"/files3-*.tar.gz`} {
		if !strings.Contains(script, g) {
			t.Errorf("listing lacks %s:\n%s", g, script)
		}
	}
}
