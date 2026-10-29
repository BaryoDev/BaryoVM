// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package guard

import (
	"errors"
	"fmt"
	"os"
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
	// Rule names the rule that refused, so the audit log can say which one.
	Rule string `json:"rule,omitempty"`
	// Target, Audit and Args are for the wrapper: what the command acts on, whether it goes in the
	// activity log, and the arguments to run (--confirm removed, --yes added after a confirmation).
	Target string   `json:"-"`
	Audit  bool     `json:"-"`
	Args   []string `json:"-"`
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
// subcommand is allowed straight through, except an alias, which is expanded and checked as the
// command it stands for.
func CheckGit(r Runner, args []string, pol Policy) Verdict {
	return checkGit(r, args, pol, 0)
}

// gitBuiltins are git subcommands an alias cannot shadow, so they need no alias lookup.
var gitBuiltins = map[string]bool{}

func init() {
	for _, b := range strings.Fields(`add am archive bisect blame branch bundle checkout cherry-pick clean clone commit config
		describe diff fetch format-patch gc grep init log merge mv notes pull push range-diff rebase reflog remote reset restore
		revert rm shortlog show sparse-checkout stash status submodule switch tag worktree rev-parse rev-list ls-files ls-remote
		ls-tree cat-file symbolic-ref show-ref update-ref apply bugreport count-objects fsck help maintenance mergetool prune
		repack version whatchanged cherry difftool request-pull send-email citool gui`) {
		gitBuiltins[b] = true
	}
}

func checkGit(r Runner, args []string, pol Policy, depth int) Verdict {
	globals, sub, rest := SplitGit(args)
	if v, bad := refuseConfigGlobals(globals); bad {
		return v
	}
	switch sub {
	case "commit":
		return checkCommit(r, globals, rest)
	case "push":
		return checkPush(r, globals, rest, pol)
	}
	if sub == "" || gitBuiltins[sub] || strings.HasPrefix(sub, "-") {
		return allow("git " + sub)
	}
	action := "git " + sub
	if depth >= 5 {
		return refuse(action, action+" refused: the alias "+sub+" expands through too many aliases. Run the real command instead of the alias", nil)
	}
	out, err := r.Git(globals, "config", "--get", "alias."+sub)
	if err != nil {
		var coded interface{ ExitCode() int }
		if errors.As(err, &coded) && coded.ExitCode() == 1 {
			return allow(action) // not an alias
		}
		return refuse(action, fmt.Sprintf("%s refused: cannot tell whether %s is an alias (%v). Run the real command instead", action, sub, err), nil)
	}
	exp := strings.TrimSpace(out)
	if exp == "" {
		return allow(action)
	}
	if strings.HasPrefix(exp, "!") {
		if containsStr(pol.AllowShellAliases, sub) {
			return allow(action)
		}
		return refuse(action, fmt.Sprintf("%s refused: %s is a shell alias, and the guard cannot tell what a shell command runs. Run baryovm git commit or push directly, or allow the alias with: git config --add %s %s", action, sub, ShellAliasKey, sub), nil)
	}
	next := append(append(append([]string{}, globals...), aliasWords(exp)...), rest...)
	return checkGit(r, next, pol, depth+1)
}

// configKeyRefused reports whether a lowercase -c key could change what the guard reads: an alias,
// a policy key, or an include that loads a file with either.
func configKeyRefused(k string) bool {
	for _, p := range []string{"alias.", "baryovm.", "include.", "includeif."} {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	return false
}

// StripConfigGlobals drops the -c settings from the global options, so the guard reads policy,
// rules and aliases from the config files only. Other globals (-C, --git-dir) stay.
func StripConfigGlobals(globals []string) []string {
	var out []string
	for i := 0; i < len(globals); i++ {
		g := globals[i]
		switch {
		case g == "-c":
			i++
		case strings.HasPrefix(g, "-c") && len(g) > 2:
		default:
			out = append(out, g)
		}
	}
	return out
}

// ConfigEnvSet returns the name of an environment variable that sets git config for a command
// without a -c flag (GIT_CONFIG_PARAMETERS, GIT_CONFIG_COUNT, GIT_CONFIG_KEY_n, GIT_CONFIG_VALUE_n), or "".
func ConfigEnvSet(environ []string) string {
	for _, e := range environ {
		name, _, _ := strings.Cut(e, "=")
		if IsConfigEnv(name) {
			return name
		}
	}
	return ""
}

// IsConfigEnv reports whether an environment variable name injects git config.
func IsConfigEnv(name string) bool {
	return name == "GIT_CONFIG" || name == "GIT_CONFIG_PARAMETERS" || name == "GIT_CONFIG_COUNT" ||
		strings.HasPrefix(name, "GIT_CONFIG_KEY_") || strings.HasPrefix(name, "GIT_CONFIG_VALUE_")
}

// refuseConfigGlobals refuses the global options that can define an alias or change config for
// one run, because the guard cannot see what they do to the command that follows.
func refuseConfigGlobals(globals []string) (Verdict, bool) {
	for i := 0; i < len(globals); i++ {
		g := globals[i]
		bad := ""
		switch {
		case g == "--config-env" || strings.HasPrefix(g, "--config-env="):
			bad = g
		case g == "-c" && i+1 < len(globals):
			i++
			if k := strings.ToLower(globals[i]); configKeyRefused(k) {
				bad = "-c " + globals[i]
			}
		case strings.HasPrefix(g, "-c") && len(g) > 2 && configKeyRefused(strings.ToLower(g[2:])):
			bad = g
		}
		if bad != "" {
			name, _, _ := strings.Cut(bad, "=")
			return refuse("git", fmt.Sprintf("git refused: %s can define an alias, set a baryovm.* policy key or load a config file for one command, which the guard cannot check. Set it with git config instead", name), nil), true
		}
	}
	return Verdict{}, false
}

var (
	commitValueLong = map[string]bool{"--message": true, "--file": true, "--reuse-message": true, "--reedit-message": true, "--author": true, "--date": true, "--template": true, "--cleanup": true, "--fixup": true, "--squash": true, "--trailer": true, "--pathspec-from-file": true}
	// commitLongs is every long option of git commit, for resolving abbreviations.
	commitLongs = []string{"--all", "--interactive", "--patch", "--only", "--include", "--no-verify", "--message", "--file", "--author", "--date",
		"--template", "--signoff", "--no-signoff", "--verbose", "--quiet", "--edit", "--no-edit", "--amend", "--reset-author", "--allow-empty",
		"--allow-empty-message", "--cleanup", "--status", "--no-status", "--gpg-sign", "--no-gpg-sign", "--reuse-message", "--reedit-message",
		"--fixup", "--squash", "--trailer", "--dry-run", "--short", "--branch", "--porcelain", "--long", "--null", "--untracked-files",
		"--pathspec-from-file", "--pathspec-file-nul", "--no-post-rewrite", "--verify", "--no-verify", "--help"}
	commitValueShort = "mFCct"
)

// resolveLong expands an abbreviated long option to the one option of known it is a prefix of, the
// way git's option parser does. An exact name wins. A name that matches none is returned unchanged
// (it cannot be an abbreviation of an option we read). A name that matches several is reported as
// ambiguous: git rejects it, and the caller refuses rather than guess.
func resolveLong(name string, known []string) (resolved string, ambiguous bool) {
	var hit string
	for _, k := range known {
		if k == name {
			return name, false
		}
		if strings.HasPrefix(k, name) && k != hit {
			if hit != "" {
				return name, true
			}
			hit = k
		}
	}
	if hit != "" {
		return hit, false
	}
	return name, false
}

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
		case strings.HasPrefix(a, "--"):
			name, val, hasVal := strings.Cut(a, "=")
			// git accepts any unambiguous prefix (--fi, --mess), so resolve it first.
			name, amb := resolveLong(name, commitLongs)
			if amb {
				return refuse(action, fmt.Sprintf("%s refused: the option %s is ambiguous. Spell the option out in full", action, a), nil)
			}
			if name == "--all" {
				all = true
			}
			if name == "--pathspec-from-file" {
				return refuse(action, action+" refused: --pathspec-from-file (also -, for stdin) names paths the guard cannot see. Pass the paths as arguments after --", nil)
			}
			if !hasVal && commitValueLong[name] && i+1 < len(rest) {
				i++
				val = rest[i]
			}
			switch name {
			case "--message", "--trailer":
				msgs = append(msgs, val)
			case "--file", "--template":
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
					if c == 'F' || c == 't' {
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
		path, perr := commitFilePath(globals, mf)
		if perr != nil {
			return refuse(action, fmt.Sprintf("%s refused: %v", action, perr), nil)
		}
		b, err := r.ReadFile(path)
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
func commitFilePath(globals []string, p string) (string, error) {
	if filepath.IsAbs(p) {
		return p, nil
	}
	// Where git starts a relative path depends on the work tree layout, which globals, -c config
	// and GIT_* variables can all move. Listing every way to move it is a race this guard loses,
	// so only -C is trusted: any other global flag, or any variable that can point git elsewhere,
	// refuses the relative path.
	redirected := false
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if name == "GIT_DIR" || name == "GIT_WORK_TREE" || name == "GIT_COMMON_DIR" || strings.HasPrefix(name, "GIT_CONFIG") {
			redirected = true
		}
	}
	for i := 0; i < len(globals); i++ {
		if globals[i] == "-C" && i+1 < len(globals) {
			i++
			continue
		}
		redirected = true
	}
	if redirected {
		return "", fmt.Errorf("the message file %s is relative and git may read it from another work tree (a global flag other than -C, or GIT_DIR, GIT_WORK_TREE, GIT_COMMON_DIR or GIT_CONFIG* is set): pass an absolute path to -F", p)
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
	return filepath.Join(dir, p), nil
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
	pushValueLong = map[string]bool{"--repo": true, "--receive-pack": true, "--exec": true, "--push-option": true, "--recurse-submodules": true}
	// pushLongs is every long option of git push, for resolving abbreviations.
	pushLongs = []string{"--all", "--branches", "--mirror", "--tags", "--delete", "--repo", "--receive-pack", "--exec", "--push-option",
		"--force", "--force-with-lease", "--force-if-includes", "--no-verify", "--verify", "--dry-run", "--porcelain", "--follow-tags",
		"--no-follow-tags", "--atomic", "--signed", "--set-upstream", "--thin", "--no-thin", "--quiet", "--verbose", "--progress",
		"--prune", "--recurse-submodules", "--ipv4", "--ipv6", "--help"}
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
		case strings.HasPrefix(a, "--"):
			name, val, hasVal := strings.Cut(a, "=")
			// git accepts any unambiguous prefix (--del), so resolve it first.
			name, amb := resolveLong(name, pushLongs)
			if amb {
				return refuse(action, fmt.Sprintf("%s refused: the option %s is ambiguous. Spell the option out in full", action, a), nil)
			}
			if !hasVal && pushValueLong[name] && i+1 < len(rest) {
				i++
				val = rest[i]
			}
			switch name {
			case "--all", "--branches":
				all = true
			case "--mirror":
				mirror = true
			case "--tags":
				tags = true
			case "--delete":
				del = true
			case "--repo":
				repo = val
			}
		case strings.HasPrefix(a, "-") && len(a) > 1:
			// A cluster such as -fd is read letter by letter and stops at -o, which takes the rest
			// of the cluster (or the next word) as its value, so "-od" is not a delete.
			for j := 1; j < len(a); j++ {
				if a[j] == 'o' {
					if j == len(a)-1 && i+1 < len(rest) {
						i++
					}
					break
				}
				if a[j] == 'd' {
					del = true
				}
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
