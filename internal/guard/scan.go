// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package guard checks what is about to leave a machine: it finds keys in text and diffs, decides
// whether a remote host is one a repository may reach, and plans the checks for `git` and `gh`
// commands. The functions here are pure or talk to a Runner, so tests never run git or gh.
// Findings carry a file, a line and a rule name, never the matched value.
package guard

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// AllowMarker on a line exempts it from the scan, for test fixtures and documented examples.
const AllowMarker = "allow-secret"

// Finding is one suspected key. It never holds the key itself, so printing or logging a Finding
// cannot leak what it found.
type Finding struct {
	File string `json:"file"`
	Line int    `json:"line"`
	Rule string `json:"rule"`
}

func (f Finding) String() string {
	return fmt.Sprintf("%s:%d (%s)", f.File, f.Line, f.Rule)
}

type rule struct {
	name string
	re   *regexp.Regexp
}

// rules are the key shapes checked everywhere: the same list the Claude Code hook uses, plus AWS
// session keys (ASIA) and Stripe restricted keys (rk_live).
var rules = []rule{
	{"google-api-key", regexp.MustCompile(`AIza[0-9A-Za-z_-]{35}`)},
	{"github-token", regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{36,}`)},
	{"github-pat", regexp.MustCompile(`github_pat_[A-Za-z0-9_]{22,}`)},
	{"aws-access-key", regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`)},
	{"slack-token", regexp.MustCompile(`xox[baprs]-[A-Za-z0-9-]{10,}`)},
	{"stripe-live-key", regexp.MustCompile(`[sr]k_live_[0-9A-Za-z]{20,}`)},
	{"npm-token", regexp.MustCompile(`npm_[A-Za-z0-9]{36}`)},
	{"private-key", regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)},
}

// ScanLine returns the rules a single line breaks, in rule order. A line carrying the allow marker
// is skipped, and so is a match containing EXAMPLE, which is how vendors write their documentation
// keys (AWS's AKIA...EXAMPLE).
func ScanLine(line string) []string {
	if strings.Contains(line, AllowMarker) {
		return nil
	}
	var hit []string
	for _, r := range rules {
		for _, m := range r.re.FindAllString(line, -1) {
			if strings.Contains(strings.ToUpper(m), "EXAMPLE") {
				continue
			}
			hit = append(hit, r.name)
			break
		}
	}
	return hit
}

// ScanText scans text line by line. name is what the finding reports as the file.
func ScanText(name, text string) []Finding {
	var out []Finding
	for i, line := range strings.Split(text, "\n") {
		for _, r := range ScanLine(line) {
			out = append(out, Finding{File: name, Line: i + 1, Rule: r})
		}
	}
	return out
}

// ScanDiff scans only the added lines of a unified diff (git diff or git log -p), reporting the
// file and the line number in the new version. Context and removed lines are ignored: a key being
// deleted is not a leak.
//
// A diff has a header per file and then hunks. Inside a hunk a content line can start with +++
// (an added line that begins with "++"), so +++ is read as a file name only in the header, before
// the first @@ of that file.
func ScanDiff(diff string) []Finding {
	var (
		out    []Finding
		file   string
		inHunk bool
		newLn  int
	)
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			inHunk = false
			file = diffGitName(line)
		case !inHunk && strings.HasPrefix(line, "+++ "):
			if name := headerName(strings.TrimPrefix(line, "+++ ")); name != "" {
				file = name
			}
		case strings.HasPrefix(line, "@@"):
			inHunk = true
			newLn = hunkStart(line)
		case inHunk && strings.HasPrefix(line, "+"):
			for _, r := range ScanLine(line[1:]) {
				out = append(out, Finding{File: file, Line: newLn, Rule: r})
			}
			newLn++
		case inHunk && strings.HasPrefix(line, " "):
			newLn++
		}
	}
	return out
}

// diffGitName reads the b/ path off a "diff --git a/x b/x" line, the fallback until the +++ line
// names the file.
func diffGitName(line string) string {
	rest := strings.TrimPrefix(line, "diff --git ")
	if i := strings.LastIndex(rest, " b/"); i >= 0 {
		return rest[i+3:]
	}
	return rest
}

func headerName(s string) string {
	s = strings.Trim(strings.TrimSpace(s), `"`)
	if s == "/dev/null" {
		return ""
	}
	return strings.TrimPrefix(s, "b/")
}

// hunkStart returns the first new-file line of an "@@ -a,b +c,d @@" header, or 1 if unreadable.
func hunkStart(line string) int {
	_, after, ok := strings.Cut(line, "+")
	if !ok {
		return 1
	}
	end := strings.IndexAny(after, ", ")
	if end < 0 {
		return 1
	}
	n, err := strconv.Atoi(after[:end])
	if err != nil {
		return 1
	}
	return n
}

// Summarise lists up to max findings as "file:line (rule)", comma separated, with a count of the
// rest.
func Summarise(fs []Finding, max int) string {
	var parts []string
	for i, f := range fs {
		if i == max {
			parts = append(parts, fmt.Sprintf("and %d more", len(fs)-max))
			break
		}
		parts = append(parts, f.String())
	}
	return strings.Join(parts, ", ")
}
