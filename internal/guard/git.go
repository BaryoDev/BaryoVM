// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package guard

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Runner is the seam to the outside world. Git runs `git <globals> <args>` and returns stdout;
// ReadFile reads a file named on a command line. Tests supply a fake, so no check ever runs git or
// gh for real against a remote.
type Runner interface {
	Git(globals []string, args ...string) (string, error)
	ReadFile(path string) ([]byte, error)
}

// Verdict is the outcome of a check. When Allowed is false, Reason says what to do next.
type Verdict struct {
	Allowed  bool      `json:"allowed"`
	Action   string    `json:"action"`
	Reason   string    `json:"reason,omitempty"`
	Findings []Finding `json:"findings,omitempty"`
}

func allow(action string) Verdict { return Verdict{Allowed: true, Action: action} }

func refuse(action, reason string, fs []Finding) Verdict {
	return Verdict{Action: action, Reason: reason, Findings: fs}
}

func keyRefusal(action, what string, fs []Finding) Verdict {
	return refuse(action, fmt.Sprintf("%s refused: %d suspected key(s) in %s: %s. Remove the key and use a gitignored env file or a pipeline secret; for a test fixture add the comment %s on that line",
		action, len(fs), what, Summarise(fs, 5), AllowMarker), fs)
}

// gitValueGlobals are git's own options that take the next word as a value.
var gitValueGlobals = map[string]bool{"-C": true, "-c": true, "--git-dir": true, "--work-tree": true, "--namespace": true, "--exec-path": true}

// SplitGit separates `git [global options] <subcommand> [args]`. The globals are kept so the
// checks run against the same repository the real command will.
func SplitGit(args []string) (globals []string, sub string, rest []string) {
	i := 0
	for i < len(args) {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			return args[:i], a, args[i+1:]
		}
		if gitValueGlobals[a] {
			i += 2
			continue
		}
		i++
	}
	return args, "", nil
}

// CheckGit plans the checks for a git command. Only commit and push are checked; every other
// subcommand is allowed straight through.
func CheckGit(r Runner, args []string, pol Policy) Verdict {
	globals, sub, rest := SplitGit(args)
	switch sub {
	case "commit":
		return checkCommit(r, globals, rest)
	case "push":
		return checkPush(r, globals, rest, pol)
	}
	return allow("git " + sub)
}

var (
	commitValueLong  = map[string]bool{"--message": true, "--file": true, "--reuse-message": true, "--reedit-message": true, "--author": true, "--date": true, "--template": true, "--cleanup": true, "--fixup": true, "--squash": true, "--trailer": true}
	commitValueShort = "mFCct"
)

// --text and --no-textconv stop .gitattributes (binary, a textconv filter) from replacing a file's
// content with "Binary files differ" or a converted form, which would hide a key from the scan.
// logDiffFlags adds -m so a merge commit shows its diff against each parent; a key introduced in a
// conflict resolution appears only there.
const (
	diffFlags    = "-U0 --no-color --no-ext-diff --text --no-textconv"
	logDiffFlags = diffFlags + " -m"
)

func diffArgs(sub ...string) []string {
	a := append([]string{}, sub...)
	return append(a, strings.Fields(diffFlags)...)
}

