// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package ship

import (
	"regexp"
	"strings"
)

// changelogSection returns the body of the changelog section for exactly version, and whether
// there is a heading for it. A heading is a markdown heading whose first token is the version,
// optionally in brackets and with a leading v: "## [1.2.4] - 2026-08-19" and "### v1.2.4" match
// 1.2.4, "## 1.2.4-beta" and "## 1.2.40" do not. The body runs to the next heading of the same or
// a higher level.
func changelogSection(text, version string) (body string, found bool) {
	re := regexp.MustCompile(`^(#{1,6})[ \t]*\[?v?` + regexp.QuoteMeta(version) + `\]?(?:[ \t:(,]|$)`)
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	start, level := -1, 0
	for i, l := range lines {
		if m := re.FindStringSubmatch(l); m != nil {
			start, level = i, len(m[1])
			break
		}
	}
	if start < 0 {
		return "", false
	}
	var b []string
	for _, l := range lines[start+1:] {
		if h := headingLevel(l); h > 0 && h <= level {
			break
		}
		b = append(b, l)
	}
	return strings.TrimSpace(strings.Join(b, "\n")), true
}

func headingLevel(l string) int {
	n := 0
	for n < len(l) && l[n] == '#' {
		n++
	}
	if n == 0 || n > 6 || n >= len(l) || (l[n] != ' ' && l[n] != '\t') {
		return 0
	}
	return n
}
