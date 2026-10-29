// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package guard

import (
	"fmt"
	"strings"
)

// ghChecked are the gh subcommands whose text goes public: a pull request, a release and an issue.
var ghChecked = map[string]bool{"pr create": true, "release create": true, "issue create": true}

// ghTextLong and ghTextShort are the flags that carry text or name a file with text. Short flags
// differ by command only in meaning (-n is notes on release create), so one table serves all three.
var (
	ghTextLong = map[string]string{
		"--body": "text", "--title": "text", "--notes": "text", "--notes-file": "file", "--body-file": "file",
	}
	ghTextShort = map[byte]string{'b': "text", 't': "text", 'n': "text", 'F': "file"}
)

// CheckGh scans the text a gh create command would publish. Every other gh command is allowed
// through. A body read from stdin (-F -) is refused because it cannot be read here and still reach
// gh; write it to a file instead.
func CheckGh(r Runner, args []string) Verdict {
	var words []string
	for _, a := range args {
		if len(words) < 2 && !strings.HasPrefix(a, "-") {
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
			kind, ok := ghTextShort[a[1]]
			if !ok {
				continue
			}
			val := a[2:]
			if val == "" {
				if i+1 >= len(args) {
					continue
				}
				i++
				val = args[i]
			}
			if v := check(a[:2], kind, val); v != nil {
				return *v
			}
		}
	}
	if len(fs) > 0 {
		return keyRefusal(action, "the text to publish", fs)
	}
	return allow(action)
}