func checkCommit(r Runner, globals, rest []string) Verdict {
	const action = "git commit"
	var (
		all   bool
		paths []string
		msgs  []string
		// msgFiles are -F and --file message files: that text is published like any other.
		msgFiles []string
	)
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		switch {
		case a == "--":
			paths = append(paths, rest[i+1:]...)
			i = len(rest)
		case a == "--all":
			all = true
		case strings.HasPrefix(a, "--"):
			name, val, hasVal := strings.Cut(a, "=")
			if !hasVal && commitValueLong[name] && i+1 < len(rest) {
				i++
				val = rest[i]
			}
			if name == "--message" {
				msgs = append(msgs, val)
			}
			if name == "--file" {
				msgFiles = append(msgFiles, val)
			}
		case strings.HasPrefix(a, "-") && len(a) > 1:
			for j := 1; j < len(a); j++ {
				c := a[j]
				if c == 'a' {
					all = true
				}
				if strings.IndexByte(commitValueShort, c) >= 0 {
					val := a[j+1:]
					if val == "" && i+1 < len(rest) {
						i++
						val = rest[i]
					}
					if c == 'm' {
						msgs = append(msgs, val)
					}
					if c == 'F' {
						msgFiles = append(msgFiles, val)
					}
					break
				}
			}
		default:
			paths = append(paths, a)
		}
	}

	var fs []Finding
	staged, err := r.Git(globals, diffArgs("diff", "--cached")...)
	if err != nil {
		return refuse(action, fmt.Sprintf("%s refused: could not read the staged diff (%v). Run it inside a git repository", action, err), nil)
	}
	fs = append(fs, ScanDiff(staged)...)
	// A commit with paths, or -a, takes the working tree content of those files, not the index.
	if all || len(paths) > 0 {
		args := diffArgs("diff")
		if !all {
			args = append(args, "--")
			args = append(args, paths...)
		}
		work, err := r.Git(globals, args...)
		if err != nil {
			return refuse(action, fmt.Sprintf("%s refused: could not read the working diff (%v)", action, err), nil)
		}
		fs = append(fs, ScanDiff(work)...)
	}
	for _, m := range msgs {
		fs = append(fs, ScanText("commit message", m)...)
	}
	for _, mf := range msgFiles {
		if mf == "-" {
			return refuse(action, action+" refused: -F - reads the message from stdin, which cannot be checked. Write it to a file and pass that file", nil)
		}
		b, err := r.ReadFile(commitFilePath(globals, mf))
		if err != nil {
			return refuse(action, fmt.Sprintf("%s refused: cannot read the message file %s (%v). Check the path", action, mf, err), nil)
		}
		fs = append(fs, ScanText(mf, string(b))...)
	}
	if len(fs) > 0 {
		return keyRefusal(action, "the commit", fs)
	}
	return allow(action)
}

// commitFilePath resolves a relative -F path against the -C directories git changes into before
// it reads the file.
func commitFilePath(globals []string, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	dir := ""
	for i := 0; i+1 < len(globals); i++ {
		if globals[i] == "-C" {
			if filepath.IsAbs(globals[i+1]) {
				dir = globals[i+1]
			} else {
				dir = filepath.Join(dir, globals[i+1])
			}
		}
	}
	return filepath.Join(dir, p)
}

// CheckCommitMsg is what the commit-msg hook runs on the message file git passes in.
func CheckCommitMsg(r Runner, file string) Verdict {
	b, err := r.ReadFile(file)
	if err != nil {
		return refuse("commit-msg", fmt.Sprintf("commit refused: cannot read the message file (%v)", err), nil)
	}
	if fs := ScanText("commit message", string(b)); len(fs) > 0 {
		return keyRefusal("commit", "the commit message", fs)
	}
	return allow("commit-msg")
}

var (
	pushValueLong = map[string]bool{"--repo": true, "--receive-pack": true, "--exec": true, "--push-option": true}
)

