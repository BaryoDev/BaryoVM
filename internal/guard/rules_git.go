// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package guard

import (
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// CheckGitRules runs the key checks first, then the rules beyond keys that apply to the
// subcommand. Each rule is narrow and switchable in the policy file (see Rules). The returned
// verdict carries the arguments to run, the target and whether it goes in the activity log.
func CheckGitRules(r Runner, args []string, pol Policy, ru Rules, env Env) Verdict {
	orig := args
	args = expandAlias(r, args) // the rules read the expansion; the original words are what runs
	v := CheckGit(r, args, pol)
	globals, sub, rest := SplitGit(args)
	globals = append(append([]string{}, globals...), "-c", "core.quotepath=off")
	if v.Allowed {
		switch sub {
		case "commit":
			v = ruleCommit(r, globals, rest, ru, v)
		case "push":
			v = rulePush(r, globals, rest, ru, env, v)
		case "reset":
			v = ruleReset(r, globals, rest, ru, env, v)
		case "clean":
			v = ruleClean(r, globals, rest, ru, env, v)
		}
	}
	if v.Target == "" {
		v.Target = gitTarget(r, globals, sub, rest)
	}
	v.Audit = ru.On(RuleAudit) && (!v.Allowed || sub == "commit" || sub == "push" || sub == "reset" || sub == "clean")
	if v.Args == nil {
		v.Args = orig
	}
	return v
}

// expandAlias replaces a plain alias with its expansion, so the rules see the real subcommand.
func expandAlias(r Runner, args []string) []string {
	for i := 0; i < 5; i++ {
		globals, sub, rest := SplitGit(args)
		if sub == "" || gitBuiltins[sub] || strings.HasPrefix(sub, "-") {
			return args
		}
		out, err := r.Git(globals, "config", "--get", "alias."+sub)
		exp := strings.TrimSpace(out)
		if err != nil || exp == "" || strings.HasPrefix(exp, "!") {
			return args
		}
		args = append(append(append([]string{}, globals...), aliasWords(exp)...), rest...)
	}
	return args
}

func gitTarget(r Runner, globals []string, sub string, rest []string) string {
	switch sub {
	case "push":
		p := parsePush(rest)
		t := p.remote
		if t == "" {
			t = "origin"
		}
		return strings.TrimSpace(SafeTarget(t) + " " + SafeTarget(strings.Join(p.refspecs, " ")))
	case "commit", "reset":
		return SafeTarget(currentBranch(r, globals))
	}
	return ""
}

func currentBranch(r Runner, globals []string) string {
	out, err := r.Git(globals, "symbolic-ref", "--short", "-q", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

type pushArgs struct {
	remote    string
	refspecs  []string
	force     bool // --force, -f, or a +refspec
	lease     bool
	del       bool
	all       bool
	mirror    bool
	tags      bool
	ambiguous string
}

func parsePush(rest []string) pushArgs {
	var p pushArgs
	var pos []string
	repo := ""
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		switch {
		case a == "--":
			pos = append(pos, rest[i+1:]...)
			i = len(rest)
		case strings.HasPrefix(a, "--"):
			name, val, hasVal := strings.Cut(a, "=")
			name, amb := resolveLong(name, pushLongs)
			if amb {
				p.ambiguous = a
			}
			if !hasVal && pushValueLong[name] && i+1 < len(rest) {
				i++
				val = rest[i]
			}
			switch name {
			case "--force":
				p.force = true
			case "--force-with-lease":
				p.lease = true
			case "--delete":
				p.del = true
			case "--all", "--branches":
				p.all = true
			case "--mirror":
				p.mirror = true
			case "--tags":
				p.tags = true
			case "--repo":
				repo = val
			}
		case strings.HasPrefix(a, "-") && len(a) > 1:
			for j := 1; j < len(a); j++ {
				if a[j] == 'o' {
					if j == len(a)-1 && i+1 < len(rest) {
						i++
					}
					break
				}
				switch a[j] {
				case 'f':
					p.force = true
				case 'd':
					p.del = true
				}
			}
		default:
			pos = append(pos, a)
		}
	}
	// Like git, the first positional is the remote and the rest are refspecs; --repo only
	// supplies the remote when there is no positional.
	switch {
	case len(pos) > 0:
		p.remote, p.refspecs = pos[0], pos[1:]
	case repo != "":
		p.remote = repo
	}
	return p
}

// dest is a refspec reduced to the ref it writes on the remote.
type dest struct {
	name   string // branch or tag name, without refs/heads/ or refs/tags/
	tag    bool
	force  bool // a +refspec
	delete bool // ":name"
	src    string
	// unresolved holds the spelling of a destination that could not be mapped to a ref name.
	unresolved string
}

func (p pushArgs) dests(r Runner, globals []string) []dest {
	var out []dest
	add := func(src, dst string, force, del bool) {
		d := dest{force: force, delete: del, src: src}
		switch {
		case dst == "HEAD" || dst == "@":
			d.name = currentBranch(r, globals)
		case strings.HasPrefix(dst, "refs/tags/"):
			d.name, d.tag = strings.TrimPrefix(dst, "refs/tags/"), true
		case strings.HasPrefix(dst, "tags/"):
			d.name, d.tag = strings.TrimPrefix(dst, "tags/"), true
		case strings.HasPrefix(dst, "refs/heads/"):
			d.name = strings.TrimPrefix(dst, "refs/heads/")
		case strings.HasPrefix(dst, "heads/"):
			d.name = strings.TrimPrefix(dst, "heads/") // git tries refs/<x> before refs/heads/<x>
		default:
			d.name = dst
		}
		if d.name == "" {
			d.unresolved = dst
			if dst == "" {
				d.unresolved = src
			}
		}
		out = append(out, d)
	}
	// resolve names the branch or tag a source ref stands for, as git does when a refspec has no ":".
	resolve := func(src string) string {
		if src == "HEAD" || src == "@" {
			if b := currentBranch(r, globals); b != "" {
				return "refs/heads/" + b
			}
			return ""
		}
		if strings.HasPrefix(src, "refs/") {
			return src
		}
		if full, err := r.Git(globals, "rev-parse", "--symbolic-full-name", src); err == nil {
			if f := strings.TrimSpace(full); strings.HasPrefix(f, "refs/") {
				return f
			}
		}
		return src
	}
	if len(p.refspecs) == 0 {
		if b := currentBranch(r, globals); b != "" && !p.all && !p.mirror {
			add("HEAD", b, false, p.del)
		}
		return out
	}
	for _, spec := range p.refspecs {
		force := strings.HasPrefix(spec, "+")
		spec = strings.TrimPrefix(spec, "+")
		src, dst, hasDst := strings.Cut(spec, ":")
		switch {
		case p.del:
			add("", src, force, true)
		case !hasDst:
			add(src, resolve(src), force, false)
		case src == "":
			add("", dst, force, true)
		default:
			add(src, dst, force, false)
		}
	}
	return out
}

func defaultBranches(r Runner, globals []string, remote string) []string {
	if remote != "" && !strings.ContainsAny(remote, "/:") {
		out, err := r.Git(globals, "symbolic-ref", "--short", "-q", "refs/remotes/"+remote+"/HEAD")
		if b := strings.TrimPrefix(strings.TrimSpace(out), remote+"/"); err == nil && b != "" {
			return []string{b}
		}
	}
	return []string{"main", "master"}
}

// unclearDestination fails closed when the refs a push writes cannot be told from the command
// line: a glob refspec, an empty or ":" refspec (the matching rule), a bare push on a detached
// HEAD, or a bare push while remote.<name>.push or push.default=matching is configured.
func unclearDestination(r Runner, globals []string, p pushArgs, remote string) *Verdict {
	const action = "git push"
	how := "Name the branch to push, or turn the rule off with: git config --add baryovm.rules.off " + RuleProtected
	bad := func(why string) *Verdict {
		v := refuseRule(action, RuleProtected, "", fmt.Sprintf("%s refused: %s, so the guard cannot tell whether it writes a protected branch. %s", action, why, how))
		return &v
	}
	for _, spec := range p.refspecs {
		sp := strings.TrimPrefix(spec, "+")
		if strings.Contains(sp, "*") {
			return bad("the refspec " + spec + " is a pattern")
		}
		if sp == "" || sp == ":" {
			return bad("the refspec " + strconv.Quote(spec) + " pushes every matching branch")
		}
		if src, dst, has := strings.Cut(sp, ":"); has && dst == "" && src != "" {
			return bad("the refspec " + spec + " has no destination")
		}
	}
	if len(p.refspecs) == 0 && !p.all && !p.mirror {
		if currentBranch(r, globals) == "" {
			return bad("HEAD is detached and no branch was named")
		}
		if !strings.ContainsAny(remote, "/:") {
			if out, err := r.Git(globals, "config", "--get-all", "remote."+remote+".push"); err == nil && strings.TrimSpace(out) != "" {
				return bad("remote." + remote + ".push is configured")
			}
		}
		if out, err := r.Git(globals, "config", "--get", "push.default"); err == nil && strings.EqualFold(strings.TrimSpace(out), "matching") {
			return bad("push.default is matching")
		}
	}
	return nil
}

func matchesAny(patterns []string, name string) bool {
	for _, p := range patterns {
		if ok, _ := path.Match(p, name); ok {
			return true
		}
	}
	return false
}

func rulePush(r Runner, globals, rest []string, ru Rules, env Env, ok Verdict) Verdict {
	const action = "git push"
	p := parsePush(rest)
	if p.ambiguous != "" {
		return ok // CheckGit already refused an ambiguous option
	}
	remote := p.remote
	if remote == "" {
		remote = defaultPushRemote(r, globals)
	}
	ds := p.dests(r, globals)
	if ru.On(RuleProtected) && len(p.refspecs) == 0 && !p.all && !p.mirror {
		// A bare push may also write the upstream branch of the current one.
		if cur := currentBranch(r, globals); cur != "" {
			if m, err := r.Git(globals, "config", "--get", "branch."+cur+".merge"); err == nil {
				if up := strings.TrimPrefix(strings.TrimSpace(m), "refs/heads/"); up != "" && up != cur {
					ds = append(ds, dest{name: up})
				}
			}
		}
	}

	if ru.On(RuleProtected) {
		if p.mirror || p.all {
			flag := "--all"
			if p.mirror {
				flag = "--mirror"
			}
			return refuseRule(action, RuleProtected, remote, fmt.Sprintf("%s refused: %s pushes every branch, including the protected ones. Push the branches you mean by name, or turn the rule off with: git config --add %s%s %s", action, flag, RuleKeyPrefix, "rules.off", RuleProtected))
		}
		if v := unclearDestination(r, globals, p, remote); v != nil {
			return *v
		}
		prot := append(defaultBranches(r, globals, remote), ru.ProtectedBranches...)
		for _, d := range ds {
			if d.unresolved != "" {
				return refuseRule(action, RuleProtected, remote, fmt.Sprintf("%s refused: cannot tell which branch %s writes (HEAD is detached or the name does not resolve). Name the branch, or turn the rule off with: git config --add baryovm.rules.off %s", action, strconv.Quote(d.unresolved), RuleProtected))
			}
			if d.tag || !matchesAny(prot, d.name) {
				continue
			}
			isLease := p.lease && !p.force && !d.force
			switch {
			case isLease && matchesAny(ru.ForceLeaseBranches, d.name):
				continue
			case isLease:
				return refuseRule(action, RuleProtected, d.name, fmt.Sprintf("%s refused: %s is a protected branch and --force-with-lease is not allowed on it. Allow it with: git config --add baryovm.forceLeaseBranches %s", action, d.name, d.name))
			case p.force || d.force:
				return refuseRule(action, RuleProtected, d.name, fmt.Sprintf("%s refused: a force push to the protected branch %s. Use --force-with-lease on a branch the policy allows, or turn the rule off with: git config --add baryovm.rules.off %s", action, d.name, RuleProtected))
			case d.delete:
				return refuseRule(action, RuleProtected, d.name, fmt.Sprintf("%s refused: deleting the protected branch %s. Turn the rule off with: git config --add baryovm.rules.off %s", action, d.name, RuleProtected))
			default:
				return refuseRule(action, RuleProtected, d.name, fmt.Sprintf("%s refused: a direct push to the protected branch %s. Push a feature branch and open a PR, or turn the rule off with: git config --add baryovm.rules.off %s", action, d.name, RuleProtected))
			}
		}
	}

	deleteOnly := p.del
	for _, d := range ds {
		if !d.delete {
			deleteOnly = false
		}
		if ru.On(RuleDestructive) && d.delete {
			isTag := d.tag
			if !isTag {
				out, err := r.Git(globals, "rev-parse", "-q", "--verify", "refs/tags/"+d.name)
				isTag = err == nil && strings.TrimSpace(out) != "" && !branchExists(r, globals, d.name)
			}
			if isTag {
				if v := confirmTarget(env, action+" --delete (tag)", RuleDestructive, d.name,
					[]string{"the tag " + d.name + " will be removed from " + remote}); v != nil {
					return *v
				}
			}
		}
	}
	if deleteOnly || p.del {
		return ok
	}

	if ru.On(RuleFiles) {
		if v := pushFiles(r, globals, p, remote, ru); v != nil {
			return *v
		}
	}
	if ru.On(RuleGate) && ru.Gate != "" {
		if v := runGate(r, globals, p, ru, env); v != nil {
			return *v
		}
	}
	return ok
}

func branchExists(r Runner, globals []string, name string) bool {
	out, err := r.Git(globals, "rev-parse", "-q", "--verify", "refs/heads/"+name)
	return err == nil && strings.TrimSpace(out) != ""
}

// gateSources lists the revisions a push sends: every refspec source, HEAD for a bare push, all
// branches for --all and --mirror, and all tags for --tags.
func gateSources(p pushArgs) (revs []string, all bool) {
	for _, spec := range p.refspecs {
		src, _, _ := strings.Cut(strings.TrimPrefix(spec, "+"), ":")
		if src != "" {
			revs = append(revs, src)
		}
	}
	if len(p.refspecs) == 0 && !p.all && !p.mirror {
		revs = append(revs, "HEAD")
	}
	return revs, p.all || p.mirror
}

func commitOf(r Runner, globals []string, rev string) string {
	out, err := r.Git(globals, "rev-parse", "--verify", "-q", rev+"^{commit}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// runGate returns nil when the gate passed for every commit the push sends, either on a recorded
// run or now. The gate script checks the work tree, so it is only run for the commit at HEAD; a
// pushed commit that is not HEAD needs a recorded pass.
func runGate(r Runner, globals []string, p pushArgs, ru Rules, env Env) *Verdict {
	const action = "git push"
	off := fmt.Sprintf("turn the rule off with: git config --add baryovm.rules.off %s", RuleGate)
	fail := func(msg string) *Verdict {
		v := refuseRule(action, RuleGate, ru.Gate, action+" refused: "+msg)
		return &v
	}
	head := commitOf(r, globals, "HEAD")
	revs, all := gateSources(p)
	var shas []string
	add := func(sha string) {
		if sha != "" && !containsStr(shas, sha) {
			shas = append(shas, sha)
		}
	}
	for _, rev := range revs {
		sha := commitOf(r, globals, rev)
		if sha == "" {
			return fail(fmt.Sprintf("cannot resolve %s to a commit to match a gate pass. Check the name, or %s", rev, off))
		}
		add(sha)
	}
	if all || p.tags {
		pat := "refs/heads"
		if !all {
			pat = "refs/tags"
		}
		out, err := r.Git(globals, "for-each-ref", "--format=%(objectname)", pat)
		if err != nil {
			return fail("cannot list the refs to match a gate pass. " + off)
		}
		for _, l := range nonEmptyLines(out) {
			add(commitOf(r, globals, strings.TrimSpace(l)))
		}
	}
	if len(shas) == 0 {
		return fail("no commit to match a gate pass. Commit first, or " + off)
	}
	recOut, _ := r.Git(globals, "config", "--get-all", "baryovm.gatepass")
	recorded := nonEmptyLines(recOut)
	for _, sha := range shas {
		if containsStr(trimAll(recorded), sha) {
			continue
		}
		short := sha[:min(8, len(sha))]
		if sha != head || env.RunGate == nil {
			return fail(fmt.Sprintf("no recorded pass of the gate %s for commit %s. Check it out and run the gate, or %s", ru.Gate, short, off))
		}
		code, err := env.RunGate(ru.Gate)
		if err != nil || code != 0 {
			return fail(fmt.Sprintf("the gate %s failed (exit %d). Fix what it found and push again, or %s", ru.Gate, code, off))
		}
		_, _ = r.Git(globals, "config", "--add", "baryovm.gatepass", sha)
	}
	return nil
}

func trimAll(l []string) []string {
	out := make([]string, len(l))
	for i, x := range l {
		out[i] = strings.TrimSpace(x)
	}
	return out
}

func ruleReset(r Runner, globals, rest []string, ru Rules, env Env, ok Verdict) Verdict {
	const action = "git reset --hard"
	if !ru.On(RuleDestructive) || !hasArg(rest, "--hard") {
		return ok
	}
	out, err := r.Git(globals, "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return refuseRule(action, RuleDestructive, "", fmt.Sprintf("%s refused: cannot read the work tree status (%v)", action, err))
	}
	lines := nonEmptyLines(out)
	if len(lines) == 0 {
		return ok
	}
	target := currentBranch(r, globals)
	if target == "" {
		target = "HEAD"
	}
	listing := []string{fmt.Sprintf("%d file(s) with uncommitted work will be lost:", len(lines))}
	listing = append(listing, capLines(lines, 20)...)
	if v := confirmTarget(env, action, RuleDestructive, target, listing); v != nil {
		return *v
	}
	return ok
}

func ruleClean(r Runner, globals, rest []string, ru Rules, env Env, ok Verdict) Verdict {
	const action = "git clean"
	if !ru.On(RuleDestructive) {
		return ok
	}
	var flags string
	var paths []string
	dry, force := false, false
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		switch {
		case a == "--":
			paths = append(paths, rest[i+1:]...)
			i = len(rest)
		case a == "--force":
			force = true
		case a == "--dry-run":
			dry = true
		case strings.HasPrefix(a, "--"):
		case strings.HasPrefix(a, "-") && len(a) > 1:
			for _, c := range a[1:] {
				switch c {
				case 'f':
					force = true
				case 'n':
					dry = true
				case 'd', 'x', 'X':
					flags += string(c)
				}
			}
		default:
			paths = append(paths, a)
		}
	}
	if dry || !force || (!strings.Contains(flags, "d") && !strings.Contains(flags, "x") && !strings.Contains(flags, "X")) {
		return ok
	}
	args := []string{"clean", "-n"}
	if flags != "" {
		args = append(args, "-"+flags)
	}
	if len(paths) > 0 {
		args = append(args, "--")
		args = append(args, paths...)
	}
	out, err := r.Git(globals, args...)
	if err != nil {
		return refuseRule(action, RuleDestructive, "", fmt.Sprintf("%s refused: cannot list what it would remove (%v)", action, err))
	}
	lines := nonEmptyLines(out)
	if len(lines) == 0 {
		return ok
	}
	top, _ := r.Git(globals, "rev-parse", "--show-toplevel")
	target := path.Base(strings.ReplaceAll(strings.TrimSpace(top), "\\", "/"))
	if target == "" || target == "." || target == "/" {
		target = "clean"
	}
	listing := []string{fmt.Sprintf("%d untracked path(s) will be removed:", len(lines))}
	listing = append(listing, capLines(lines, 20)...)
	if v := confirmTarget(env, action, RuleDestructive, target, listing); v != nil {
		return *v
	}
	return ok
}

func hasArg(args []string, name string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == name {
			return true
		}
	}
	return false
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, strings.TrimRight(l, "\r"))
		}
	}
	return out
}

