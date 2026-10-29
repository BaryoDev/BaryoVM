// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package releasestate keeps a release's progress in a plain JSON file on the host, so a release
// that dies halfway leaves a record of what it did and did not touch. The file is the record and
// the guard: while it exists, another release refuses. It never prints; the caller decides what to
// say.
package releasestate

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/BaryoDev/BaryoVM/internal/sshx"
)

// Step statuses. "started" is written before a step runs and is never cleared by a crash, so a state
// that says started means the step may have run.
const (
	StatusStarted = "started"
	StatusDone    = "done"
	StatusFailed  = "failed"
)

// ErrBusy means another release holds the start lock on this host.
var ErrBusy = errors.New("another release is starting on this host")

// Step is one stage of a release.
type Step struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// State is the file's content.
type State struct {
	Stack      string `json:"stack"`
	Commit     string `json:"commit,omitempty"`
	Started    string `json:"started"`
	Updated    string `json:"updated,omitempty"`
	Backup     string `json:"backup,omitempty"`
	BackupFile string `json:"backupFile,omitempty"`
	Steps      []Step `json:"steps"`
	Failed     string `json:"failed,omitempty"`
	Error      string `json:"error,omitempty"`
}

// New is a fresh state for a release of stack at commit (which may be empty).
func New(stack, commit string) *State {
	return &State{Stack: stack, Commit: commit, Started: time.Now().UTC().Format(time.RFC3339), Steps: []Step{}}
}

var (
	secretAssign = regexp.MustCompile(`(?i)\b([A-Za-z0-9_]*(?:PASS|SECRET|TOKEN|KEY)[A-Za-z0-9_]*)=\S+`)
	urlPassword  = regexp.MustCompile(`(://)[^/\s:@]+:[^/\s@]+@`)
)

// Redact strips what a failed command's output tends to carry: KEY=value pairs whose name holds
// PASS, SECRET, TOKEN or KEY, and user:password@ in URLs. The record sits in plain text on the host
// and in command output, so it must not hold anything a build step echoed. It is pattern based, not
// a guarantee.
func Redact(s string) string {
	s = secretAssign.ReplaceAllString(s, "$1=[redacted]")
	return urlPassword.ReplaceAllString(s, "${1}[redacted]@")
}

// Mark records a step's status, adding the step when it is new.
func (s *State) Mark(name, status string, err error) {
	msg := ""
	if err != nil {
		msg = Redact(err.Error())
	}
	for i := range s.Steps {
		if s.Steps[i].Name == name {
			s.Steps[i].Status, s.Steps[i].Error = status, msg
			s.refresh()
			return
		}
	}
	s.Steps = append(s.Steps, Step{Name: name, Status: status, Error: msg})
	s.refresh()
}

func (s *State) refresh() {
	s.Failed, s.Error = "", ""
	for _, st := range s.Steps {
		if st.Status == StatusFailed {
			s.Failed, s.Error = st.Name, st.Error
			return
		}
	}
}

// Done reports whether a step finished.
func (s *State) Done(name string) bool {
	for _, st := range s.Steps {
		if st.Name == name {
			return st.Status == StatusDone
		}
	}
	return false
}

// nonResumable steps change the running stack in a way a second run cannot be trusted to finish:
// compose up and the vhost swap. Everything else is safe to run again.
func nonResumable(name string) bool { return name == "deploy" || name == "tls" }

// Unsafe is the first step that is not done and cannot be resumed, if any.
func (s *State) Unsafe() (string, bool) {
	for _, st := range s.Steps {
		if st.Status != StatusDone && nonResumable(st.Name) {
			return st.Name, true
		}
	}
	return "", false
}

// Touched lists the steps that ran or may have run, with their status.
func (s *State) Touched() []string {
	var out []string
	for _, st := range s.Steps {
		out = append(out, st.Name+" ("+st.Status+")")
	}
	return out
}