func checkPush(r Runner, globals, rest []string, pol Policy) Verdict {
	const action = "git push"
	var (
		positional []string
		repo       string
		all        bool
		mirror     bool
		tags       bool
		del        bool
	)
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		switch {
		case a == "--":
			positional = append(positional, rest[i+1:]...)
			i = len(rest)
		case a == "--all" || a == "--branches":
			all = true
		case a == "--mirror":
			mirror = true
		case a == "--tags":
			tags = true
		case a == "--delete" || a == "-d":
			del = true
		case strings.HasPrefix(a, "--"):
			name, val, hasVal := strings.Cut(a, "=")
			if !hasVal && pushValueLong[name] && i+1 < len(rest) {
				i++
				val = rest[i]
			}
			if name == "--repo" {
				repo = val
			}
		case a == "-o" && i+1 < len(rest):
			i++
		case strings.HasPrefix(a, "-") && len(a) > 1:
			// a cluster such as -fd; -o carries a value, so its letters are not flags
			if !strings.HasPrefix(a, "-o") && strings.Contains(a, "d") {
				del = true
			}
		default:
			positional = append(positional, a)
		}
	}

	remote := repo
	var refspecs []string
	if remote == "" && len(positional) > 0 {
		remote, refspecs = positional[0], positional[1:]
	} else {
		refspecs = positional
	}
	if remote == "" {
		remote = defaultPushRemote(r, globals)
	}

	// Resolve to URLs with local git commands only; nothing here touches the network. A remote can
	// have several push URLs and git pushes to all of them, so each one must pass the policy.
	isName := true
	out, err := r.Git(globals, "remote", "get-url", "--push", "--all", remote)
	urls := strings.Fields(out)
	if err != nil || len(urls) == 0 {
		if strings.ContainsAny(remote, "/:") {
			urls, isName = []string{remote}, false
		} else {
			return refuse(action, fmt.Sprintf("%s refused: no remote named %q. List them with git remote -v", action, remote), nil)
		}
	}
	for _, url := range urls {
		host, err := ParseRemoteHost(url)
		if err != nil {
			return refuse(action, fmt.Sprintf("%s refused: %v. Push to a remote with a plain host", action, err), nil)
		}
		if !pol.Allows(host) {
			return refuse(action, fmt.Sprintf("%s refused: host %q is not in the allowed list (%s). Push to an allowed remote, or allow it with: git config --add %s %s",
				action, host, strings.Join(pol.AllowedHosts, ", "), PolicyKey, host), nil)
		}
	}
	if del {
		return allow(action) // deleting remote refs sends no commits
	}

	var revs []string
	switch {
	case mirror:
		revs = []string{"--all"}
	case all:
		revs = []string{"--branches"}
	case len(refspecs) == 0:
		revs = []string{"HEAD"}
	}
	if tags {
		revs = append(revs, "--tags")
	}
	for _, spec := range refspecs {
		src, _, _ := strings.Cut(strings.TrimPrefix(spec, "+"), ":")
		if src == "" {
			continue // ":branch" deletes a remote ref, nothing to scan
		}
		if strings.HasPrefix(src, "-") {
			return refuse(action, fmt.Sprintf("%s refused: cannot read the refspec %q. Give a branch or tag name", action, spec), nil)
		}
		revs = append(revs, src)
	}
	if len(revs) == 0 {
		return allow(action)
	}
	not := "--remotes"
	if isName {
		not = "--remotes=" + remote
	}
	logArgs := append([]string{"log", "-p", "--format="}, strings.Fields(logDiffFlags)...)
	logArgs = append(logArgs, revs...)
	logArgs = append(logArgs, "--not", not)
	out, err = r.Git(globals, logArgs...)
	if err != nil {
		return refuse(action, fmt.Sprintf("%s refused: could not list the commits to push (%v). Check the branch name", action, err), nil)
	}
	if fs := ScanDiff(out); len(fs) > 0 {
		return keyRefusal(action, "the commits being pushed", fs)
	}
	return allow(action)
}

func defaultPushRemote(r Runner, globals []string) string {
	if b, err := r.Git(globals, "symbolic-ref", "--short", "HEAD"); err == nil {
		if rem, err := r.Git(globals, "config", "branch."+strings.TrimSpace(b)+".remote"); err == nil && strings.TrimSpace(rem) != "" {
			return strings.TrimSpace(rem)
		}
	}
	return "origin"
}

// CheckPreCommit is what the pre-commit hook runs: the staged diff only.
func CheckPreCommit(r Runner) Verdict {
	const action = "pre-commit"
	staged, err := r.Git(nil, diffArgs("diff", "--cached")...)
	if err != nil {
		return refuse(action, fmt.Sprintf("commit refused: could not read the staged diff (%v)", err), nil)
	}
	if fs := ScanDiff(staged); len(fs) > 0 {
		return keyRefusal("commit", "the staged changes", fs)
	}
	return allow(action)
}

const zeroSHA = "0000000000000000000000000000000000000000"

// CheckPrePush is what the pre-push hook runs. git passes the remote name and URL as arguments and
// one "<local ref> <local sha> <remote ref> <remote sha>" line per ref on stdin.
func CheckPrePush(r Runner, remote, url, stdin string, pol Policy) Verdict {
	const action = "push"
	host, err := ParseRemoteHost(url)
	if err != nil {
		return refuse(action, fmt.Sprintf("push refused: %v", err), nil)
	}
	if !pol.Allows(host) {
		return refuse(action, fmt.Sprintf("push refused: host %q is not in the allowed list (%s). Allow it with: git config --add %s %s",
			host, strings.Join(pol.AllowedHosts, ", "), PolicyKey, host), nil)
	}
	var fs []Finding
	for _, line := range strings.Split(stdin, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 || f[1] == zeroSHA {
			continue
		}
		args := append([]string{"log", "-p", "--format="}, strings.Fields(logDiffFlags)...)
		if f[3] == zeroSHA {
			args = append(args, f[1], "--not", "--remotes="+remote)
		} else {
			args = append(args, f[3]+".."+f[1])
		}
		out, err := r.Git(nil, args...)
		if err != nil {
			return refuse(action, fmt.Sprintf("push refused: could not list the commits to push (%v)", err), nil)
		}
		fs = append(fs, ScanDiff(out)...)
	}
	if len(fs) > 0 {
		return keyRefusal("push", "the commits being pushed", fs)
	}
	return allow(action)
}