func capLines(l []string, n int) []string {
	if len(l) <= n {
		return l
	}
	return append(append([]string{}, l[:n]...), fmt.Sprintf("and %d more", len(l)-n))
}

// ---- files that never go in git ----

var (
	keyExts       = []string{".pem", ".p12", ".jks", ".keystore"}
	serviceAcctRe = regexp.MustCompile(`"type"\s*:\s*"service_account"`)
)

// fileRule returns the rule name a file breaks, or "". blobRev is the revision the content is
// read from, "" for the index. A file the policy allows by path or name is skipped.
func fileRule(r Runner, globals []string, ru Rules, name, blobRev string) string {
	base := path.Base(name)
	if matchesAny(ru.AllowFiles, name) || matchesAny(ru.AllowFiles, base) {
		return ""
	}
	if base == ".env" || (strings.HasPrefix(base, ".env.") && base != ".env.example") {
		return "env-file"
	}
	lower := strings.ToLower(base)
	for _, e := range keyExts {
		if strings.HasSuffix(lower, e) {
			return "key-file"
		}
	}
	spec := blobRev + ":" + name
	if ru.MaxFileSize > 0 {
		if out, err := r.Git(globals, "cat-file", "-s", spec); err == nil {
			if n, perr := strconv.ParseInt(strings.TrimSpace(out), 10, 64); perr == nil && n > ru.MaxFileSize {
				return "too-large"
			}
		}
	}
	if strings.HasSuffix(lower, ".json") {
		if out, err := r.Git(globals, "show", spec); err == nil && serviceAcctRe.MatchString(out) {
			return "service-account"
		}
	}
	return ""
}

