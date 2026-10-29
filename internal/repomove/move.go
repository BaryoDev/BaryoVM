// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package repomove

import (
	"fmt"
	"strings"
)

// Options is one move.
type Options struct {
	Stack     string
	From, To  Repo
	Create    bool                   // create the destination when it does not exist
	ExportDir string                 // where issues.json and pulls.json go
	LocalDir  string                 // the local clone to repoint; empty skips it
	VMs       []VMTarget             // VMs searched for checkouts
	CredUser  string                 // user name for the read-only credential
	Cred      func() (string, error) // resolves the read-only token; nil means none
}

// Deps are the seams. Nothing in this package runs a program or opens a connection except through
// them.
type Deps struct {
	Exec    Exec
	TempDir func() (string, func(), error)
	Dial    func(vm string) (Remote, error)
	// Record stores the new source in the fleet and reports whether it changed anything. It runs
	// last.
	Record func(to string) (changed bool, err error)
}

// Report is everything a move found and did, for the -o json envelope.
type Report struct {
	Stack        string        `json:"stack"`
	From         string        `json:"from"`
	To           string        `json:"to"`
	Refs         int           `json:"refs"`
	AlreadyMoved bool          `json:"alreadyMoved,omitempty"`
	Created      bool          `json:"created,omitempty"`
	Mirrored     bool          `json:"mirrored,omitempty"`
	Export       *ExportReport `json:"export,omitempty"`
	Checkouts    []Checkout    `json:"checkouts"`
	References   []Reference   `json:"references"`
	Local        string        `json:"local,omitempty"` // what happened to the local clone
	Recorded     bool          `json:"recorded,omitempty"`
	Done         []string      `json:"done"`
	Notes        []string      `json:"notes,omitempty"`
	NextCommands []string      `json:"nextCommands,omitempty"`
}

func (r *Report) did(format string, a ...any)  { r.Done = append(r.Done, fmt.Sprintf(format, a...)) }
func (r *Report) note(format string, a ...any) { r.Notes = append(r.Notes, fmt.Sprintf(format, a...)) }

func lsRemote(ex Exec, url string) (Refs, string, int, error) {
	argv := LsRemoteArgv(url)
	out, exit, err := ex(argv)
	if err != nil || exit != 0 {
		return nil, out, exit, callError(argv, out, exit, err)
	}
	return ParseRefs(out), out, exit, nil
}

