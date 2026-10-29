// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package repoinit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// GitHub is every call this package makes to GitHub. It is the seam: tests hand in a fake, and
// the real one is API. Every real call has its own timeout.
type GitHub interface {
	DefaultBranch(ctx context.Context, repo Repo) (string, error)
	HasProtection(ctx context.Context, repo Repo, branch string) (bool, error)
	ProtectBranch(ctx context.Context, repo Repo, branch string, p Protection) error
	Dispatch(ctx context.Context, repo Repo, workflow, ref string, inputs map[string]string) error
	Runs(ctx context.Context, repo Repo, workflow, ref string) ([]WorkflowRun, error)
	Jobs(ctx context.Context, repo Repo, runID int64) ([]Job, error)
}

// Repo is owner/name.
type Repo struct {
	Owner string `json:"owner"`
	Name  string `json:"name"`
}

func (r Repo) String() string { return r.Owner + "/" + r.Name }

var (
	ownerRe = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
	repoRe  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
)

// ParseRepo reads owner/name and refuses anything that could change a URL path.
func ParseRepo(s string) (Repo, error) {
	o, n, ok := strings.Cut(s, "/")
	if !ok || !ownerRe.MatchString(o) || !repoRe.MatchString(n) || n == "." || n == ".." || strings.Contains(n, "/") {
		return Repo{}, fmt.Errorf("repository %q: write it as owner/name", s)
	}
	return Repo{Owner: o, Name: n}, nil
}

// Protection is the branch protection init applies.
type Protection struct {
	Checks    []string `json:"checks"`
	Strict    bool     `json:"strict"`
	CodeOwner bool     `json:"codeOwnerReviews"`
	Approvals int      `json:"approvals"`
}

// WorkflowRun is one workflow run.
type WorkflowRun struct {
	Event      string
	Branch     string
	CreatedAt  time.Time
	ID         int64
	Status     string
	Conclusion string
	URL        string
}

// Job is one job of a run with its steps.
type Job struct {
	Name       string
	Conclusion string
	Steps      []Step
}

// Step is one step of a job.
type Step struct {
	Name       string
	Status     string
	Conclusion string
}

// CallTimeout bounds every real GitHub call.
const CallTimeout = 30 * time.Second

// API is the real GitHub, over the REST API.
type API struct {
	Token   string
	Base    string // default https://api.github.com
	Client  *http.Client
	Timeout time.Duration // per call, default CallTimeout
}

