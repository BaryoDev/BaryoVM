// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package remote

import (
	"errors"
	"io/fs"
	"strings"
	"testing"
)

const goodConfig = `{
  "stacks": {
    "playground": {
      "dir": "/opt/playground",
      "file": "docker-compose.yml",
      "sudo": true,
      "services": ["api"],
      "healthUrl": "http://127.0.0.1:8080/health",
      "dbContainer": "pg",
      "dbName": "app",
      "envFile": ".env",
      "backupDir": "/var/backups/app",
      "keep": 7,
      "ops": ["update", "backup", "status", "logs"]
    },
    "readonly": {"dir": "/opt/ro", "ops": ["status"]}
  }
}`

func TestParseConfigAcceptsAGoodFile(t *testing.T) {
	c, err := ParseConfig([]byte(goodConfig))
	if err != nil {
		t.Fatal(err)
	}
	pg, ok := c.Stack("playground")
	if !ok || !pg.Allows(OpUpdate) || !pg.Allows(OpLogs) || pg.Dir != "/opt/playground" || !pg.Sudo {
		t.Errorf("playground read wrong: %+v", pg)
	}
	ro, _ := c.Stack("readonly")
	if ro.Allows(OpUpdate) || !ro.Allows(OpStatus) {
		t.Errorf("readonly must allow only status: %+v", ro)
	}
	if _, ok := c.Stack("missing"); ok {
		t.Error("a stack not in the file must not exist")
	}
}