// Run moves the repository. Everything before the first write is read-only; the writes run
// outward-in (export, create, mirror, compare) and only then touch anything that pulls from the old
// remote. A failure at any step leaves every old remote where it was: the steps that repoint undo
// themselves, in reverse, when a later one fails.
func Run(o Options, d Deps) (*Report, error) {
	rep := &Report{Stack: o.Stack, From: o.From.URL, To: o.To.URL, Checkouts: []Checkout{}, References: []Reference{}, Done: []string{}}
	if Same(o.From, o.To) {
		rep.AlreadyMoved = true
		rep.note("the source and the destination are the same repository: nothing to move")
		return rep, nil
	}

	// Read-only: both sides.
	srcRefs, _, _, err := lsRemote(d.Exec, o.From.URL)
	if err != nil {
		return rep, fmt.Errorf("cannot read the source repository: %w", err)
	}
	if len(srcRefs) == 0 {
		return rep, fmt.Errorf("the source repository has no branches or tags: nothing to move")
	}
	rep.Refs = len(srcRefs)

	identical, needCreate := false, false
	dstRefs, out, exit, derr := lsRemote(d.Exec, o.To.URL)
	switch {
	case derr == nil && len(dstRefs) > 0:
		if diff := Diff(srcRefs, dstRefs); !diff.Empty() {
			return rep, fmt.Errorf("the destination already has commits and they differ from the source (%s): baryovm never force-pushes, so empty it or pick another repository", diff)
		}
		identical = true
	case derr == nil:
		// exists and empty: ready for the mirror
	case exit == -1:
		return rep, derr
	case o.Create:
		needCreate = true
	default:
		_ = out
		return rep, fmt.Errorf("cannot read the destination (%v): if it does not exist yet, create it empty or run again with --create", derr)
	}

	// Read-only: the local clone and the VMs.
	localAction, err := inspectLocal(d.Exec, o)
	if err != nil {
		return rep, err
	}
	checkouts, refs, err := discover(o, d)
	if err != nil {
		return rep, err
	}
	rep.Checkouts, rep.References = checkouts, refs
	var token string
	var useCred bool
	if o.Cred != nil && strings.HasPrefix(o.To.URL, "https://") {
		for _, c := range checkouts {
			if !c.Dirty && !c.AlreadyNew && c.Skipped == "" {
				useCred = true
			}
		}
		if useCred {
			if token, err = o.Cred(); err != nil {
				return rep, fmt.Errorf("--read-cred: %w", err)
			}
		}
	}
	if o.Cred != nil && !strings.HasPrefix(o.To.URL, "https://") {
		rep.note("--read-cred is for https remotes: a checkout on an ssh remote needs a deploy key on its VM")
	}

	// Writes, outward first.
	if o.From.Kind == GitHub {
		if identical && ExportPresent(o.ExportDir) {
			rep.note("the export in %s is already there and was left alone", o.ExportDir)
		} else {
			e, err := ExportGitHub(d.Exec, o.From)
			if err != nil {
				return rep, err
			}
			if err := e.Write(o.ExportDir); err != nil {
				return rep, fmt.Errorf("write the export: %w", err)
			}
			rep.Export = &e.Report
			rep.did("exported %d issues and %d pull requests to %s", e.Report.Issues, e.Report.PullRequests, o.ExportDir)
		}
	} else {
		rep.note("issues and pull requests were not exported: only a GitHub source can be, and %s is not one", o.From.Host)
	}

	if needCreate {
		argv, err := CreateArgv(o.To)
		if err != nil {
			return rep, err
		}
		out, exit, err := d.Exec(argv)
		if err != nil || exit != 0 {
			return rep, fmt.Errorf("create the destination: %w", callError(argv, out, exit, err))
		}
		rep.Created = true
		rep.did("created %s", o.To.URL)
	}

	if identical {
		rep.did("the destination already matches the source (%d refs): no push", len(srcRefs))
	} else {
		if err := Mirror(d.Exec, d.TempDir, o.From.URL, o.To.URL); err != nil {
			return rep, fmt.Errorf("mirror failed, every remote still points at the old repository: %w", err)
		}
		rep.Mirrored = true
		rep.did("pushed every branch and tag to %s", o.To.URL)
	}

	// Compare again, from the hosts, before anything is repointed.
	now, _, _, err := lsRemote(d.Exec, o.From.URL)
	if err != nil {
		return rep, fmt.Errorf("cannot re-read the source to compare: %w", err)
	}
	got, _, _, err := lsRemote(d.Exec, o.To.URL)
	if err != nil {
		return rep, fmt.Errorf("cannot re-read the destination to compare: %w", err)
	}
	if diff := Diff(now, got); !diff.Empty() {
		return rep, fmt.Errorf("the two ref lists differ (%s): not repointing anything, so every remote still points at the old repository", diff)
	}
	rep.did("compared the ref lists: %d refs on both sides", len(now))

	// Repoint, with undo.
	var undo []func()
	fail := func(err error) (*Report, error) {
		for i := len(undo) - 1; i >= 0; i-- {
			undo[i]()
		}
		return rep, fmt.Errorf("%w (the old remote was put back where it could be)", err)
	}
	conns := map[string]Remote{}
	sudoOf := map[string]bool{}
	for _, v := range o.VMs {
		sudoOf[v.Name] = v.Sudo
	}
	for i := range rep.Checkouts {
		c := &rep.Checkouts[i]
		if c.Dirty || c.AlreadyNew || c.Skipped != "" {
			continue
		}
		r := conns[c.VM]
		if r == nil {
			if r, err = d.Dial(c.VM); err != nil {
				return fail(fmt.Errorf("connect to %s: %w", c.VM, err))
			}
			conns[c.VM] = r
		}
		sudo := sudoOf[c.VM]
		owner := c.Owner
		if _, err := r.Run(SetURLCmd(sudo, owner, c.Dir, o.To.URL)); err != nil {
			return fail(fmt.Errorf("repoint %s:%s: %w", c.VM, c.Dir, scrub(err, token)))
		}
		old := c.Origin
		undo = append(undo, func() { _, _ = r.Run(SetURLCmd(sudo, owner, c.Dir, old)) })
		c.Repointed = true
		if useCred {
			helper, err := CredHelperCmd(sudo, owner, c.Dir)
			if err != nil {
				return fail(err)
			}
			approve, err := CredApproveCmd(sudo, owner, c.Dir)
			if err != nil {
				return fail(err)
			}
			if _, err := r.Run(helper); err != nil {
				return fail(fmt.Errorf("set the credential helper on %s:%s: %w", c.VM, c.Dir, scrub(err, token)))
			}
			in := strings.NewReader(CredInput(o.To.Host, o.CredUser, token))
			if _, err := r.RunStdin(approve, in); err != nil {
				return fail(fmt.Errorf("store the read-only credential on %s:%s: %w", c.VM, c.Dir, scrub(err, token)))
			}
			c.CredStored = true
		}
		rep.did("repointed origin of %s:%s", c.VM, c.Dir)
	}

	switch localAction {
	case "repoint":
		for _, argv := range [][]string{
			{"git", "-C", o.LocalDir, "remote", "rename", "origin", "old-origin"},
			{"git", "-C", o.LocalDir, "remote", "add", "origin", o.To.URL},
		} {
			if out, exit, err := d.Exec(argv); err != nil || exit != 0 {
				if argv[4] == "add" {
					_, _, _ = d.Exec([]string{"git", "-C", o.LocalDir, "remote", "rename", "old-origin", "origin"})
				}
				return fail(fmt.Errorf("repoint the local clone: %w", callError(argv, out, exit, err)))
			}
		}
		undo = append(undo, func() {
			_, _, _ = d.Exec([]string{"git", "-C", o.LocalDir, "remote", "remove", "origin"})
			_, _, _ = d.Exec([]string{"git", "-C", o.LocalDir, "remote", "rename", "old-origin", "origin"})
		})
		rep.Local = "origin now points at the new remote, the old one is kept as old-origin"
		rep.did("repointed the local clone %s (old remote kept as old-origin)", o.LocalDir)
	case "already":
		rep.Local = "origin already points at the new remote"
	default:
		rep.Local = localAction
	}

	if d.Record != nil {
		changed, err := d.Record(o.To.URL)
		if err != nil {
			return fail(fmt.Errorf("record the new source for the stack: %w", err))
		}
		if changed {
			rep.Recorded = true
			rep.did("recorded %s as the source of stack %s", o.To.URL, o.Stack)
		}
	}

	rep.NextCommands = OldRepoCommands(o.From)
	if localAction == "repoint" || localAction == "already" {
		rep.NextCommands = append(rep.NextCommands, "git -C "+o.LocalDir+" remote remove old-origin")
	}
	return rep, nil
}