func (a *API) do(ctx context.Context, method, p string, body any, out any) error {
	timeout := a.Timeout
	if timeout <= 0 {
		timeout = CallTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	base := a.Base
	if base == "" {
		base = "https://api.github.com"
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(base, "/")+p, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "baryovm")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c := a.Client
	if c == nil {
		c = http.DefaultClient
	}
	resp, err := c.Do(req)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("github %s %s timed out after %s", method, p, timeout)
		}
		// The error text of net/http carries the URL, never the header, so no token can leak here.
		return fmt.Errorf("github %s %s: %w", method, p, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 300 {
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &e)
		msg := e.Message
		if msg == "" {
			msg = strings.TrimSpace(string(raw))
			if len(msg) > 200 {
				msg = msg[:200]
			}
		}
		return &StatusError{Status: resp.StatusCode, Method: method, Path: p, Message: msg}
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// StatusError is a non-2xx answer from GitHub.
type StatusError struct {
	Status  int
	Method  string
	Path    string
	Message string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("github %s %s: %d %s", e.Method, e.Path, e.Status, e.Message)
}

func repoPath(r Repo) string {
	return "/repos/" + url.PathEscape(r.Owner) + "/" + url.PathEscape(r.Name)
}

// DefaultBranch implements GitHub.
func (a *API) DefaultBranch(ctx context.Context, repo Repo) (string, error) {
	var out struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := a.do(ctx, http.MethodGet, repoPath(repo), nil, &out); err != nil {
		return "", err
	}
	if out.DefaultBranch == "" {
		return "", fmt.Errorf("%s has no default branch yet: push a first commit", repo)
	}
	return out.DefaultBranch, nil
}

// HasProtection implements GitHub. A 404 whose message says the branch is not protected means
// there is none. Any other 404 (no repository, no access) is an error, not "unprotected".
func (a *API) HasProtection(ctx context.Context, repo Repo, branch string) (bool, error) {
	err := a.do(ctx, http.MethodGet, repoPath(repo)+"/branches/"+url.PathEscape(branch)+"/protection", nil, nil)
	var se *StatusError
	if errors.As(err, &se) && se.Status == http.StatusNotFound && strings.Contains(strings.ToLower(se.Message), "not protected") {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// ProtectBranch implements GitHub, as a PUT of the whole protection.
func (a *API) ProtectBranch(ctx context.Context, repo Repo, branch string, p Protection) error {
	checks := p.Checks
	if checks == nil {
		checks = []string{}
	}
	body := map[string]any{
		"required_status_checks": map[string]any{"strict": p.Strict, "contexts": checks},
		"enforce_admins":         false,
		"required_pull_request_reviews": map[string]any{
			"require_code_owner_reviews":      p.CodeOwner,
			"required_approving_review_count": p.Approvals,
		},
		"restrictions": nil,
	}
	return a.do(ctx, http.MethodPut, repoPath(repo)+"/branches/"+url.PathEscape(branch)+"/protection", body, nil)
}

// Dispatch implements GitHub.
func (a *API) Dispatch(ctx context.Context, repo Repo, workflow, ref string, inputs map[string]string) error {
	body := map[string]any{"ref": ref}
	if len(inputs) > 0 {
		body["inputs"] = inputs
	}
	return a.do(ctx, http.MethodPost, repoPath(repo)+"/actions/workflows/"+url.PathEscape(workflow)+"/dispatches", body, nil)
}

// Runs implements GitHub: the newest workflow_dispatch runs of the workflow on ref.
func (a *API) Runs(ctx context.Context, repo Repo, workflow, ref string) ([]WorkflowRun, error) {
	q := url.Values{"event": {"workflow_dispatch"}, "branch": {ref}, "per_page": {"20"}}
	var out struct {
		Runs []struct {
			ID         int64     `json:"id"`
			Event      string    `json:"event"`
			HeadBranch string    `json:"head_branch"`
			CreatedAt  time.Time `json:"created_at"`
			Status     string    `json:"status"`
			Conclusion string    `json:"conclusion"`
			HTMLURL    string    `json:"html_url"`
		} `json:"workflow_runs"`
	}
	if err := a.do(ctx, http.MethodGet, repoPath(repo)+"/actions/workflows/"+url.PathEscape(workflow)+"/runs?"+q.Encode(), nil, &out); err != nil {
		return nil, err
	}
	runs := make([]WorkflowRun, 0, len(out.Runs))
	for _, r := range out.Runs {
		runs = append(runs, WorkflowRun{ID: r.ID, Event: r.Event, Branch: r.HeadBranch, CreatedAt: r.CreatedAt, Status: r.Status, Conclusion: r.Conclusion, URL: r.HTMLURL})
	}
	return runs, nil
}

// Jobs implements GitHub.
func (a *API) Jobs(ctx context.Context, repo Repo, runID int64) ([]Job, error) {
	var out struct {
		Jobs []struct {
			Name       string `json:"name"`
			Conclusion string `json:"conclusion"`
			Steps      []struct {
				Name       string `json:"name"`
				Status     string `json:"status"`
				Conclusion string `json:"conclusion"`
			} `json:"steps"`
		} `json:"jobs"`
	}
	if err := a.do(ctx, http.MethodGet, fmt.Sprintf("%s/actions/runs/%d/jobs?per_page=100", repoPath(repo), runID), nil, &out); err != nil {
		return nil, err
	}
	jobs := make([]Job, 0, len(out.Jobs))
	for _, j := range out.Jobs {
		job := Job{Name: j.Name, Conclusion: j.Conclusion}
		for _, s := range j.Steps {
			job.Steps = append(job.Steps, Step{Name: s.Name, Status: s.Status, Conclusion: s.Conclusion})
		}
		jobs = append(jobs, job)
	}
	return jobs, nil
}
