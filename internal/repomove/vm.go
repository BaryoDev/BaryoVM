// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package repomove

import (
	"io"
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
	Sudo  bool
	Roots []string // directories to search for checkouts
}

// DefaultRoots are searched on a VM in addition to the directories of the stacks registered on it.
var DefaultRoots = []string{"/opt", "/srv", "/home", "/var/www"}

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
}

// Reference is a crontab line or systemd unit line that mentions a checkout. Listed, never edited:
// the user repoints these to the deployed copy.
type Reference struct {
	VM       string `json:"vm"`
	Checkout string `json:"checkout"`
	Match    string `json:"match"`
}

// gitC runs git in dir. safe.directory is set per call because the search may run as root over a
// checkout another user owns, and git refuses that otherwise.
func gitC(dir string) string {
	return "git -c safe.directory=" + sshx.Quote(dir) + " -C " + sshx.Quote(dir)
}

// FindCmd lists the .git entries under the roots, to a depth that covers /opt/app/repo/.git.
func FindCmd(sudo bool, roots []string) string {
	q := make([]string, len(roots))
	for i, r := range roots {
		q[i] = sshx.Quote(r)
	}
	return sshx.Sudo(sudo, "find "+strings.Join(q, " ")+" -maxdepth 5 -name .git -prune -print 2>/dev/null; true")
}

// ParseFinds turns find output into checkout directories.
func ParseFinds(out string) []string {
	var dirs []string
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasSuffix(line, "/.git") {
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

// GetURLCmd reads the origin URL of a checkout.
func GetURLCmd(sudo bool, dir string) string {
	return sshx.Sudo(sudo, gitC(dir)+" remote get-url origin")
}

// StatusCmd lists local changes; empty output means a clean checkout.
func StatusCmd(sudo bool, dir string) string {
	return sshx.Sudo(sudo, gitC(dir)+" status --porcelain")
}

// SetURLCmd points origin at url. Only the remote changes; the working tree is not touched.
func SetURLCmd(sudo bool, dir, url string) string {
	return sshx.Sudo(sudo, gitC(dir)+" remote set-url origin "+sshx.Quote(url))
}

// CredHelperCmd stores credentials for this checkout in a file inside its .git directory, which the
// credential approve call then fills. Repository-local, so no other repository on the VM sees it.
func CredHelperCmd(sudo bool, dir string) string {
	return sshx.Sudo(sudo, gitC(dir)+" config credential.helper "+sshx.Quote("store --file="+dir+"/.git/baryovm-credentials"))
}

// CredApproveCmd reads a credential description on stdin and stores it with the helper. umask 077
// keeps the file private. The token arrives on stdin only.
func CredApproveCmd(sudo bool, dir string) string {
	return sshx.SudoShell(sudo, "umask 077 && "+gitC(dir)+" credential approve")
}

// CredInput is the stdin for CredApproveCmd.
func CredInput(host, user, token string) string {
	return "protocol=https\nhost=" + host + "\nusername=" + user + "\npassword=" + token + "\n\n"
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
