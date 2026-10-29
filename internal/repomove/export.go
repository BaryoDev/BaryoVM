// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package repomove

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ExportReport says what an export wrote. Counts are of what the host returned; nothing is
// dropped, and a call that fails (a rate limit included) fails the export instead of leaving a
// shorter file behind.
type ExportReport struct {
	Dir            string `json:"dir"`
	Issues         int    `json:"issues"`
	PullRequests   int    `json:"pullRequests"`
	IssueComments  int    `json:"issueComments"`
	ReviewComments int    `json:"reviewComments"`
}

// Export is the exported data, kept as the host sent it.
type Export struct {
	Issues       []map[string]any
	PullRequests []map[string]any
	Report       ExportReport
}

// fetchAll runs one paged gh call and flattens the pages. --slurp wraps each page in an outer array;
// a bare array (an older gh) is accepted too.
func fetchAll(ex Exec, path string) ([]map[string]any, error) {
	argv := []string{"gh", "api", "--paginate", "--slurp", path}
	out, exit, err := ex(argv)
	if err != nil || exit != 0 {
		if strings.Contains(strings.ToLower(out), "rate limit") {
			return nil, fmt.Errorf("export stopped: GitHub's API rate limit was hit, wait for it to reset and run the move again (nothing was written)")
		}
		return nil, fmt.Errorf("export stopped: %w", callError(argv, out, exit, err))
	}
	var raw []json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &raw); err != nil {
		return nil, fmt.Errorf("export stopped: gh api did not return JSON for %s", path)
	}
	var items []map[string]any
	for _, r := range raw {
		var page []map[string]any
		if json.Unmarshal(r, &page) == nil {
			items = append(items, page...)
			continue
		}
		var one map[string]any
		if err := json.Unmarshal(r, &one); err != nil {
			return nil, fmt.Errorf("export stopped: unexpected JSON from gh api")
		}
		items = append(items, one)
	}
	return items, nil
}

func number(m map[string]any) int {
	f, _ := m["number"].(float64)
	return int(f)
}

// commentOwner reads the issue or pull request number out of a comment's issue_url or
// pull_request_url (".../issues/12", ".../pulls/12").
func commentOwner(c map[string]any) int {
	for _, k := range []string{"issue_url", "pull_request_url"} {
		if u, _ := c[k].(string); u != "" {
			var n int
			if _, err := fmt.Sscanf(u[strings.LastIndex(u, "/")+1:], "%d", &n); err == nil {
				return n
			}
		}
	}
	return 0
}

// ExportGitHub reads every issue and pull request of owner/name, with their comments. GitHub's
// issues endpoint lists pull requests too; those are taken from the pulls endpoint instead, so a
// pull request appears once, with its review comments.
func ExportGitHub(ex Exec, r Repo) (*Export, error) {
	base := "repos/" + r.Path
	issuesAll, err := fetchAll(ex, base+"/issues?state=all&per_page=100")
	if err != nil {
		return nil, err
	}
	pulls, err := fetchAll(ex, base+"/pulls?state=all&per_page=100")
	if err != nil {
		return nil, err
	}
	issueComments, err := fetchAll(ex, base+"/issues/comments?per_page=100")
	if err != nil {
		return nil, err
	}
	reviewComments, err := fetchAll(ex, base+"/pulls/comments?per_page=100")
	if err != nil {
		return nil, err
	}
	byNum := func(list []map[string]any) map[int][]map[string]any {
		m := map[int][]map[string]any{}
		for _, c := range list {
			n := commentOwner(c)
			m[n] = append(m[n], c)
		}
		return m
	}
	ic, rc := byNum(issueComments), byNum(reviewComments)
	e := &Export{}
	for _, it := range issuesAll {
		if _, isPR := it["pull_request"]; isPR {
			continue
		}
		it["comments_data"] = orEmpty(ic[number(it)])
		e.Issues = append(e.Issues, it)
	}
	for _, p := range pulls {
		p["comments_data"] = orEmpty(ic[number(p)])
		p["review_comments_data"] = orEmpty(rc[number(p)])
		e.PullRequests = append(e.PullRequests, p)
	}
	sort.Slice(e.Issues, func(i, j int) bool { return number(e.Issues[i]) < number(e.Issues[j]) })
	sort.Slice(e.PullRequests, func(i, j int) bool { return number(e.PullRequests[i]) < number(e.PullRequests[j]) })
	e.Report = ExportReport{Issues: len(e.Issues), PullRequests: len(e.PullRequests),
		IssueComments: len(issueComments), ReviewComments: len(reviewComments)}
	return e, nil
}

func orEmpty(l []map[string]any) []map[string]any {
	if l == nil {
		return []map[string]any{}
	}
	return l
}

// Write puts issues.json and pulls.json in dir, both 0600 in a 0700 directory: a private
// repository's discussions are private. Both are written only after every fetch succeeded.
func (e *Export) Write(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	files := []struct {
		name string
		v    any
	}{{"issues.json", e.Issues}, {"pulls.json", e.PullRequests}}
	// Both files go to temp names first and are renamed into place afterwards, so a write cut off
	// halfway never leaves a short file under the real name. A rename replaces a symlink at the
	// target instead of following it, so the file a link points at is not overwritten.
	var temps, finals []string
	cleanup := func() {
		for _, t := range temps {
			_ = os.Remove(t)
		}
	}
	for _, f := range files {
		b, err := json.MarshalIndent(f.v, "", "  ")
		if err != nil {
			cleanup()
			return err
		}
		tmp := filepath.Join(dir, "."+f.name+".tmp")
		_ = os.Remove(tmp) // a stale temp name, or a link planted there, is replaced
		h, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			cleanup()
			return err
		}
		temps = append(temps, tmp)
		if _, err := h.Write(b); err != nil {
			_ = h.Close()
			cleanup()
			return err
		}
		if err := h.Close(); err != nil {
			cleanup()
			return err
		}
		finals = append(finals, filepath.Join(dir, f.name))
	}
	for i, t := range temps {
		if err := os.Rename(t, finals[i]); err != nil {
			cleanup()
			return err
		}
	}
	e.Report.Dir = dir
	return nil
}

// ExportPresent reports whether a finished export already sits in dir: both files are regular
// files holding valid JSON. A file cut off halfway does not parse and does not count.
func ExportPresent(dir string) bool {
	for _, n := range []string{"issues.json", "pulls.json"} {
		p := filepath.Join(dir, n)
		st, err := os.Lstat(p)
		if err != nil || !st.Mode().IsRegular() {
			return false
		}
		b, err := os.ReadFile(p)
		if err != nil || !json.Valid(b) {
			return false
		}
	}
	return true
}