func fileRefusal(action string, ru Rules, fs []Finding) Verdict {
	var parts []string
	for _, f := range fs {
		parts = append(parts, f.File+" ("+f.Rule+")")
	}
	return Verdict{Action: action, Rule: RuleFiles, Findings: fs, Reason: fmt.Sprintf(
		"%s refused: files that should not be in git: %s. Remove them from the commit and gitignore them, allow a file with: git config --add baryovm.allowFiles <path>, raise the limit with: git config baryovm.maxFileSize <size> (now %d bytes), or turn the rule off with: git config --add baryovm.rules.off %s",
		action, strings.Join(parts, ", "), ru.MaxFileSize, RuleFiles)}
}

func checkFiles(r Runner, globals []string, ru Rules, names []string, blobRevs []string) []Finding {
	var fs []Finding
	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] {
			continue
		}
		seen[n] = true
		rule := ""
		for _, rev := range blobRevs {
			if rule = fileRule(r, globals, ru, n, rev); rule != "" {
				break
			}
		}
		if rule != "" {
			fs = append(fs, Finding{File: n, Rule: rule})
		}
	}
	return fs
}

func pushFiles(r Runner, globals []string, p pushArgs, remote string, ru Rules) *Verdict {
	var revs, blobRevs []string
	switch {
	case p.all || p.mirror:
		revs = []string{"--branches"}
		blobRevs = []string{"HEAD"}
	case len(p.refspecs) == 0:
		revs, blobRevs = []string{"HEAD"}, []string{"HEAD"}
	}
	if p.tags {
		revs = append(revs, "--tags")
	}
	for _, spec := range p.refspecs {
		src, _, _ := strings.Cut(strings.TrimPrefix(spec, "+"), ":")
		if src != "" && !strings.HasPrefix(src, "-") {
			revs = append(revs, src)
			blobRevs = append(blobRevs, src)
		}
	}
	if len(revs) == 0 {
		return nil
	}
	not := "--remotes"
	if !strings.ContainsAny(remote, "/:") {
		not = "--remotes=" + remote
	}
	args := append([]string{"log", "--name-only", "--format=", "--diff-filter=ACMR"}, revs...)
	args = append(args, "--not", not)
	out, err := r.Git(globals, args...)
	if err != nil {
		return nil // checkPush already refused an unreadable range
	}
	if fs := checkFiles(r, globals, ru, nonEmptyLines(out), blobRevs); len(fs) > 0 {
		v := fileRefusal("git push", ru, fs)
		return &v
	}
	return nil
}

