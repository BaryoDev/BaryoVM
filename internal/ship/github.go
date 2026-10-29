// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package ship

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// WorkflowRun is the part of a GitHub Actions run ship reads.
type WorkflowRun struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Path       string `json:"path,omitempty"`
	URL        string `json:"url"`
	Status     string `json:"status"`     // queued | in_progress | completed
	Conclusion string `json:"conclusion"` // success | failure | cancelled | skipped ...
	HeadSHA    string `json:"headSha,omitempty"`
}

// Step is one step of a job. A job with no steps (skipped whole, for one) is one Step with an empty
// Name, so the job's own conclusion can still be matched.
type Step struct {
	Job        string `json:"job"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
}

// GitHub is the seam to the GitHub API: reads, plus creating the release.
type GitHub interface {
	// FindRun returns the push run the tag started, or nil when none exists yet.
	FindRun(ctx context.Context, repo, tag, sha, workflow string) (*WorkflowRun, error)
	GetRun(ctx context.Context, repo string, id int64) (*WorkflowRun, error)
	Steps(ctx context.Context, repo string, id int64) ([]Step, error)
	// ReleaseURL returns the existing release for the tag.
	ReleaseURL(ctx context.Context, repo, tag string) (string, bool, error)
	CreateRelease(ctx context.Context, repo, tag, title, notes string) (string, error)
}

// APIGitHub is the REST implementation. The token is used in one header and never in an error.
type APIGitHub struct {
	Base  string // default https://api.github.com
	Token string
	H     Doer
}

func (g APIGitHub) base() string {
	if g.Base == "" {
		return "https://api.github.com"
	}
	return strings.TrimRight(g.Base, "/")
}

// do sends one request. A 404 is an answer only for a call that says so (allow404, the GETs that ask
// whether something exists); anywhere else it is an error, so a POST that finds nothing cannot read
// as success with an empty body.
func (g APIGitHub) do(ctx context.Context, method, path string, body any, out any, allow404 bool) (int, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.base()+path, rd)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+g.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := g.H.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if resp.StatusCode == http.StatusNotFound && allow404 {
		return resp.StatusCode, nil
	}
	if resp.StatusCode/100 != 2 {
		return resp.StatusCode, fmt.Errorf("%s %s: HTTP %d", method, path, resp.StatusCode)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			return resp.StatusCode, fmt.Errorf("%s %s: answer is not JSON: %w", method, path, err)
		}
	}
	return resp.StatusCode, nil
}

type apiRun struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Path       string `json:"path"`
	HTMLURL    string `json:"html_url"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	HeadSHA    string `json:"head_sha"`
	HeadBranch string `json:"head_branch"`
	Event      string `json:"event"`
}

func (a apiRun) run() *WorkflowRun {
	return &WorkflowRun{ID: a.ID, Name: a.Name, Path: a.Path, URL: a.HTMLURL, Status: a.Status, Conclusion: a.Conclusion, HeadSHA: a.HeadSHA}
}

func (g APIGitHub) FindRun(ctx context.Context, repo, tag, sha, workflow string) (*WorkflowRun, error) {
	var out struct {
		Runs []apiRun `json:"workflow_runs"`
	}
	if _, err := g.do(ctx, http.MethodGet, "/repos/"+repo+"/actions/runs?event=push&per_page=50&head_sha="+url.QueryEscape(sha), nil, &out, false); err != nil {
		return nil, err
	}
	for _, r := range out.Runs {
		// A tag push reports the tag name as the head branch. The commit is compared here as well as
		// in the query, so a run of another commit that reused the tag name is never watched.
		if r.HeadBranch != tag || r.Event != "push" || r.HeadSHA != sha {
			continue
		}
		if workflow != "" && !strings.Contains(r.Name, workflow) && !strings.Contains(r.Path, workflow) {
			continue
		}
		return r.run(), nil
	}
	return nil, nil
}

func (g APIGitHub) GetRun(ctx context.Context, repo string, id int64) (*WorkflowRun, error) {
	var r apiRun
	if _, err := g.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/actions/runs/%d", repo, id), nil, &r, false); err != nil {
		return nil, err
	}
	return r.run(), nil
}

func (g APIGitHub) Steps(ctx context.Context, repo string, id int64) ([]Step, error) {
	var steps []Step
	// A matrix can run past 100 jobs: read every page, up to a cap that no real run reaches.
	for page := 1; page <= 30; page++ {
		var out struct {
			Jobs []struct {
				Name       string `json:"name"`
				Status     string `json:"status"`
				Conclusion string `json:"conclusion"`
				Steps      []struct {
					Name       string `json:"name"`
					Status     string `json:"status"`
					Conclusion string `json:"conclusion"`
				} `json:"steps"`
			} `json:"jobs"`
		}
		if _, err := g.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/actions/runs/%d/jobs?per_page=100&page=%d", repo, id, page), nil, &out, false); err != nil {
			return nil, err
		}
		for _, j := range out.Jobs {
			if len(j.Steps) == 0 {
				steps = append(steps, Step{Job: j.Name, Status: j.Status, Conclusion: j.Conclusion})
			}
			for _, s := range j.Steps {
				steps = append(steps, Step{Job: j.Name, Name: s.Name, Status: s.Status, Conclusion: s.Conclusion})
			}
		}
		if len(out.Jobs) < 100 {
			break
		}
	}
	return steps, nil
}

func (g APIGitHub) ReleaseURL(ctx context.Context, repo, tag string) (string, bool, error) {
	var r struct {
		HTMLURL string `json:"html_url"`
	}
	code, err := g.do(ctx, http.MethodGet, "/repos/"+repo+"/releases/tags/"+url.PathEscape(tag), nil, &r, true)
	if err != nil || code == http.StatusNotFound {
		return "", false, err
	}
	return r.HTMLURL, true, nil
}

func (g APIGitHub) CreateRelease(ctx context.Context, repo, tag, title, notes string) (string, error) {
	var r struct {
		HTMLURL string `json:"html_url"`
	}
	if _, err := g.do(ctx, http.MethodPost, "/repos/"+repo+"/releases", map[string]any{"tag_name": tag, "name": title, "body": notes}, &r, false); err != nil {
		return "", err
	}
	if r.HTMLURL == "" {
		return "", fmt.Errorf("POST /repos/%s/releases answered without a release URL: check %s/releases", repo, repo)
	}
	return r.HTMLURL, nil
}
