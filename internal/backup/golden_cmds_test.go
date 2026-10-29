// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package backup

import "testing"

// recorder captures the one script a backup function sends, and answers with out.
type recorder struct {
	cmds []string
	out  string
}

func (r *recorder) Run(cmd string) (string, error) {
	r.cmds = append(r.cmds, cmd)
	return r.out, nil
}

func (r *recorder) only(t *testing.T) string {
	t.Helper()
	if len(r.cmds) != 1 {
		t.Fatalf("want one remote command, got %d: %q", len(r.cmds), r.cmds)
	}
	return r.cmds[0]
}

func baseCfg() Config {
	return Config{
		Name:        "app",
		Dir:         "/opt/app",
		EnvFile:     ".env",
		DBContainer: "app-postgres-1",
		DBName:      "appdb",
	}
}

func sudoCfg() Config {
	c := baseCfg()
	c.Sudo = true
	c.BackupDir = "/srv/bk"
	c.Keep = 3
	c.DBUser = "app"
	return c
}

func noEnvCfg() Config {
	c := baseCfg()
	c.EnvFile = ""
	return c
}

func TestBackupScriptGolden(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{"backup_default", baseCfg()},
		{"backup_sudo_custom_dir", sudoCfg()},
		{"backup_no_env_file", noEnvCfg()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := &recorder{}
			if _, err := Backup(r, c.cfg); err != nil {
				t.Fatal(err)
			}
			golden(t, c.name, r.only(t))
		})
	}
}

func TestListScriptGolden(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{"list_default", baseCfg()},
		{"list_custom_dir", sudoCfg()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := &recorder{}
			if _, err := List(r, c.cfg); err != nil {
				t.Fatal(err)
			}
			golden(t, c.name, r.only(t))
		})
	}
}

func TestRestoreScriptGolden(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		file string
	}{
		{"restore_newest", baseCfg(), ""},
		{"restore_named_sudo", sudoCfg(), "db-1.dump"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := &recorder{}
			if _, err := Restore(r, c.cfg, c.file); err != nil {
				t.Fatal(err)
			}
			golden(t, c.name, r.only(t))
		})
	}
}

func TestContainerExistsCommandGolden(t *testing.T) {
	r := &recorder{out: "web\napp-postgres-1\n"}
	ok, err := ContainerExists(r, baseCfg())
	if err != nil || !ok {
		t.Fatalf("want the container found, got %v, %v", ok, err)
	}
	s := &recorder{out: "web\n"}
	ok, err = ContainerExists(s, sudoCfg())
	if err != nil || ok {
		t.Fatalf("want the container missing, got %v, %v", ok, err)
	}
	golden(t, "container_exists", r.only(t)+"\n"+s.only(t)+"\n")
}

// A refused env file must never reach the remote shell.
func TestBackupRefusesBadEnvFileBeforeRunning(t *testing.T) {
	for _, name := range []string{"/etc/passwd", "../x", "a b", ".env;rm -rf /"} {
		r := &recorder{}
		c := baseCfg()
		c.EnvFile = name
		if _, err := Backup(r, c); err == nil {
			t.Errorf("%q: want an error", name)
		}
		if len(r.cmds) != 0 {
			t.Errorf("%q: nothing may run, got %q", name, r.cmds)
		}
	}
}