// ---- commit ----

func ruleCommit(r Runner, globals, rest []string, ru Rules, ok Verdict) Verdict {
	const action = "git commit"
	if ru.On(RuleFiles) {
		out, err := r.Git(globals, "diff", "--cached", "--name-only", "--diff-filter=ACMR")
		if err == nil {
			if fs := checkFiles(r, globals, ru, nonEmptyLines(out), []string{""}); len(fs) > 0 {
				v := fileRefusal(action, ru, fs)
				return v
			}
		}
	}
	if ru.On(RuleText) {
		texts, err := commitTexts(r, globals, rest)
		if err != nil {
			return refuseRule(action, RuleText, "", fmt.Sprintf("%s refused: %v", action, err))
		}
		for _, t := range texts {
			if fs := CheckText(ru, t.name, t.text, true); len(fs) > 0 {
				return textRefusal(action, ru, fs)
			}
		}
	}
	return ok
}

type namedText struct{ name, text string }

// commitTexts collects the message text of a commit command: -m, --message, --trailer, and the
// -F or --file message file. CheckGit already refused stdin and unreadable files.
func commitTexts(r Runner, globals, rest []string) ([]namedText, error) {
	var out []namedText
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		switch {
		case a == "--":
			return out, nil
		case strings.HasPrefix(a, "--"):
			name, val, hasVal := strings.Cut(a, "=")
			name, _ = resolveLong(name, commitLongs)
			if !hasVal && commitValueLong[name] && i+1 < len(rest) {
				i++
				val = rest[i]
			}
			switch name {
			case "--message", "--trailer":
				out = append(out, namedText{"commit message", val})
			case "--file":
				if t, err := readMsgFile(r, globals, val); err == nil {
					out = append(out, t)
				}
			}
		case strings.HasPrefix(a, "-") && len(a) > 1:
			for j := 1; j < len(a); j++ {
				c := a[j]
				if strings.IndexByte(commitValueShort, c) < 0 {
					continue
				}
				val := a[j+1:]
				if val == "" && i+1 < len(rest) {
					i++
					val = rest[i]
				}
				switch c {
				case 'm':
					out = append(out, namedText{"commit message", val})
				case 'F':
					if t, err := readMsgFile(r, globals, val); err == nil {
						out = append(out, t)
					}
				}
				break
			}
		}
	}
	return out, nil
}