// Describe is the refusal text: what the earlier release did, and what it may have touched.
func (s *State) Describe() string {
	var b strings.Builder
	fmt.Fprintf(&b, "an earlier release of %s began %s", s.Stack, s.Started)
	if s.Commit != "" {
		fmt.Fprintf(&b, " at commit %s", s.Commit)
	}
	b.WriteString(". ")
	if len(s.Steps) == 0 {
		b.WriteString("It had not started a step. ")
	} else {
		b.WriteString("Steps: " + strings.Join(s.Touched(), ", ") + ". ")
	}
	if s.Failed != "" {
		fmt.Fprintf(&b, "It failed at %s: %s. ", s.Failed, Redact(s.Error))
	} else if n := lastStarted(s); n != "" {
		fmt.Fprintf(&b, "It stopped during %s, which may have run. ", n)
	}
	return strings.TrimSpace(b.String())
}

func lastStarted(s *State) string {
	for i := len(s.Steps) - 1; i >= 0; i-- {
		if s.Steps[i].Status == StatusStarted {
			return s.Steps[i].Name
		}
	}
	return ""
}

// Loc is where the state lives on the host and whether root is needed to touch it.
type Loc struct {
	File string
	Sudo bool
}

// For places the state file next to the release root, outside the synced tree so a sync --delete
// cannot take it.
func For(remoteRoot, stack string, sudo bool) Loc {
	root := strings.TrimRight(strings.TrimSpace(remoteRoot), "/")
	// No shared fallback such as /var/tmp: with sudo, a fixed name in a world-writable directory
	// writes through whatever symlink another local user left there.
	dir := path.Dir(root)
	if root == "" {
		dir = "/"
	}
	return Loc{File: path.Join(dir, ".baryovm-release-"+stack+".json"), Sudo: sudo}
}

// Lock is the directory whose atomic creation serialises two releases starting together.
func (l Loc) Lock() string { return l.File + ".lock" }

// DeployedFile records the commit of the last finished release.
func (l Loc) DeployedFile() string { return strings.TrimSuffix(l.File, ".json") + ".deployed.json" }

func (l Loc) sh(cmd string) string { return sshx.SudoShell(l.Sudo, cmd) }

// LockCmd takes the start lock. It prints "locked" or "busy".
func (l Loc) LockCmd() string {
	dir := sshx.Quote(path.Dir(l.File))
	lock := sshx.Quote(l.Lock())
	return l.sh("mkdir -p " + dir + " && { mkdir " + lock + " 2>/dev/null && echo locked || echo busy; }")
}

// UnlockCmd drops the start lock.
func (l Loc) UnlockCmd() string { return l.sh("rmdir " + sshx.Quote(l.Lock())) }

// ReadCmd prints the state file, or nothing when there is none.
func (l Loc) ReadCmd() string {
	f := sshx.Quote(l.File)
	return l.sh("if [ -f " + f + " ]; then cat " + f + "; fi")
}

// WriteCmd replaces file with data in one rename, so a reader never sees half a file. The base64
// is in transit only; the file on the host is plain JSON.
func WriteCmd(l Loc, file string, data []byte) string {
	f := sshx.Quote(file)
	tmp := sshx.Quote(path.Join(path.Dir(file), ".baryovm-release.XXXXXX"))
	b64 := base64.StdEncoding.EncodeToString(data)
	// The trap removes the temp file when the decode or the rename fails; after a good mv it is gone
	// already and rm -f finds nothing.
	return l.sh("t=$(mktemp " + tmp + ") && { trap 'rm -f \"$t\"' EXIT; printf %s " + sshx.Quote(b64) + " | base64 -d > \"$t\" && mv \"$t\" " + f + " ; }")
}

// RemoveCmd deletes the state file and the lock. A failed write cleans up its own temp file.
func (l Loc) RemoveCmd() string {
	return l.sh("rm -f " + sshx.Quote(l.File) + "; rmdir " + sshx.Quote(l.Lock()) + " 2>/dev/null; true")
}

// Acquire takes the start lock. ErrBusy when another release has it.
func Acquire(r sshx.Runner, l Loc) error {
	out, err := r.Run(l.LockCmd())
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) == "busy" {
		return fmt.Errorf("%w: lock %s is held (remove it with `stack release --clear` if no release is running)", ErrBusy, l.Lock())
	}
	return nil
}

