// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BaryoDev/BaryoVM/internal/backup"
	"github.com/BaryoDev/BaryoVM/internal/fleet"
)

// pullRunner plays a VM that just ran a backup: the script prints scriptOut, the directory listing
// is listing, and cat answers with the file named in the command.
type pullRunner struct {
	scriptOut string
	listing   string
	files     map[string]string
	seen      []string
}

func (p *pullRunner) Run(cmd string) (string, error) {
	p.seen = append(p.seen, cmd)
	switch {
	case strings.Contains(cmd, "set -e"):
		return p.scriptOut, nil
	case strings.Contains(cmd, "ls -1 \"$BK\""):
		return p.listing, nil
	case strings.Contains(cmd, "cat \"$BK\"/"):
		for n, body := range p.files {
			if strings.HasSuffix(cmd, "'"+n+"'") {
				return body, nil
			}
		}
	}
	return "", nil
}

func newPullRunner() *pullRunner {
	return &pullRunner{
		scriptOut: "backup ok: db-20260101-120000.dump (1.0K) in /home/opc/app-backups\n",
		listing:   "db-20260101-120000.dump\ndb-20260101-120000.dump.partial\nenv-20260101-120000\ndb-20251231-120000.dump\nenv-20251231-120000\n",
		files: map[string]string{
			"db-20260101-120000.dump": "DUMPBYTES",
			"env-20260101-120000":     "SECRET=1\n",
		},
	}
}

func TestBackupPullWritesOwnerOnlyFilesOfThisRunOnly(t *testing.T) {
	stackFixture(t)
	r := newPullRunner()
	stdout, _ := runCmd(t, newStackBackupCmd(), r, true, "app", "--pull")

	dir := filepath.Join(fleet.Dir(), "backups", "app")
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v, want 0700", info.Mode().Perm())
	}
	entries, _ := os.ReadDir(dir)
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
		fi, _ := e.Info()
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %v, want 0600", e.Name(), fi.Mode().Perm())
		}
	}
	want := "db-20260101-120000.dump env-20260101-120000"
	if strings.Join(got, " ") != want {
		t.Errorf("files = %v, want %s", got, want)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "db-20260101-120000.dump"))
	if string(b) != "DUMPBYTES" {
		t.Errorf("content = %q", b)
	}

	var env struct {
		OK   bool `json:"ok"`
		Data struct {
			Output string       `json:"output"`
			Pulled []PulledFile `json:"pulled"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("no envelope: %v (%q)", err, stdout)
	}
	if !env.OK || len(env.Data.Pulled) != 2 || env.Data.Pulled[0].Size != 9 ||
		env.Data.Pulled[0].Path != filepath.Join(dir, "db-20260101-120000.dump") {
		t.Errorf("envelope = %+v", env)
	}
}

func TestBackupPullNeverReadsAPartialFile(t *testing.T) {
	stackFixture(t)
	r := newPullRunner()
	runCmd(t, newStackBackupCmd(), r, true, "app", "--pull")
	for _, c := range r.seen {
		if strings.Contains(c, "set -e") {
			continue // the backup script itself writes .partial and renames it
		}
		if strings.Contains(c, ".partial") {
			t.Errorf("a .partial file was requested: %q", c)
		}
	}
	if _, err := os.Stat(filepath.Join(fleet.Dir(), "backups", "app", "db-20260101-120000.dump.partial")); err == nil {
		t.Error("a .partial file was written locally")
	}
}

func TestBackupPullRefusesGroupWritableDirBeforeBackingUp(t *testing.T) {
	stackFixture(t)
	dir := filepath.Join(fleet.Dir(), "backups", "app")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o770); err != nil {
		t.Fatal(err)
	}
	r := newPullRunner()
	_, _, err := runCmdE(t, newStackBackupCmd(), r, true, "app", "--pull")
	if err == nil || !strings.Contains(err.Error(), "writable by group or others") {
		t.Fatalf("err = %v", err)
	}
	if len(r.seen) != 0 {
		t.Errorf("ran remote commands: %q", r.seen)
	}
}

func TestBackupPullRefusedOnForcedVM(t *testing.T) {
	stackFixture(t)
	store, _ := fleet.Load()
	store.Find("vm1").Remote = fleet.RemoteForced
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	r := newPullRunner()
	_, _, err := runCmdE(t, newStackBackupCmd(), r, true, "app", "--pull")
	if err == nil || !strings.Contains(err.Error(), "forced") {
		t.Fatalf("err = %v", err)
	}
	if len(r.seen) != 0 {
		t.Errorf("ran remote commands: %q", r.seen)
	}
	if _, err := os.Stat(filepath.Join(fleet.Dir(), "backups")); err == nil {
		t.Error("a local directory was created")
	}
}

func TestSetFilesDropsPartialAndUnsafeNames(t *testing.T) {
	got := backup.SetFiles("a-20260101-120000.sql\na-20260101-120000.sql.partial\n../x-20260101-120000.sql\nenv-20260101-120000\n.h-20260101-120000\nb-20260101-120001.sql\n", "20260101-120000")
	if strings.Join(got, ",") != "a-20260101-120000.sql,env-20260101-120000" {
		t.Errorf("got %v", got)
	}
}

func TestSetFilesKeepsADoubleDotNameOutOfThePullSet(t *testing.T) {
	// "a..b" passes the name pattern and holds no slash, but a double dot is dropped all the same.
	got := backup.SetFiles("a..b-20260101-120000.sql\nok-20260101-120000.sql\n", "20260101-120000")
	if strings.Join(got, ",") != "ok-20260101-120000.sql" {
		t.Errorf("got %v", got)
	}
}

// A hand-edited fleet.json can hold a name that stackNameRe would have refused. ".." would resolve
// the pull directory to the fleet directory itself, and "a/b" to a nested one.
func TestBackupPullRefusesAHandEditedStackName(t *testing.T) {
	for _, bad := range []string{"..", "a/b", ".hidden"} {
		stackFixture(t)
		store, _ := fleet.Load()
		store.FindStack("app").Name = bad
		if err := store.Save(); err != nil {
			t.Fatal(err)
		}
		r := newPullRunner()
		_, _, err := runCmdE(t, newStackBackupCmd(), r, true, bad, "--pull")
		if err == nil || !strings.Contains(err.Error(), "not allowed") {
			t.Fatalf("%q: err = %v", bad, err)
		}
		if len(r.seen) != 0 {
			t.Errorf("%q: ran remote commands: %q", bad, r.seen)
		}
		if _, err := os.Stat(filepath.Join(fleet.Dir(), "backups")); err == nil {
			t.Errorf("%q: a local directory was created", bad)
		}
	}
}

func TestBackupPullRefusesDryRun(t *testing.T) {
	stackFixture(t)
	r := newPullRunner()
	_, _, err := runCmdE(t, newStackBackupCmd(), r, true, "app", "--pull", "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("err = %v", err)
	}
	if len(r.seen) != 0 {
		t.Errorf("ran remote commands: %q", r.seen)
	}
}