func readMsgFile(r Runner, globals []string, name string) (namedText, error) {
	if name == "-" {
		return namedText{}, fmt.Errorf("stdin")
	}
	p, err := commitFilePath(globals, name)
	if err != nil {
		return namedText{}, err
	}
	b, err := r.ReadFile(p)
	if err != nil {
		return namedText{}, err
	}
	return namedText{name, string(b)}, nil
}

// ---- commit and PR text ----

var (
	aiNames = `claude|anthropic|chatgpt|openai|copilot|gemini|codex|cursor|aider|devin|windsurf|\bgpt-?\d|noreply@anthropic\.com`
	// attribution matches a trailer or footer that credits an AI tool.
	attribution = regexp.MustCompile(`(?i)(^\s*(co-authored-by|assisted-by|generated-by|created-by)\s*:.*(` + aiNames + `))|(generated\s+(with|by|using)\b.*(` + aiNames + `|\bai\b|\bllm\b))|(made\s+with\s+(` + aiNames + `))|\x{1F916}\s*generated`)
)

// CheckText checks commit or PR text. AI attribution is refused whenever the text rule is on;
// the house style checks run only when the repository opted in with baryovm.houseStyle. For a
// commit, the first line is also checked for length and a trailing period.
func CheckText(ru Rules, name, text string, commit bool) []Finding {
	var fs []Finding
	for i, line := range strings.Split(text, "\n") {
		if attribution.MatchString(line) {
			fs = append(fs, Finding{File: name, Line: i + 1, Rule: "ai-attribution"})
		}
		if ru.HouseStyle && strings.ContainsRune(line, '\u2014') {
			fs = append(fs, Finding{File: name, Line: i + 1, Rule: "house-em-dash"})
		}
	}
	if ru.HouseStyle && commit {
		first, _, _ := strings.Cut(text, "\n")
		if len(first) > 72 {
			fs = append(fs, Finding{File: name, Line: 1, Rule: "house-subject-long"})
		}
		if strings.HasSuffix(strings.TrimSpace(first), ".") {
			fs = append(fs, Finding{File: name, Line: 1, Rule: "house-subject-period"})
		}
	}
	return fs
}

