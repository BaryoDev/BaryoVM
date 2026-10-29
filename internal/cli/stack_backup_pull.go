// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/backup"
	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"github.com/BaryoDev/BaryoVM/internal/sshx"
	"github.com/BaryoDev/BaryoVM/internal/ui"
)

// PulledFile is one backup file copied to the operator's machine.
type PulledFile struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// pullDir is where a stack's pulled backups go: <fleet dir>/backups/<stack>.
func pullDir(stack string) string {
	return filepath.Join(fleet.Dir(), "backups", stack)
}

// preparePullDir creates dir owner-only, or accepts an existing one that nobody else can write to.
// Backups hold the database and a copy of the stack's config, so a directory others can write into
// is refused rather than tightened behind the operator's back.
func preparePullDir(dir string) error {
	if filepath.Base(dir) == "" || filepath.Base(dir) == "." || filepath.Base(dir) == ".." {
		return fmt.Errorf("bad pull directory %q", dir)
	}
	info, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
			return err
		}
		if err := os.Mkdir(dir, 0o700); err != nil {
			return err
		}
		return os.Chmod(dir, 0o700)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s exists and is not a directory", dir)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s is writable by group or others (mode %04o): run `chmod 700 %s` and retry", dir, info.Mode().Perm(), dir)
	}
	return nil
}

// writePulled writes data to dir/name as an owner-only file, through a temp file and a rename so a
// cut connection leaves no half file under a real name.
func writePulled(dir, name string, data []byte) (PulledFile, error) {
	final := filepath.Join(dir, name)
	tmp := filepath.Join(dir, ".pull-"+name+".tmp")
	_ = os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- dir is the operator's own backup dir, name passed backup.SetFiles
	if err != nil {
		return PulledFile{}, err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return PulledFile{}, err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return PulledFile{}, err
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return PulledFile{}, err
	}
	return PulledFile{Path: final, Size: int64(len(data))}, nil
}

// pullBackupSet copies the files of the set the backup that printed out just made.
func pullBackupSet(c sshx.Runner, cfg backup.Config, out, dir string) ([]PulledFile, error) {
	ts, err := backup.SetTimestamp(out)
	if err != nil {
		return nil, err
	}
	listing, err := c.Run(cfg.ListDirCmd())
	if err != nil {
		return nil, err
	}
	names := backup.SetFiles(listing, ts)
	if len(names) == 0 {
		return nil, fmt.Errorf("no files with timestamp %s found in %s", ts, cfg.DirNote())
	}
	var pulled []PulledFile
	for _, n := range names {
		data, err := c.Run(cfg.ReadSetFileCmd(n))
		if err != nil {
			return pulled, fmt.Errorf("pull %s: %w", n, err)
		}
		p, err := writePulled(dir, n, []byte(data))
		if err != nil {
			return pulled, err
		}
		pulled = append(pulled, p)
	}
	return pulled, nil
}

// runStackBackupPull is `stack backup --pull`: take the backup, then copy its files home.
func runStackBackupPull(name string) error {
	const action = "stack backup"
	fail := func(err error) error {
		ui.Emit(ui.Result{OK: false, Action: action, Message: name, Error: err.Error()})
		return err
	}
	store, err := fleet.Load()
	if err != nil {
		return err
	}
	st := store.FindStack(name)
	if st == nil {
		return fmt.Errorf("no stack named %q", name)
	}
	vm := store.Find(st.VM)
	if vm == nil {
		return fmt.Errorf("stack %q references unknown VM %q", name, st.VM)
	}
	if err := requireLinux(vm, action); err != nil {
		return err
	}
	if vm.Forced() {
		return fail(fmt.Errorf("--pull does not work on VM %q: it is marked remote=forced and its key cannot read files. Use a key without a forced command, or pull the backups another way", vm.Name))
	}
	if !hasBackupStrategies(st) {
		return fail(noBackupConfigError(st))
	}
	dir := pullDir(name)
	if strings.TrimSpace(name) == "" || filepath.Base(name) != name {
		return fail(fmt.Errorf("stack name %q cannot be used as a directory name", name))
	}
	if err := preparePullDir(dir); err != nil {
		return fail(err)
	}
	var o opOutput
	err = ui.Step(fmt.Sprintf("backing up %s and pulling it to %s", name, dir), func() error {
		var e error
		o, e = runOnVM(vm, func(c sshx.Runner) (opOutput, error) {
			cfg := backupConfig(st)
			out, err := backup.Backup(c, cfg)
			if err != nil {
				return opOutput{}, err
			}
			pulled, err := pullBackupSet(c, cfg, out, dir)
			if err != nil {
				return opOutput{}, fmt.Errorf("the backup was taken and is on the VM (%s), but the pull failed: %w", cfg.DirNote(), err)
			}
			var b strings.Builder
			b.WriteString(strings.TrimRight(out, "\n"))
			for _, p := range pulled {
				fmt.Fprintf(&b, "\npulled: %s (%d bytes)", p.Path, p.Size)
			}
			return opOutput{text: b.String(), data: map[string]any{"output": out, "dir": dir, "pulled": pulled}}, nil
		})
		return e
	})
	if err != nil {
		return fail(err)
	}
	emitOp(name, action, o)
	return nil
}
