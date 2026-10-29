// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/backup"
	"github.com/BaryoDev/BaryoVM/internal/compose"
	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"github.com/BaryoDev/BaryoVM/internal/release"
	"github.com/BaryoDev/BaryoVM/internal/releasestate"
	"github.com/BaryoDev/BaryoVM/internal/rollback"
	"github.com/BaryoDev/BaryoVM/internal/sshx"
	"github.com/BaryoDev/BaryoVM/internal/ui"
)

// gitHead is the commit of the source being released, or "" when the local root is not a git
// checkout. A variable so tests do not depend on the machine they run on.
var gitHead = func(dir string) string {
	if dir == "" {
		return ""
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output() // #nosec G204 -- fixed git binary, dir is the stack's local root
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// relRun carries a release's progress record. Every step is written as started before it runs and
// as done or failed after, so a crash between the write and the step leaves "started", which reads
// as "this step may have run".
type relRun struct {
	sc     sshx.Runner
	loc    releasestate.Loc
	st     *releasestate.State
	resume bool
}

// step runs fn under name. On a resume, a step the earlier release finished is skipped.
func (r *relRun) step(name string, fn func() error) error {
	if r.resume && r.st.Done(name) {
		ui.Notef("%s: done by the earlier release, skipping", name)
		return nil
	}
	r.st.Mark(name, releasestate.StatusStarted, nil)
	if err := releasestate.Write(r.sc, r.loc, r.st); err != nil {
		return fmt.Errorf("cannot record release state at %s, so %s did not run: %w", r.loc.File, name, err)
	}
	if err := fn(); err != nil {
		r.st.Mark(name, releasestate.StatusFailed, err)
		_ = releasestate.Write(r.sc, r.loc, r.st) // best effort: the started mark already stands
		return err
	}
	r.st.Mark(name, releasestate.StatusDone, nil)
	if err := releasestate.Write(r.sc, r.loc, r.st); err != nil {
		return fmt.Errorf("%s ran but the release state at %s could not be updated: %w", name, r.loc.File, err)
	}
	return nil
}

// stateData is what a refusal or a failure reports about the record, for the JSON envelope.
func stateData(l releasestate.Loc, s *releasestate.State, name string) map[string]any {
	_, unsafe := s.Unsafe()
	d := map[string]any{
		"stateFile": l.File,
		"stack":     s.Stack,
		"started":   s.Started,
		"steps":     s.Steps,
		"resumable": !unsafe,
		"hint":      stateHint(name, s),
	}
	if s.Commit != "" {
		d["commit"] = s.Commit
	}
	if s.Failed != "" {
		d["failedStep"] = s.Failed
		d["failedError"] = releasestate.Redact(s.Error)
	}
	if s.Backup != "" {
		d["backup"] = s.Backup
	}
	if s.BackupFile != "" {
		d["backupFile"] = s.BackupFile
	}
	return d
}

// stateHint says what to do about a record, including the half-applied case.
func stateHint(name string, s *releasestate.State) string {
	if step, bad := s.Unsafe(); bad {
		h := fmt.Sprintf("the %s step may be half applied and cannot be resumed. ", step)
		h += fmt.Sprintf("`baryovm stack rollback %s` puts the previous images back. ", name)
		if s.BackupFile != "" {
			h += fmt.Sprintf("The pre-release backup is %s in %s: restore it with `baryovm stack restore %s --file %s --yes` only if the data needs it. ",
				s.BackupFile, s.Backup, name, s.BackupFile)
		}
		return h + fmt.Sprintf("Then drop the record with `baryovm stack release %s --clear`.", name)
	}
	return fmt.Sprintf("run `baryovm stack release %s --resume` to continue from the first step not done, or `--clear` to drop the record", name)
}

// refuseState is the refusal when a record is already there.
func refuseState(l releasestate.Loc, s *releasestate.State, name string) error {
	e := fmt.Errorf("%s. %s", s.Describe(), stateHint(name, s))
	ui.Emit(ui.Result{OK: false, Action: "stack release", Error: e.Error(), Data: stateData(l, s, name)})
	return e
}

// runReleaseClear drops the record and the lock. When the release it drops was half applied, the
// rollback record is protected first, so the next release cannot replace the last good images with
// what the broken one left running.
func runReleaseClear(vm *fleet.VM, l releasestate.Loc, bcfg backup.Config, name string) error {
	sc, err := dialRelease(vm)
	if err != nil {
		ui.Emit(ui.Result{OK: false, Action: "stack release", Error: err.Error()})
		return err
	}
	defer sc.Close()
	prev, err := releasestate.Read(sc, l)
	protected := false
	if err == nil && prev != nil {
		if _, bad := prev.Unsafe(); bad {
			if protected, err = rollback.SetProtected(sc, bcfg, true); err != nil {
				err = fmt.Errorf("the release record was not cleared because the rollback record could not be protected: %w; fix or delete %s in the stack's backup directory, then clear again", err, rollback.File)
			}
		}
	}
	if err == nil {
		err = releasestate.Remove(sc, l)
	}
	if err != nil {
		ui.Emit(ui.Result{OK: false, Action: "stack release", Error: err.Error()})
		return err
	}
	data := map[string]any{"stateFile": l.File, "cleared": prev != nil}
	msg := name + ": no release record to clear"
	if prev != nil {
		data = stateData(l, prev, name)
		data["cleared"] = true
		data["rollbackProtected"] = protected
		msg = name + ": release record cleared"
		if protected {
			msg += ", the rollback record is kept until a release finishes"
		}
	}
	ui.Emit(ui.Result{OK: true, Action: "stack release", Message: msg, Data: data})
	ui.Successf("%s", msg)
	return nil
}

// recordRollback saves the images the stack runs right now, next to the backups, before the
// release changes them. It never stops a release: a missing record only means no rollback target.
func recordRollback(c sshx.Runner, st *fleet.Stack, m *release.Manifest, name string) {
	imgs, err := compose.Images(c, releaseComposeStack(st, m), nil)
	kept := false
	if err == nil {
		kept, err = rollback.Save(c, releaseBackupConfig(st, m), rollback.FromCompose(name, "release", imgs))
	}
	if kept {
		ui.Notef("keeping the rollback record from before the half-applied release that was cleared")
	}
	if err != nil {
		ui.Notef("could not record the images to roll back to: %v", err)
	}
}