func textRefusal(action string, ru Rules, fs []Finding) Verdict {
	return Verdict{Action: action, Rule: RuleText, Findings: fs, Reason: fmt.Sprintf(
		"%s refused: text the repository does not allow: %s. Remove the attribution line or style problem and run it again, or turn the rule off with: git config --add baryovm.rules.off %s",
		action, Summarise(fs, 5), RuleText)}
}

// CheckPreCommitFiles is the pre-commit hook's file rule.
func CheckPreCommitFiles(r Runner, ru Rules) Verdict {
	const action = "pre-commit"
	if !ru.On(RuleFiles) {
		return allow(action)
	}
	g := []string{"-c", "core.quotepath=off"}
	out, err := r.Git(g, "diff", "--cached", "--name-only", "--diff-filter=ACMR")
	if err != nil {
		return allow(action) // CheckPreCommit already refused an unreadable index
	}
	if fs := checkFiles(r, g, ru, nonEmptyLines(out), []string{""}); len(fs) > 0 {
		return fileRefusal("commit", ru, fs)
	}
	return allow(action)
}

// CheckCommitMsgText is the commit-msg hook's text rule.
func CheckCommitMsgText(r Runner, file string, ru Rules) Verdict {
	if !ru.On(RuleText) {
		return allow("commit-msg")
	}
	b, err := r.ReadFile(file)
	if err != nil {
		return allow("commit-msg") // CheckCommitMsg already refused an unreadable file
	}
	var kept []string
	for _, l := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(l, "#") { // git comment lines are not part of the message
			kept = append(kept, l)
		}
	}
	if fs := CheckText(ru, "commit message", strings.Join(kept, "\n"), true); len(fs) > 0 {
		return textRefusal("commit", ru, fs)
	}
	return allow("commit-msg")
}

// aliasWords splits an alias the way git does: spaces separate words, single and double quotes
// group them, and a backslash escapes the next character.
func aliasWords(s string) []string {
	var out []string
	var cur strings.Builder
	in, quote, esc := false, rune(0), false
	for _, c := range s {
		switch {
		case esc:
			cur.WriteRune(c)
			esc = false
		case c == '\\' && quote != '\'':
			esc = true
			in = true
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				cur.WriteRune(c)
			}
		case c == '"' || c == '\'':
			quote, in = c, true
		case c == ' ' || c == '\t' || c == '\n':
			if in {
				out = append(out, cur.String())
				cur.Reset()
				in = false
			}
		default:
			cur.WriteRune(c)
			in = true
		}
	}
	if in {
		out = append(out, cur.String())
	}
	return out
}
