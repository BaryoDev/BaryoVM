// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package release

import (
	"fmt"
	"strings"
)

// rule is one rsync exclude pattern, parsed.
type rule struct {
	pattern  string // without the leading and trailing slash
	anchored bool   // leading "/": match from the transfer root
	dirOnly  bool   // trailing "/": match directories only
	fullPath bool   // contains "/" or "**": match more than the last component
}

// matcher applies a manifest's excludes the way rsync does, for the subset the tar transport
// supports: `*`, `?` and `**` globs, a leading "/" to anchor, a trailing "/" for directories.
// prefix is the name rsync puts in front of every path when the entry has no trailing slash
// ("out" syncs to out/...), so anchored patterns see the same paths rsync does.
type matcher struct {
	rules  []rule
	prefix string
}

// newMatcher refuses a pattern it cannot match faithfully rather than guess: a guess that
// ships a file rsync would have excluded can put a secret on the host.
func newMatcher(patterns []string, prefix string) (*matcher, error) {
	mt := &matcher{prefix: prefix}
	for _, p := range patterns {
		if p == "" {
			continue
		}
		if strings.ContainsAny(p, `[\`) || strings.Contains(p, "***") ||
			strings.HasPrefix(p, "!") || strings.HasPrefix(p, "+ ") || strings.HasPrefix(p, "- ") {
			return nil, fmt.Errorf("exclude %q uses a pattern feature the tar transport does not support "+
				"(character classes, escapes, ***, filter rules); simplify it or install rsync", p)
		}
		if strings.HasPrefix(p, "//") {
			return nil, fmt.Errorf("exclude %q starts with two slashes, which rsync does not treat as /%s; "+
				"write it with one", p, strings.TrimLeft(p, "/"))
		}
		r := rule{}
		if strings.HasPrefix(p, "/") {
			r.anchored = true
			p = strings.TrimLeft(p, "/")
		}
		if strings.HasSuffix(p, "/") {
			r.dirOnly = true
			p = strings.TrimRight(p, "/")
		}
		if p == "" {
			continue
		}
		r.pattern = p
		r.fullPath = strings.Contains(p, "/") || strings.Contains(p, "**")
		mt.rules = append(mt.rules, r)
	}
	return mt, nil
}

// excluded reports whether rel (slash separated, relative to the destination) is excluded, or
// sits inside an excluded directory.
func (mt *matcher) excluded(rel string, isDir bool) bool {
	if mt == nil || len(mt.rules) == 0 {
		return false
	}
	full := rel
	if mt.prefix != "" {
		full = mt.prefix + "/" + rel
		if rel == "" {
			full = mt.prefix
		}
	}
	parts := strings.Split(full, "/")
	for i := range parts {
		sub := strings.Join(parts[:i+1], "/")
		dir := isDir || i < len(parts)-1
		for _, r := range mt.rules {
			if r.matches(sub, dir) {
				return true
			}
		}
	}
	return false
}

func (r rule) matches(path string, isDir bool) bool {
	if r.dirOnly && !isDir {
		return false
	}
	switch {
	case r.anchored:
		return glob(r.pattern, path)
	case r.fullPath:
		if globLead(r.pattern, path) {
			return true
		}
		for i := 0; i < len(path); i++ {
			if path[i] == '/' && globLead(r.pattern, path[i+1:]) {
				return true
			}
		}
		return false
	default:
		last := path
		if i := strings.LastIndexByte(path, '/'); i >= 0 {
			last = path[i+1:]
		}
		return glob(r.pattern, last)
	}
}

// globLead is glob, except that a leading "**/" also matches zero directories, so "**/.env"
// excludes a top-level .env the way rsync does.
func globLead(p, s string) bool {
	for {
		if glob(p, s) {
			return true
		}
		if !strings.HasPrefix(p, "**/") {
			return false
		}
		p = p[3:]
	}
}

// glob matches s against p where `*` and `?` stop at "/" and `**` does not.
func glob(p, s string) bool {
	for len(p) > 0 {
		switch {
		case strings.HasPrefix(p, "**"):
			rest := strings.TrimLeft(p, "*")
			for i := 0; i <= len(s); i++ {
				if glob(rest, s[i:]) {
					return true
				}
			}
			return false
		case p[0] == '*':
			rest := p[1:]
			for i := 0; i <= len(s); i++ {
				if glob(rest, s[i:]) {
					return true
				}
				if i < len(s) && s[i] == '/' {
					break
				}
			}
			return false
		case p[0] == '?':
			if len(s) == 0 || s[0] == '/' {
				return false
			}
			p, s = p[1:], s[1:]
		default:
			if len(s) == 0 || s[0] != p[0] {
				return false
			}
			p, s = p[1:], s[1:]
		}
	}
	return len(s) == 0
}
