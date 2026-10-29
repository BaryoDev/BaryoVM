// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package ship

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Gate check names, stable for -o json consumers.
const (
	CheckVersion    = "version"
	CheckTree       = "clean-tree"
	CheckBranch     = "default-branch"
	CheckSynced     = "synced"
	CheckRepo       = "repo"
	CheckTag        = "tag"
	CheckChangelog  = "changelog"
	CheckNotes      = "release-notes"
	CheckPublished  = "not-published"
	CheckDeployed   = "deployed"
	checkNotChecked = "not checked: no version"
)

// Check is one gate result. Reason is set only on a refusal and names the specific cause.
type Check struct {
	Name     string `json:"name"`
	OK       bool   `json:"ok"`
	Observed string `json:"observed,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// Timings bounds every outbound call and every wait. Zero values take the defaults.
type Timings struct {
	Call    time.Duration // one outbound call
	RunWait time.Duration // for the tag push to start a run
	Watch   time.Duration // for the run to finish
	Index   time.Duration // for the registry to index every package
	Poll    time.Duration // between polls
}

func (t Timings) withDefaults() Timings {
	def := func(v *time.Duration, d time.Duration) {
		if *v <= 0 {
			*v = d
		}
	}
	def(&t.Call, 30*time.Second)
	def(&t.RunWait, 2*time.Minute)
	def(&t.Watch, 30*time.Minute)
	def(&t.Index, 15*time.Minute)
	def(&t.Poll, 10*time.Second)
	return t
}

// Deps are the seams. Dir is the repository.
type Deps struct {
	Dir      string
	Git      Git
	Registry Registry
	ReadFile func(path string) ([]byte, error)
	// GitHub builds the API client. It is called only after the gate passed and only for a real
	// run, so a dry run needs no token and cannot reach the API.
	GitHub func() (GitHub, error)
	// Deployed returns the commit the named stack last released, or found false when it has none.
	Deployed func(ctx context.Context, stack string) (commit string, found bool, err error)
	// Confirm shows the diff summary and answers whether to go on. Nil means no confirmation.
	Confirm func(summary string) (bool, error)
	Now     func() time.Time
	Sleep   func(time.Duration)
	Timings Timings
}

func (d Deps) call(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, d.Timings.withDefaults().Call)
}

func (d Deps) git(ctx context.Context, args ...string) (string, error) {
	c, cancel := d.call(ctx)
	defer cancel()
	return d.Git.Run(c, d.Dir, args...)
}

// gateState is what the gate learned and later stages reuse.
type gateState struct {
	version  string
	tag      string
	head     string
	repo     string
	packages []string
	// tag position: present locally, present on the remote. Both only when equal to head.
	haveLocal, haveRemote bool
	notes                 string
}

var githubRemoteRe = regexp.MustCompile(`github\.com[:/]([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+?)(?:\.git)?/?$`)

func short(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

// preflight runs every gate check and reports all of them: a refusal names each cause, not only
// the first. It changes nothing.
func preflight(ctx context.Context, d Deps, s Spec, wantVersion string) ([]Check, *gateState) {
	st := &gateState{}
	var checks []Check
	add := func(c Check) { checks = append(checks, c) }
	fail := func(name, format string, a ...any) { add(Check{Name: name, Reason: fmt.Sprintf(format, a...)}) }
	ok := func(name, observed string) { add(Check{Name: name, OK: true, Observed: observed}) }
	remote := s.remote()

	// version
	var verFile []byte
	if b, err := d.ReadFile(filepath.Join(d.Dir, filepath.FromSlash(s.VersionFrom))); err != nil {
		fail(CheckVersion, "cannot read versionFrom %s: %v", s.VersionFrom, err)
	} else if v, err := d.Registry.Version(b); err != nil {
		fail(CheckVersion, "%s yields no version: %v", s.VersionFrom, err)
	} else if wantVersion != "" && wantVersion != v {
		fail(CheckVersion, "%s says %s but %s was asked for: bump the file first, or ask for %s", s.VersionFrom, v, wantVersion, v)
	} else {
		verFile, st.version = b, v
		st.tag = s.TagFor(v)
		ok(CheckVersion, v)
	}

	// clean tree
	if out, err := d.git(ctx, "status", "--porcelain"); err != nil {
		fail(CheckTree, "cannot read the working tree: %v", err)
	} else if lines := nonEmpty(out); len(lines) > 0 {
		fail(CheckTree, "working tree is not clean: %d path(s) changed, first %q: commit or stash them", len(lines), strings.TrimSpace(lines[0]))
	} else {
		ok(CheckTree, "clean")
	}

	// head, default branch, synced
	headOut, herr := d.git(ctx, "rev-parse", "HEAD")
	st.head = strings.TrimSpace(headOut)
	cur, cerr := d.git(ctx, "rev-parse", "--abbrev-ref", "HEAD")
	cur = strings.TrimSpace(cur)
	branch := s.Branch
	if branch == "" {
		if out, err := d.git(ctx, "symbolic-ref", "--short", "refs/remotes/"+remote+"/HEAD"); err == nil {
			branch = strings.TrimPrefix(strings.TrimSpace(out), remote+"/")
		}
	}
	switch {
	case herr != nil || cerr != nil:
		fail(CheckBranch, "cannot read HEAD: %v", firstErr(herr, cerr))
	case branch == "":
		fail(CheckBranch, "cannot tell the default branch of %s: set publish.branch", remote)
	case cur != branch:
		fail(CheckBranch, "on branch %q, not the default branch %q: switch to it first", cur, branch)
	default:
		ok(CheckBranch, branch)
	}
	switch {
	case herr != nil || branch == "":
		fail(CheckSynced, "not checked: HEAD or the default branch is unknown")
	default:
		out, err := d.git(ctx, "ls-remote", remote, "refs/heads/"+branch)
		f := strings.Fields(out)
		switch {
		case err != nil:
			fail(CheckSynced, "cannot read %s: %v", remote, err)
		case len(f) == 0:
			fail(CheckSynced, "%s has no branch %s: push it first", remote, branch)
		case f[0] != st.head:
			fail(CheckSynced, "HEAD %s differs from %s/%s at %s: pull or push so they match", short(st.head), remote, branch, short(f[0]))
		default:
			ok(CheckSynced, short(st.head))
		}
	}

	// repo
	st.repo = s.Repo
	if st.repo == "" {
		if out, err := d.git(ctx, "remote", "get-url", remote); err != nil {
			fail(CheckRepo, "cannot read the url of %s: %v", remote, err)
		} else if m := githubRemoteRe.FindStringSubmatch(strings.TrimSpace(out)); m != nil {
			st.repo = m[1] + "/" + m[2]
		} else {
			fail(CheckRepo, "%s is not a GitHub remote (%s): set publish.repo", remote, strings.TrimSpace(out))
		}
	}
	if st.repo != "" {
		ok(CheckRepo, st.repo)
	}

	if st.version == "" {
		for _, n := range []string{CheckTag, CheckChangelog, CheckNotes, CheckPublished} {
			fail(n, checkNotChecked)
		}
	} else {
		tagCheck(ctx, d, s, st, &checks)
		// changelog
		if b, err := d.ReadFile(filepath.Join(d.Dir, filepath.FromSlash(s.changelog()))); err != nil {
			fail(CheckChangelog, "cannot read %s: %v", s.changelog(), err)
		} else if body, found := changelogSection(string(b), st.version); !found {
			fail(CheckChangelog, "%s has no section for %s: add a heading such as \"## %s\" before releasing", s.changelog(), st.version, st.version)
		} else if body == "" {
			fail(CheckChangelog, "%s has a heading for %s but nothing under it", s.changelog(), st.version)
		} else {
			st.notes = body
			ok(CheckChangelog, s.changelog())
		}
		// release notes field
		if text, present := d.Registry.ReleaseNotes(verFile); !present {
			ok(CheckNotes, "the registry has no release notes field here")
		} else if !strings.Contains(text, st.version) {
			fail(CheckNotes, "PackageReleaseNotes in %s does not mention %s: update it, it is the only changelog a version comparison shows", s.VersionFrom, st.version)
		} else {
			ok(CheckNotes, "mentions "+st.version)
		}
		// published
		st.packages = s.Packages
		if len(st.packages) == 0 {
			if n := d.Registry.PackageName(s.VersionFrom, verFile); n != "" {
				st.packages = []string{n}
			}
		}
		if len(st.packages) == 0 {
			fail(CheckPublished, "no package name: %s declares none, set publish.packages", s.VersionFrom)
		} else {
			var already, errs []string
			for _, p := range st.packages {
				c, cancel := d.call(ctx)
				pub, err := d.Registry.Published(c, p, st.version)
				cancel()
				switch {
				case err != nil:
					errs = append(errs, fmt.Sprintf("%s: %v", p, err))
				case pub:
					already = append(already, p)
				}
			}
			resume := st.haveLocal || st.haveRemote
			switch {
			case len(errs) > 0:
				fail(CheckPublished, "cannot ask the registry: %s", strings.Join(errs, "; "))
			case len(already) > 0 && !resume:
				fail(CheckPublished, "%s %s is already published: a version cannot be published twice, bump it", strings.Join(already, ", "), st.version)
			case len(already) > 0:
				ok(CheckPublished, "already published: "+strings.Join(already, ", ")+" (resuming)")
			default:
				ok(CheckPublished, "none of "+strings.Join(st.packages, ", ")+" is published")
			}
		}
	}

	// deployed
	switch {
	case s.RequireDeployed == "":
		ok(CheckDeployed, "not required")
	case d.Deployed == nil:
		fail(CheckDeployed, "cannot read the release record of %s here", s.RequireDeployed)
	default:
		c, cancel := d.call(ctx)
		commit, found, err := d.Deployed(c, s.RequireDeployed)
		cancel()
		switch {
		case err != nil:
			fail(CheckDeployed, "cannot read the release record of stack %s: %v", s.RequireDeployed, err)
		case !found:
			fail(CheckDeployed, "stack %s has never finished a release: release it from this commit first", s.RequireDeployed)
		case st.head == "" || (commit != st.head && !strings.HasPrefix(st.head, commit)):
			fail(CheckDeployed, "stack %s last released %s, not this source (%s): release it from this commit first", s.RequireDeployed, short(commit), short(st.head))
		default:
			ok(CheckDeployed, s.RequireDeployed+" released "+short(commit))
		}
	}
	return checks, st
}

// tagCheck decides the tag's state. Absent is fine. Present at HEAD is a resume. Present anywhere
// else is a refusal, because a version number is spent the moment it is used.
func tagCheck(ctx context.Context, d Deps, s Spec, st *gateState, checks *[]Check) {
	remote := s.remote()
	refuse := func(format string, a ...any) {
		*checks = append(*checks, Check{Name: CheckTag, Reason: fmt.Sprintf(format, a...)})
	}
	local, lerr := d.git(ctx, "rev-parse", "-q", "--verify", "refs/tags/"+st.tag+"^{commit}")
	local = strings.TrimSpace(local)
	if lerr != nil {
		local = "" // an unknown tag is a non-zero exit
	}
	out, err := d.git(ctx, "ls-remote", "--tags", remote, "refs/tags/"+st.tag, "refs/tags/"+st.tag+"^{}")
	if err != nil {
		refuse("cannot read the tags on %s: %v", remote, err)
		return
	}
	rsha := ""
	for _, l := range nonEmpty(out) {
		f := strings.Fields(l)
		if len(f) < 2 {
			continue
		}
		if f[1] == "refs/tags/"+st.tag+"^{}" || (rsha == "" && f[1] == "refs/tags/"+st.tag) {
			rsha = f[0]
		}
	}
	for _, where := range []struct{ name, sha string }{{"locally", local}, {"on " + remote, rsha}} {
		if where.sha != "" && where.sha != st.head {
			refuse("tag %s already exists %s at %s, not at HEAD %s: a version number is spent once it is used, bump the version", st.tag, where.name, short(where.sha), short(st.head))
			return
		}
	}
	st.haveLocal, st.haveRemote = local != "", rsha != ""
	obs := "no tag " + st.tag + " yet"
	if st.haveLocal || st.haveRemote {
		obs = "tag " + st.tag + " is at HEAD: resuming"
	}
	*checks = append(*checks, Check{Name: CheckTag, OK: true, Observed: obs})
}

func nonEmpty(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}