// under reports whether dir is one of the roots or inside one, by whole path elements.
func under(dir string, roots []string) bool {
	for _, r := range roots {
		r = strings.TrimRight(r, "/")
		if dir == r || strings.HasPrefix(dir, r+"/") {
			return true
		}
	}
	return false
}

func scrub(err error, token string) error {
	if token == "" || err == nil {
		return err
	}
	return fmt.Errorf("%s", strings.ReplaceAll(err.Error(), token, "***"))
}

// inspectLocal decides what to do with the local clone: "repoint", "already", or a sentence saying
// why it is left alone. It only reads. An existing old-origin is refused up front, before any
// write, since the rename would fail halfway through.
func inspectLocal(ex Exec, o Options) (string, error) {
	if o.LocalDir == "" {
		return "no local clone given: left alone", nil
	}
	out, exit, err := ex([]string{"git", "-C", o.LocalDir, "remote", "get-url", "origin"})
	if err != nil || exit != 0 {
		return "the local directory has no origin: left alone", nil
	}
	cur, perr := Parse(strings.TrimSpace(out))
	if perr != nil {
		return "the local origin is not a URL baryovm reads: left alone", nil
	}
	switch {
	case Same(cur, o.To):
		return "already", nil
	case !Same(cur, o.From):
		return "the local origin points somewhere else: left alone", nil
	}
	if _, exit, err := ex([]string{"git", "-C", o.LocalDir, "remote", "get-url", "old-origin"}); err == nil && exit == 0 {
		return "", fmt.Errorf("the local clone already has a remote named old-origin: remove it with `git -C %s remote remove old-origin` and run the move again", o.LocalDir)
	}
	return "repoint", nil
}

