// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package repomove

import (
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/sshx"
)

// Remote is one VM. RunStdin carries the read-only credential, which must never be part of a
// command line.
type Remote interface {
	Run(cmd string) (string, error)
	RunStdin(cmd string, stdin io.Reader) (string, error)
}

// VMTarget is a registered VM to search for checkouts. Sudo is the stack setting: a checkout under
// a root-owned directory is only readable with it.
type VMTarget struct {
	Name  string
	User  string // the SSH user: with root, the only checkout owner accepted
	Sudo  bool
	Roots []string // directories to search for checkouts
}

// Checkout is a clone of the repository found on a VM.
type Checkout struct {
	VM         string `json:"vm"`
	Dir        string `json:"dir"`
	Origin     string `json:"origin"`
	Dirty      bool   `json:"dirty,omitempty"`      // local changes: reported and left alone
	Repointed  bool   `json:"repointed,omitempty"`  // origin now points at the new remote
	AlreadyNew bool   `json:"alreadyNew,omitempty"` // origin already pointed at the new remote
	CredStored bool   `json:"credStored,omitempty"`
	Skipped    string `json:"skipped,omitempty"`
	Owner      string `json:"owner,omitempty"`
}

// Reference is a crontab line or systemd unit line that mentions a checkout. Listed, never edited:
// the user repoints these to the deployed copy.
type Reference struct {
	VM       string `json:"vm"`
	Checkout string `json:"checkout"`
	Match    string `json:"match"`
}

// gitEnv and gitOpts harden every git call on a VM. A checkout may belong to another user and carry
// a hostile .git/config, so the fsmonitor and hooks settings that would run code are blanked and the
// system config is ignored. There is no safe.directory override: a checkout is run as its owner
// (see asOwner), never as root, and only after OwnerCmd has passed.
const gitEnv = "env GIT_CONFIG_NOSYSTEM=1 "
const gitOpts = "git -c core.fsmonitor= -c core.hooksPath=/dev/null "

func gitC(dir string) string {
	return gitOpts + "-C " + sshx.Quote(dir)
}

var ownerRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9._-]*$`)

// safeDirRE is the only directory shape the credential step accepts, as a second guard beside the
// quoting.
var safeDirRE = regexp.MustCompile(`^/[A-Za-z0-9._/-]*$`)

// asOwner runs cmd as the checkout owner when the stack uses sudo, never as root, so a checkout's
// own config cannot run code with more rights than its owner has.
func asOwner(sudo bool, owner, cmd string) string {
	if !sudo {
		return gitEnv + cmd
	}
	return "sudo -n -u " + sshx.Quote(owner) + " " + gitEnv + cmd
}

// OwnerCmd prints the owner of the checkout directory and of its .git, one per line.
func OwnerCmd(sudo bool, dir string) string {
	return sshx.Sudo(sudo, "stat -c %U -- "+sshx.Quote(dir)+" "+sshx.Quote(dir+"/.git"))
}

// ParseOwner returns the owner when the directory and its .git have the same valid one, else an
// error that says why the checkout is refused. No git command runs before this passes.
func ParseOwner(out string) (string, error) {
	f := strings.Fields(out)
	if len(f) != 2 || f[0] != f[1] || !ownerRE.MatchString(f[0]) {
		return "", fmt.Errorf("the checkout and its .git are not owned by one user (%s)", strings.Join(f, ", "))
	}
	return f[0], nil
}

// FindCmd lists the .git entries under the roots, to a depth that covers /opt/app/repo/.git.
func FindCmd(sudo bool, roots []string) string {
	q := make([]string, len(roots))
	for i, r := range roots {
		q[i] = sshx.Quote(r)
	}
	return sshx.Sudo(sudo, "find "+strings.Join(q, " ")+" -maxdepth 5 -name .git -prune -print0 2>/dev/null; true")
}

// ParseFinds turns find -print0 output into checkout directories. A name with a control character
// (a newline included) or a .. element is dropped, since the later prefix check is lexical.
func ParseFinds(out string) []string {
	var dirs []string
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\x00") {
		if !strings.HasSuffix(line, "/.git") || !safePath(line) {
			continue
		}
		d := strings.TrimSuffix(line, "/.git")
		if d != "" && !seen[d] {
			seen[d] = true
			dirs = append(dirs, d)
		}
	}
	return dirs
}

// safePath accepts an absolute, already clean path with no control characters and no .. element.
func safePath(p string) bool {
	if !strings.HasPrefix(p, "/") || path.Clean(p) != p {
		return false
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	for _, el := range strings.Split(p, "/") {
		if el == ".." {
			return false
		}
	}
	return true
}

// GetURLCmd reads the origin URL of a checkout.
func GetURLCmd(sudo bool, owner, dir string) string {
	return asOwner(sudo, owner, gitC(dir)+" remote get-url origin")
}

// StatusCmd lists local changes; empty output means a clean checkout.
func StatusCmd(sudo bool, owner, dir string) string {
	return asOwner(sudo, owner, gitC(dir)+" status --porcelain")
}

// SetURLCmd points origin at url. Only the remote changes; the working tree is not touched.
func SetURLCmd(sudo bool, owner, dir, url string) string {
	return asOwner(sudo, owner, gitC(dir)+" remote set-url origin "+sshx.Quote(url))
}

func credFile(dir string) string { return dir + "/.git/baryovm-credentials" }

func checkCredDir(dir string) error {
	if !safeDirRE.MatchString(dir) {
		return fmt.Errorf("refusing to store a credential in %q: the path has characters outside A-Z a-z 0-9 . _ / -", dir)
	}
	return nil
}

// CredHelperCmd stores credentials for this checkout in a file inside its .git directory, which the
// credential approve call then fills. Repository-local, so no other repository on the VM sees it.
// Git runs the helper value through a shell, so the path is quoted inside the value and the value
// is quoted again for the remote shell.
func CredHelperCmd(sudo bool, owner, dir string) (string, error) {
	if err := checkCredDir(dir); err != nil {
		return "", err
	}
	val := "store --file=" + sshx.Quote(credFile(dir))
	return asOwner(sudo, owner, gitC(dir)+" config credential.helper "+sshx.Quote(val)), nil
}

// CredApproveCmd reads a credential description on stdin and stores it with the helper, as the
// checkout owner. umask 077 keeps the file private and a symlink in the file's place is refused.
// The token arrives on stdin only.
func CredApproveCmd(sudo bool, owner, dir string) (string, error) {
	if err := checkCredDir(dir); err != nil {
		return "", err
	}
	// credential approve goes to every configured helper, the owner's global ones included. An empty
	// credential.helper first resets that list, then only the store file of this checkout is named.
	helper := "-c credential.helper= -c " + sshx.Quote("credential.helper=store --file="+sshx.Quote(credFile(dir)))
	inner := "if [ -L " + sshx.Quote(credFile(dir)) + " ]; then echo 'credential file is a symlink' >&2; exit 1; fi; umask 077; " +
		gitEnv + gitOpts + helper + " -C " + sshx.Quote(dir) + " credential approve"
	return asOwnerShell(sudo, owner, inner), nil
}

// CredUndoCmd removes what CredHelperCmd and CredApproveCmd wrote: the credential file and the
// helper setting. It runs when a later step fails and the move is undone.
func CredUndoCmd(sudo bool, owner, dir string) (string, error) {
	if err := checkCredDir(dir); err != nil {
		return "", err
	}
	inner := "rm -f -- " + sshx.Quote(credFile(dir)) + "; " + gitEnv + gitC(dir) + " config --unset-all credential.helper; true"
	return asOwnerShell(sudo, owner, inner), nil
}

func asOwnerShell(sudo bool, owner, inner string) string {
	if !sudo {
		return "sh -c " + sshx.Quote(inner)
	}
	return "sudo -n -u " + sshx.Quote(owner) + " sh -c " + sshx.Quote(inner)
}

// CredInput is the stdin for CredApproveCmd. The credential protocol is line based, so a value
// with a line break or NUL would add fields of its own and is refused.
func CredInput(host, user, token string) (string, error) {
	for _, v := range []string{host, user, token} {
		if strings.ContainsAny(v, "\r\n\x00") {
			return "", fmt.Errorf("the credential holds a line break or NUL: refusing to send it")
		}
	}
	return "protocol=https\nhost=" + host + "\nusername=" + user + "\npassword=" + token + "\n\n", nil
}

// LocalBranchRemotesArgv is the argv that lists branch.<name>.remote settings of a clone.
func LocalBranchRemotesArgv(dir string) []string {
	return []string{"git", "-C", dir, "config", "--get-regexp", `^branch\..*\.remote$`}
}

// TrackedBranches returns the branches whose upstream remote is remote, from the output of
// LocalBranchRemotesArgv.
func TrackedBranches(out, remote string) []string {
	var names []string
	for _, line := range strings.Split(out, "\n") {
		key, val, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok || strings.TrimSpace(val) != remote || !strings.HasPrefix(key, "branch.") || !strings.HasSuffix(key, ".remote") {
			continue
		}
		names = append(names, strings.TrimSuffix(strings.TrimPrefix(key, "branch."), ".remote"))
	}
	return names
}

// ReferencesCmd finds lines in the system crontabs, the user's crontab and systemd units that
// mention dir. Read-only.
func ReferencesCmd(sudo bool, dir string) string {
	q := sshx.Quote(dir)
	files := "/etc/crontab /etc/cron.d/* /etc/systemd/system/*.service /etc/systemd/system/*.timer"
	return sshx.Sudo(sudo, "grep -sn -F -- "+q+" "+files) + "; crontab -l 2>/dev/null | grep -n -F -- " + q + " | sed 's/^/crontab:/'; true"
}

// ParseReferences keeps the non-empty lines of ReferencesCmd output.
func ParseReferences(vm, dir, out string) []Reference {
	var refs []Reference
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			refs = append(refs, Reference{VM: vm, Checkout: dir, Match: line})
		}
	}
	return refs
}
