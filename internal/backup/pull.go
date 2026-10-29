// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package backup

import (
	"errors"
	"regexp"
	"sort"
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/sshx"
)

var (
	okLineRe = regexp.MustCompile(`(?m)^backup ok: \S*?-(\d{8}-\d{6})(?:\.|\s|$)`)
	// A file name safe to join onto a local directory: no separators, no leading dot.
	pullNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
)

// SetTimestamp reads the timestamp of the set a backup run just made from the "backup ok" line the
// script prints. Every file of the set carries it: <base>-<ts>.<ext>.
func SetTimestamp(out string) (string, error) {
	m := okLineRe.FindStringSubmatch(out)
	if m == nil {
		return "", errors.New("the backup output names no backup file, so there is nothing to pull")
	}
	return m[1], nil
}

// ListDirCmd prints the names in the backup dir, one per line.
func (c Config) ListDirCmd() string {
	return c.bkVar() + "ls -1 \"$BK\""
}

// ReadSetFileCmd builds the command that prints one named file of the backup dir to stdout.
// name must come from SetFiles.
func (c Config) ReadSetFileCmd(name string) string {
	return c.bkVar() + "cat \"$BK\"/" + sshx.Quote(name)
}

// SetFiles picks the files of the set with timestamp ts out of a directory listing: names that end
// in -<ts> or contain -<ts>. . A .partial file is never part of a set, and a name that could
// escape a local directory is dropped.
func SetFiles(listing, ts string) []string {
	var out []string
	for _, line := range strings.Split(listing, "\n") {
		name := strings.TrimSpace(line)
		if name == "" || strings.HasSuffix(name, ".partial") || strings.Contains(name, "..") || !pullNameRe.MatchString(name) {
			continue
		}
		if strings.HasSuffix(name, "-"+ts) || strings.Contains(name, "-"+ts+".") {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}
