// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package remote

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/backup"
)

// DefaultConfigPath is where the VM keeps its allowlist.
const DefaultConfigPath = "/etc/baryovm/remote.json"

// Config is the VM's own allowlist. The client never sends any of it: it names a stack, and the
// stack's directory, services, health URL and backup settings come from here. That is what lets
// the wrapper work without the client's fleet, and what keeps a leaked CI key from reaching
// anything the file does not list.
type Config struct {
	Stacks map[string]StackConfig `json:"stacks"`
}

// StackConfig is one stack the wrapper may act on.
type StackConfig struct {
	Dir       string   `json:"dir"`
	File      string   `json:"file,omitempty"`
	Sudo      bool     `json:"sudo,omitempty"`
	Services  []string `json:"services,omitempty"`  // limits update and logs; empty means all
	HealthURL string   `json:"healthUrl,omitempty"` // required for update

	DBContainer string `json:"dbContainer,omitempty"`
	DBName      string `json:"dbName,omitempty"`
	DBUser      string `json:"dbUser,omitempty"`
	EnvFile     string `json:"envFile,omitempty"`
	BackupDir   string `json:"backupDir,omitempty"`
	Keep        int    `json:"keep,omitempty"`

	// Ops is which operations this stack allows. Empty allows none: the file fails closed.
	Ops []string `json:"ops"`
}

// Allows reports whether the stack permits op.
func (s StackConfig) Allows(op Op) bool {
	for _, o := range s.Ops {
		if o == string(op) {
			return true
		}
	}
	return false
}

// HasBackup reports whether the stack has a database to back up.
func (s StackConfig) HasBackup() bool { return s.DBContainer != "" && s.DBName != "" }

// Stack returns the entry for name.
func (c *Config) Stack(name string) (StackConfig, bool) {
	s, ok := c.Stacks[name]
	return s, ok
}

var identRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)

// ParseConfig reads and validates an allowlist. Unknown keys are an error rather than ignored: a
// typo in "ops" must not silently leave a stack with a different policy than the owner wrote.
func ParseConfig(b []byte) (*Config, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var c Config
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("parse remote config: %w", err)
	}
	if dec.More() {
		return nil, errors.New("parse remote config: unexpected data after the JSON document")
	}
	names := make([]string, 0, len(c.Stacks))
	for n := range c.Stacks {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if !ValidStackName(n) {
			return nil, fmt.Errorf("remote config: stack name %q is not allowed", n)
		}
		if err := c.Stacks[n].validate(); err != nil {
			return nil, fmt.Errorf("remote config: stack %q: %w", n, err)
		}
	}
	return &c, nil
}

func plainText(p string) bool {
	if p == "" {
		return false
	}
	for i := 0; i < len(p); i++ {
		if p[i] < 0x20 || p[i] == 0x7f {
			return false
		}
	}
	return true
}

func (s StackConfig) validate() error {
	if !plainText(s.Dir) || !path.IsAbs(s.Dir) {
		return errors.New("dir must be an absolute path")
	}
	if s.File != "" && !plainText(s.File) {
		return errors.New("file has a control character")
	}
	for _, v := range s.Services {
		if !identRE.MatchString(v) {
			return fmt.Errorf("service %q is not a plain name", v)
		}
	}
	if s.HealthURL != "" {
		if !plainText(s.HealthURL) || !strings.HasPrefix(s.HealthURL, "http://") && !strings.HasPrefix(s.HealthURL, "https://") {
			return errors.New("healthUrl must be an http or https URL")
		}
	}
	for label, v := range map[string]string{"dbContainer": s.DBContainer, "dbName": s.DBName, "dbUser": s.DBUser} {
		if v != "" && !identRE.MatchString(v) {
			return fmt.Errorf("%s %q is not a plain name", label, v)
		}
	}
	if err := backup.ValidateEnvFile(s.EnvFile); err != nil {
		return err
	}
	if s.BackupDir != "" && (!plainText(s.BackupDir) || !path.IsAbs(s.BackupDir)) {
		return errors.New("backupDir must be an absolute path")
	}
	if s.Keep < 0 {
		return errors.New("keep must not be negative")
	}
	seen := map[string]bool{}
	for _, o := range s.Ops {
		if !Op(o).Valid() {
			return fmt.Errorf("op %q is not in the v1 allowlist (%s)", o, opList())
		}
		if seen[o] {
			return fmt.Errorf("op %q is listed twice", o)
		}
		seen[o] = true
	}
	if s.Allows(OpUpdate) && s.HealthURL == "" {
		return errors.New("update needs a healthUrl: an update that cannot be verified cannot be rolled back")
	}
	if s.Allows(OpBackup) && !s.HasBackup() {
		return errors.New("backup needs dbContainer and dbName")
	}
	return nil
}

// FileInfo is what the permission check needs from a path.
type FileInfo struct {
	Mode fs.FileMode
	UID  uint32
}

// FS is the filesystem seam, so the permission rules are tested without being root.
type FS interface {
	Lstat(name string) (FileInfo, error)
	ReadFile(name string) ([]byte, error)
}

// CheckOwnership applies the rule to one path: a regular file (or a directory when wantDir), never
// a symlink, owned by root, not writable by group or others. A file the SSH user owns could be
// rewritten by that user, which would turn the allowlist into whatever the key holder wants.
func CheckOwnership(label string, fi FileInfo, wantDir bool) error {
	if fi.Mode&fs.ModeSymlink != 0 {
		return fmt.Errorf("refusing to run: %s is a symlink", label)
	}
	if wantDir && !fi.Mode.IsDir() || !wantDir && !fi.Mode.IsRegular() {
		return fmt.Errorf("refusing to run: %s is not the kind of path expected", label)
	}
	if fi.UID != 0 {
		return fmt.Errorf("refusing to run: %s is owned by uid %d, it must be owned by root", label, fi.UID)
	}
	if fi.Mode.Perm()&0o022 != 0 {
		return fmt.Errorf("refusing to run: %s is writable by group or others (mode %04o)", label, fi.Mode.Perm())
	}
	return nil
}

// LoadConfig checks the allowlist file and the directory that holds it, then parses it. Both are
// checked because a root-owned file in a directory the user can write is a file the user can
// replace.
func LoadConfig(fsys FS, p string) (*Config, error) {
	d := path.Dir(p)
	di, err := fsys.Lstat(d)
	if err != nil {
		return nil, fmt.Errorf("refusing to run: cannot read %s: %w", d, err)
	}
	if err := CheckOwnership(d, di, true); err != nil {
		return nil, err
	}
	fi, err := fsys.Lstat(p)
	if err != nil {
		return nil, fmt.Errorf("refusing to run: cannot read %s: %w", p, err)
	}
	if err := CheckOwnership(p, fi, false); err != nil {
		return nil, err
	}
	b, err := fsys.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("refusing to run: cannot read %s: %w", p, err)
	}
	return ParseConfig(b)
}