// discover finds the checkouts of the source repository on the VMs and the cron and systemd lines
// that run from them. Read-only.
func discover(o Options, d Deps) ([]Checkout, []Reference, error) {
	var checkouts []Checkout
	var refs []Reference
	for _, v := range o.VMs {
		r, err := d.Dial(v.Name)
		if err != nil {
			return nil, nil, fmt.Errorf("connect to %s: %w", v.Name, err)
		}
		// Only the directories of stacks registered on the VM are searched. A checkout anywhere
		// else is not ours to run git in.
		if len(v.Roots) == 0 {
			continue
		}
		out, err := r.Run(FindCmd(v.Sudo, v.Roots))
		if err != nil {
			return nil, nil, fmt.Errorf("search %s for checkouts: %w", v.Name, err)
		}
		for _, dir := range ParseFinds(out) {
			if !under(dir, v.Roots) {
				continue
			}
			ow, err := r.Run(OwnerCmd(v.Sudo, dir))
			owner, oerr := ParseOwner(ow)
			if err != nil || oerr != nil {
				if oerr == nil {
					oerr = err
				}
				checkouts = append(checkouts, Checkout{VM: v.Name, Dir: dir, Skipped: "refused, no git command was run: " + oerr.Error()})
				continue
			}
			if owner != "root" && owner != v.User {
				checkouts = append(checkouts, Checkout{VM: v.Name, Dir: dir, Owner: owner, Skipped: "needs attention, no git command was run: owned by " + owner + ", not root or the SSH user " + v.User})
				continue
			}
			url, err := r.Run(GetURLCmd(v.Sudo, owner, dir))
			if err != nil {
				continue // a .git with no origin is not ours
			}
			cur, err := Parse(strings.TrimSpace(url))
			if err != nil {
				continue
			}
			c := Checkout{VM: v.Name, Dir: dir, Origin: strings.TrimSpace(url), Owner: owner}
			switch {
			case Same(cur, o.To):
				c.AlreadyNew = true
			case Same(cur, o.From):
				st, err := r.Run(StatusCmd(v.Sudo, owner, dir))
				if err != nil {
					return nil, nil, fmt.Errorf("read the status of %s:%s: %w", v.Name, dir, err)
				}
				c.Dirty = strings.TrimSpace(st) != ""
				if c.Dirty {
					c.Skipped = "has local changes: left alone, commit or stash them and run the move again"
				}
			default:
				continue
			}
			checkouts = append(checkouts, c)
			ro, err := r.Run(ReferencesCmd(v.Sudo, dir))
			if err != nil {
				return nil, nil, fmt.Errorf("look for cron and systemd references on %s: %w", v.Name, err)
			}
			refs = append(refs, ParseReferences(v.Name, dir, ro)...)
		}
	}
	return checkouts, refs, nil
}