func TestAStackWithNoOpsAllowsNothing(t *testing.T) {
	c, err := ParseConfig([]byte(`{"stacks":{"app":{"dir":"/opt/app"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	s, _ := c.Stack("app")
	for _, op := range Ops {
		if s.Allows(op) {
			t.Errorf("%s allowed with no ops listed", op)
		}
	}
}

func TestParseConfigRefusesBadFiles(t *testing.T) {
	cases := map[string]string{
		"not json":           `stacks:`,
		"unknown key":        `{"stacks":{"app":{"dir":"/opt/app","opps":["update"]}}}`,
		"unknown top key":    `{"stacks":{},"admin":true}`,
		"trailing document":  `{"stacks":{}} {"stacks":{}}`,
		"bad stack name":     `{"stacks":{"My App":{"dir":"/opt/app"}}}`,
		"relative dir":       `{"stacks":{"app":{"dir":"opt/app"}}}`,
		"no dir":             `{"stacks":{"app":{}}}`,
		"newline in dir":     `{"stacks":{"app":{"dir":"/opt/app\nrm"}}}`,
		"unknown op":         `{"stacks":{"app":{"dir":"/opt/app","ops":["exec"]}}}`,
		"release op":         `{"stacks":{"app":{"dir":"/opt/app","ops":["release"]}}}`,
		"duplicate op":       `{"stacks":{"app":{"dir":"/opt/app","ops":["status","status"]}}}`,
		"update no health":   `{"stacks":{"app":{"dir":"/opt/app","ops":["update"]}}}`,
		"backup no db":       `{"stacks":{"app":{"dir":"/opt/app","ops":["backup"]}}}`,
		"service injection":  `{"stacks":{"app":{"dir":"/opt/app","services":["api;id"]}}}`,
		"container name":     `{"stacks":{"app":{"dir":"/opt/app","dbContainer":"a b","dbName":"x"}}}`,
		"env file escapes":   `{"stacks":{"app":{"dir":"/opt/app","envFile":"../.env"}}}`,
		"health url scheme":  `{"stacks":{"app":{"dir":"/opt/app","healthUrl":"file:///etc/passwd"}}}`,
		"relative backupDir": `{"stacks":{"app":{"dir":"/opt/app","backupDir":"b"}}}`,
		"negative keep":      `{"stacks":{"app":{"dir":"/opt/app","keep":-1}}}`,
		"unknown runtime":    `{"stacks":{"app":{"dir":"/opt/app","runtime":"nerdctl"}}}`,
		"runtime injection":  `{"stacks":{"app":{"dir":"/opt/app","runtime":"docker; id"}}}`,
	}
	for name, body := range cases {
		if _, err := ParseConfig([]byte(body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// fakeFS answers Lstat from a table, so ownership is tested without being root.
type fakeFS struct {
	info  map[string]FileInfo
	files map[string]string
	reads int
}

func (f *fakeFS) Lstat(name string) (FileInfo, error) {
	fi, ok := f.info[name]
	if !ok {
		return FileInfo{}, fs.ErrNotExist
	}
	return fi, nil
}

func (f *fakeFS) ReadFile(name string) ([]byte, error) {
	f.reads++
	s, ok := f.files[name]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return []byte(s), nil
}

const cfgPath = "/etc/baryovm/remote.json"

func goodFS() *fakeFS {
	return &fakeFS{
		info: map[string]FileInfo{
			"/etc/baryovm": {Mode: fs.ModeDir | 0o755, UID: 0},
			cfgPath:        {Mode: 0o644, UID: 0},
		},
		files: map[string]string{cfgPath: goodConfig},
	}
}

func TestLoadConfigAcceptsRootOwnedNonWritable(t *testing.T) {
	for _, mode := range []fs.FileMode{0o644, 0o640, 0o600, 0o444} {
		f := goodFS()
		f.info[cfgPath] = FileInfo{Mode: mode, UID: 0}
		if _, err := LoadConfig(f, cfgPath); err != nil {
			t.Errorf("mode %04o: %v", mode, err)
		}
	}
}

func TestLoadConfigRefusesUnsafePermissions(t *testing.T) {
	cases := map[string]func(*fakeFS){
		"file group writable": func(f *fakeFS) { f.info[cfgPath] = FileInfo{Mode: 0o664, UID: 0} },
		"file world writable": func(f *fakeFS) { f.info[cfgPath] = FileInfo{Mode: 0o666, UID: 0} },
		"file other writable": func(f *fakeFS) { f.info[cfgPath] = FileInfo{Mode: 0o602, UID: 0} },
		"file owned by user":  func(f *fakeFS) { f.info[cfgPath] = FileInfo{Mode: 0o644, UID: 1000} },
		"file owned by user and tight": func(f *fakeFS) {
			f.info[cfgPath] = FileInfo{Mode: 0o600, UID: 1000}
		},
		"file is a symlink": func(f *fakeFS) { f.info[cfgPath] = FileInfo{Mode: fs.ModeSymlink | 0o777, UID: 0} },
		"file is a dir":     func(f *fakeFS) { f.info[cfgPath] = FileInfo{Mode: fs.ModeDir | 0o755, UID: 0} },
		"dir group writable": func(f *fakeFS) {
			f.info["/etc/baryovm"] = FileInfo{Mode: fs.ModeDir | 0o775, UID: 0}
		},
		"dir world writable": func(f *fakeFS) {
			f.info["/etc/baryovm"] = FileInfo{Mode: fs.ModeDir | 0o777, UID: 0}
		},
		"dir owned by user": func(f *fakeFS) {
			f.info["/etc/baryovm"] = FileInfo{Mode: fs.ModeDir | 0o755, UID: 1000}
		},
		"dir is a symlink": func(f *fakeFS) {
			f.info["/etc/baryovm"] = FileInfo{Mode: fs.ModeSymlink | 0o777, UID: 0}
		},
		"file missing": func(f *fakeFS) { delete(f.info, cfgPath) },
		"dir missing":  func(f *fakeFS) { delete(f.info, "/etc/baryovm") },
	}
	for name, mutate := range cases {
		f := goodFS()
		mutate(f)
		_, err := LoadConfig(f, cfgPath)
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if !strings.Contains(err.Error(), "refusing to run") {
			t.Errorf("%s: error should say it is refusing: %v", name, err)
		}
		if f.reads != 0 {
			t.Errorf("%s: the file was read before the permission check passed", name)
		}
	}
}

func TestLoadConfigRefusesABadFileEvenWhenOwnedByRoot(t *testing.T) {
	f := goodFS()
	f.files[cfgPath] = `{"stacks":{"app":{"dir":"/opt/app","ops":["exec"]}}}`
	if _, err := LoadConfig(f, cfgPath); err == nil {
		t.Error("an op outside the allowlist must be refused at load")
	}
}

func TestCheckOwnershipNamesTheProblem(t *testing.T) {
	err := CheckOwnership("x", FileInfo{Mode: 0o666, UID: 0}, false)
	if err == nil || !strings.Contains(err.Error(), "writable by group or others") {
		t.Errorf("got %v", err)
	}
	err = CheckOwnership("x", FileInfo{Mode: 0o644, UID: 1000}, false)
	if err == nil || !strings.Contains(err.Error(), "owned by uid 1000") {
		t.Errorf("got %v", err)
	}
	if err := CheckOwnership("x", FileInfo{Mode: 0o644, UID: 0}, false); err != nil {
		t.Errorf("a root-owned 0644 file must pass: %v", err)
	}
	if errors.Is(err, nil) {
		t.Skip()
	}
}
