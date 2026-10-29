// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package guard

import (
	"fmt"
	"strings"
)

// ghChecked are the gh subcommands whose text goes public: a pull request, a release and an issue.
var ghChecked = map[string]bool{
	"pr create": true, "pr edit": true, "pr comment": true, "pr review": true,
	"issue create": true, "issue edit": true, "issue comment": true,
	"release create": true, "release edit": true,
}

// ghTextLong and ghTextShort are the flags that carry text or name a file with text. Short flags
// differ by command only in meaning (-n is notes on release create), so one table serves all three.
var (
	ghTextLong = map[string]string{
		"--body": "text", "--title": "text", "--notes": "text", "--notes-file": "file", "--body-file": "file",
	}
	ghTextShort = map[byte]string{'b': "text", 't': "text", 'n': "text", 'F': "file"}
)

// ghShortFlags lists, per scanned subcommand, the short flags that take a value we do not scan
// (other) and the ones that take none (bool). Text flags (-b -t -n -F) come from ghTextShort. The
// letters are read from `gh <group> <verb> --help` (gh 2.102.0), because the same letter differs by
// command: -p is --project on pr create and --prerelease on release create. -R is on every one.
var ghShortFlags = map[string]struct{ other, bool string }{
	"pr create":      {"aBHlmprRT", "defw"},
	"pr edit":        {"BmR", ""},
	"pr comment":     {"R", "ew"},
	"pr review":      {"R", "acr"},
	"issue create":   {"almpRT", "ew"},
	"issue edit":     {"mR", ""},
	"issue comment":  {"R", "ew"},
	"release create": {"R", "dp"},
	"release edit":   {"R", ""},
}

type shortGroup struct {
	letter   byte   // the text flag reached, 0 if none
	kind     string // "text" or "file" for a text flag
	val      string // the value when it sits in the same word
	needNext bool   // the value is the next word
	unknown  byte   // a letter that is not known, 0 if none
}

// walkShort walks a group of short flags the way pflag does: letter by letter, and the first flag
// that takes a value takes the rest of the word, or the next word when nothing is left.
func walkShort(a string, sub string) shortGroup {
	flags := ghShortFlags[sub]
	bools, others := flags.bool, flags.other
	for j := 1; j < len(a); j++ {
		c := a[j]
		g := shortGroup{val: a[j+1:]}
		g.needNext = g.val == ""
		if kind, ok := ghTextShort[c]; ok {
			g.letter, g.kind = c, kind
			return g
		}
		if strings.IndexByte(others, c) >= 0 {
			return g
		}
		if strings.IndexByte(bools, c) < 0 {
			return shortGroup{unknown: c}
		}
	}
	return shortGroup{}
}

// CheckGh scans the text a gh create command would publish. Every other gh command is allowed
// through. A body read from stdin (-F -) is refused because it cannot be read here and still reach
// gh; write it to a file instead.
func CheckGh(r Runner, args []string) Verdict {
	var words []string
	// The group and verb are the first two words that are not flags. -R and --repo take a value
	// that is not a word, so it is skipped: `gh pr -R o/r create` is still `pr create`.
	for i := 0; i < len(args) && len(words) < 2; i++ {
		a := args[i]
		if a == "--repo" {
			i++
			continue
		}
		if len(a) >= 2 && a[0] == '-' && a[1] != '-' {
			// Before the verb is known only -R matters: it takes the next word as its value.
			if k := strings.IndexByte(a, 'R'); k >= 0 && k == len(a)-1 {
				i++
			}
			continue
		}
		if !strings.HasPrefix(a, "-") {
			words = append(words, a)
		}
	}
	if len(words) < 2 {
		return allow("gh")
	}
	action := "gh " + words[0] + " " + words[1]
	if !ghChecked[words[0]+" "+words[1]] {
		return allow(action)
	}

	var fs []Finding
	check := func(flag, kind, val string) *Verdict {
		if kind == "text" {
			fs = append(fs, ScanText(flag, val)...)
			return nil
		}
		if val == "-" {
			v := refuse(action, fmt.Sprintf("%s refused: %s - reads stdin, which cannot be checked. Write the text to a file and pass that file", action, flag), nil)
			return &v
		}
		b, err := r.ReadFile(val)
		if err != nil {
			v := refuse(action, fmt.Sprintf("%s refused: cannot read %s (%v). Check the path", action, val, err), nil)
			return &v
		}
		fs = append(fs, ScanText(val, string(b))...)
		return nil
	}

	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			i = len(args)
		case strings.HasPrefix(a, "--"):
			name, val, hasVal := strings.Cut(a, "=")
			kind, ok := ghTextLong[name]
			if !ok {
				continue
			}
			if !hasVal {
				if i+1 >= len(args) {
					continue
				}
				i++
				val = args[i]
			}
			if v := check(name, kind, val); v != nil {
				return *v
			}
		case len(a) >= 2 && a[0] == '-':
			c := walkShort(a, words[0]+" "+words[1])
			if c.unknown != 0 && len(a) == 2 {
				continue // a single flag we do not know: gh rejects it, nothing to scan
			}
			if c.unknown != 0 {
				return refuse(action, fmt.Sprintf("%s refused: cannot read the flag group %s (unknown flag -%c). Pass the flags separately, for example -b text -w", action, a, c.unknown), nil)
			}
			val := c.val
			if c.needNext {
				if i+1 >= len(args) {
					continue
				}
				i++
				val = args[i]
			}
			if c.kind == "" {
				continue
			}
			if v := check("-"+string(c.letter), c.kind, val); v != nil {
				return *v
			}
		}
	}
	if len(fs) > 0 {
		return keyRefusal(action, "the text to publish", fs)
	}
	return allow(action)
}