// Unlock drops the start lock.
func Unlock(r sshx.Runner, l Loc) error {
	_, err := r.Run(l.UnlockCmd())
	return err
}

// Read returns the state on the host, or nil when there is none.
func Read(r sshx.Runner, l Loc) (*State, error) {
	out, err := r.Run(l.ReadCmd())
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(out) == "" {
		return nil, nil
	}
	var s State
	if err := json.Unmarshal([]byte(out), &s); err != nil {
		return nil, fmt.Errorf("release state %s is not valid JSON (inspect it, or remove it with `stack release --clear`): %w", l.File, err)
	}
	return &s, nil
}

// ActiveWindow is how long after its last write a release with no failed step still counts as
// running. Every step writes before and after it runs, so a live release leaves a fresh stamp unless
// one step runs longer than this (a very slow image build); that release can look idle and be
// resumed over, which is why the refusal text names --clear as the way past it.
const ActiveWindow = 10 * time.Minute

var clock = time.Now

// Active reports whether the record looks like a release still running: stamped inside the window
// and no step failed. A failed step means the release stopped itself. A record with no stamp
// predates the field and counts as stopped.
func (s *State) Active(now time.Time) bool {
	if s.Failed != "" {
		return false
	}
	at, err := time.Parse(time.RFC3339, s.Updated)
	if err != nil {
		return false
	}
	return now.Sub(at) < ActiveWindow
}

// Write stores the state, stamping the heartbeat that lets --resume tell a live release from a dead one.
func Write(r sshx.Runner, l Loc, s *State) error {
	s.Updated = clock().UTC().Format(time.RFC3339)
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	_, err = r.Run(WriteCmd(l, l.File, append(b, '\n')))
	return err
}

// Remove deletes the state and the lock.
func Remove(r sshx.Runner, l Loc) error {
	_, err := r.Run(l.RemoveCmd())
	return err
}

// Begin starts a release under the host lock, so two releases starting together cannot both read
// "nothing in progress". With no state on the host it writes fresh and returns it as cur. With one
// it returns that as existing and writes nothing. The lock is held only across the read and the
// write; the state file is what guards the rest of the release.
func Begin(r sshx.Runner, l Loc, fresh *State) (cur, existing *State, err error) {
	if err := Acquire(r, l); err != nil {
		return nil, nil, err
	}
	defer Unlock(r, l) //nolint:errcheck // a stale lock is cleared by --clear
	prev, err := Read(r, l)
	if err != nil {
		return nil, nil, err
	}
	if prev != nil {
		return nil, prev, nil
	}
	if err := Write(r, l, fresh); err != nil {
		return nil, nil, fmt.Errorf("cannot record release state at %s, so nothing was changed: %w", l.File, err)
	}
	return fresh, nil, nil
}

// Open reads the state under the lock for a resume. Nil when there is none.
func Open(r sshx.Runner, l Loc) (*State, error) {
	if err := Acquire(r, l); err != nil {
		return nil, err
	}
	defer Unlock(r, l) //nolint:errcheck
	return Read(r, l)
}

// ParseBackupFile pulls the dump file name out of backup.Backup's "backup ok: <file> (...)" line.
func ParseBackupFile(out string) string {
	const marker = "backup ok: "
	i := strings.Index(out, marker)
	if i < 0 {
		return ""
	}
	f := strings.Fields(out[i+len(marker):])
	if len(f) == 0 {
		return ""
	}
	return f[0]
}

// Deployed is the record of the last finished release.
type Deployed struct {
	Stack  string `json:"stack"`
	Commit string `json:"commit"`
	At     string `json:"at"`
}

// RecordDeployed writes the commit of a finished release.
func RecordDeployed(r sshx.Runner, l Loc, stack, commit string) error {
	b, err := json.MarshalIndent(Deployed{Stack: stack, Commit: commit, At: time.Now().UTC().Format(time.RFC3339)}, "", "  ")
	if err != nil {
		return err
	}
	_, err = r.Run(WriteCmd(l, l.DeployedFile(), append(b, '\n')))
	return err
}
