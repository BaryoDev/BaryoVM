// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package repomove

import (
	"fmt"
	"sort"
	"strings"
)

// Exec runs one program on the operator's machine as an argv (no shell) and returns its combined
// output and exit code. exit is -1 when the program could not start.
type Exec func(argv []string) (out string, exit int, err error)

// Refs maps a ref name (refs/heads/main, refs/tags/v1) to its object id.
type Refs map[string]string

// LsRemoteArgv lists the branches and tags of a remote.
func LsRemoteArgv(url string) []string {
	return []string{"git", "ls-remote", "--heads", "--tags", url}
}

// ParseRefs reads `git ls-remote` output. Lines that are not "<sha><TAB><ref>" (warnings the
// combined output carries) are skipped. A peeled tag line (name^{}) is kept under its own name,
// so an annotated tag compares by both its object and what it points at.
func ParseRefs(out string) Refs {
	refs := Refs{}
	for _, line := range strings.Split(out, "\n") {
		sha, ref, ok := strings.Cut(strings.TrimRight(line, "\r"), "\t")
		if !ok || len(sha) < 40 || !strings.HasPrefix(ref, "refs/") {
			continue
		}
		refs[ref] = sha
	}
	return refs
}

// RefDiff is what separates two ref lists.
type RefDiff struct {
	Missing []string `json:"missing,omitempty"` // on the old side, not on the new
	Changed []string `json:"changed,omitempty"` // on both, at different commits
	Extra   []string `json:"extra,omitempty"`   // on the new side only
}

// Empty reports whether the two lists are the same.
func (d RefDiff) Empty() bool { return len(d.Missing)+len(d.Changed)+len(d.Extra) == 0 }

// OnlyMissing reports whether the new side is a strict subset of the old: some refs are not there
// yet and nothing it holds differs or is extra. That is what a push cut off halfway leaves.
func (d RefDiff) OnlyMissing() bool {
	return len(d.Missing) > 0 && len(d.Changed) == 0 && len(d.Extra) == 0
}

// Diff compares the old side's refs with the new side's.
func Diff(old, now Refs) RefDiff {
	var d RefDiff
	for ref, sha := range old {
		got, ok := now[ref]
		switch {
		case !ok:
			d.Missing = append(d.Missing, ref)
		case got != sha:
			d.Changed = append(d.Changed, ref)
		}
	}
	for ref := range now {
		if _, ok := old[ref]; !ok {
			d.Extra = append(d.Extra, ref)
		}
	}
	sort.Strings(d.Missing)
	sort.Strings(d.Changed)
	sort.Strings(d.Extra)
	return d
}

func (d RefDiff) String() string {
	var parts []string
	if len(d.Missing) > 0 {
		parts = append(parts, "missing on the new side: "+strings.Join(d.Missing, ", "))
	}
	if len(d.Changed) > 0 {
		parts = append(parts, "at a different commit: "+strings.Join(d.Changed, ", "))
	}
	if len(d.Extra) > 0 {
		parts = append(parts, "only on the new side: "+strings.Join(d.Extra, ", "))
	}
	return strings.Join(parts, "; ")
}

// MirrorCloneArgv and PushArgv copy every branch and tag. The push names the ref patterns instead of
// using --mirror: --mirror force-updates and deletes refs on the destination, and this move never
// does either. A destination that already holds a ref is refused before this runs.
func MirrorCloneArgv(from, dir string) []string {
	return []string{"git", "clone", "--mirror", from, dir}
}

// PushArgv pushes every branch and tag of the scratch mirror in dir to the new remote.
func PushArgv(dir, to string) []string {
	return []string{"git", "-C", dir, "push", to, "refs/heads/*:refs/heads/*", "refs/tags/*:refs/tags/*"}
}

// Mirror clones from into a scratch directory and pushes every branch and tag to to.
func Mirror(ex Exec, tempDir func() (string, func(), error), from, to string) error {
	dir, cleanup, err := tempDir()
	if err != nil {
		return err
	}
	defer cleanup()
	for _, argv := range [][]string{MirrorCloneArgv(from, dir), PushArgv(dir, to)} {
		out, exit, err := ex(argv)
		if err != nil || exit != 0 {
			return callError(argv, out, exit, err)
		}
	}
	return nil
}

// callError names the program and the last line it said. A program's own output is trimmed to one
// line so a long dump cannot carry anything along.
func callError(argv []string, out string, exit int, err error) error {
	if exit == -1 {
		return fmt.Errorf("%s could not run (is it installed and on PATH?)", argv[0])
	}
	msg := strings.TrimSpace(out)
	if i := strings.LastIndex(msg, "\n"); i >= 0 {
		msg = strings.TrimSpace(msg[i+1:])
	}
	if msg == "" && err != nil {
		msg = err.Error()
	}
	return fmt.Errorf("%s exited %d: %s", strings.Join(argv[:min(len(argv), 2)], " "), exit, msg)
}
