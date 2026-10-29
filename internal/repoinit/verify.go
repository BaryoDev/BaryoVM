// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package repoinit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Verdicts of a verify.
const (
	Took         = "took"         // every OIDC step succeeded
	DidNotTake   = "did-not-take" // an OIDC step failed
	Inconclusive = "inconclusive" // the run ended without answering: see Reason
)

// VerifyOptions is one verify run.
type VerifyOptions struct {
	Repo     Repo
	Workflow string
	Steps    []string          // the OIDC step names that must all succeed
	Ref      string            // empty means the default branch
	Inputs   map[string]string // sent with the dispatch, the dry run switch
	Timeout  time.Duration     // how long to wait for the run to finish
	Poll     time.Duration
	Values   TrustedPublishing // repeated in the report so a failure shows what to check
}

// VerifyDeps are the seams of verify.
type VerifyDeps struct {
	GH    GitHub
	Sleep func(ctx context.Context, d time.Duration) error
	Now   func() time.Time // default time.Now
}

// dispatchSkew is how far a run's created_at may precede our clock and still be ours.
const dispatchSkew = 10 * time.Second

// StepResult is what one OIDC step did.
type StepResult struct {
	Name       string `json:"name"`
	Conclusion string `json:"conclusion"` // success, failure, skipped, or not-found
}

// VerifyReport is the Data of repo verify.
type VerifyReport struct {
	Repo     string            `json:"repo"`
	Workflow string            `json:"workflow"`
	Ref      string            `json:"ref"`
	Verdict  string            `json:"verdict"`
	Reason   string            `json:"reason,omitempty"`
	RunID    int64             `json:"runId,omitempty"`
	RunURL   string            `json:"runUrl,omitempty"`
	Run      string            `json:"runConclusion,omitempty"`
	Steps    []StepResult      `json:"steps"`
	Values   TrustedPublishing `json:"trustedPublishing"`
}

// RealSleep waits d or until ctx ends.
func RealSleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Verify starts the workflow's dry run and reads whether the OIDC exchange step succeeded. The
// run it reads is the one whose id was not there before the dispatch, so an earlier run is never
// mistaken for this one. It waits Timeout / Poll polls and no wall clock.
func Verify(ctx context.Context, o VerifyOptions, d VerifyDeps) (*VerifyReport, error) {
	if o.Poll <= 0 {
		o.Poll = 10 * time.Second
	}
	if o.Timeout <= 0 {
		o.Timeout = 15 * time.Minute
	}
	rep := &VerifyReport{Repo: o.Repo.String(), Workflow: o.Workflow, Steps: []StepResult{}, Values: o.Values}
	ref := o.Ref
	if ref == "" {
		b, err := d.GH.DefaultBranch(ctx, o.Repo)
		if err != nil {
			return rep, fmt.Errorf("find the default branch: %w", err)
		}
		ref = b
	}
	rep.Ref = ref
	before, err := d.GH.Runs(ctx, o.Repo, o.Workflow, ref)
	if err != nil {
		return rep, fmt.Errorf("list runs of %s: %w", o.Workflow, err)
	}
	known := map[int64]bool{}
	for _, r := range before {
		known[r.ID] = true
	}
	now := d.Now
	if now == nil {
		now = time.Now
	}
	dispatchedAt := now()
	if err := d.GH.Dispatch(ctx, o.Repo, o.Workflow, ref, o.Inputs); err != nil {
		var se *StatusError
		if errors.As(err, &se) && (se.Status == 404 || se.Status == 422) {
			return rep, fmt.Errorf("start %s on %s: %w: is the workflow committed and pushed to %s, and does it have a workflow_dispatch trigger? Push it before creating the registry policy", o.Workflow, ref, err, ref)
		}
		return rep, fmt.Errorf("start %s on %s: %w", o.Workflow, ref, err)
	}
	polls := int(o.Timeout / o.Poll)
	if polls < 1 {
		polls = 1
	}
	var run *WorkflowRun
	for i := 0; i <= polls; i++ {
		if i > 0 {
			if err := d.Sleep(ctx, o.Poll); err != nil {
				return rep, fmt.Errorf("stopped waiting for the run: %w", err)
			}
		}
		runs, err := d.GH.Runs(ctx, o.Repo, o.Workflow, ref)
		if err != nil {
			return rep, fmt.Errorf("list runs of %s: %w", o.Workflow, err)
		}
		run = nil
		matches := 0
		for j := range runs {
			r := runs[j]
			if known[r.ID] || r.Event != "workflow_dispatch" || r.Branch != ref || r.CreatedAt.Before(dispatchedAt.Add(-dispatchSkew)) {
				continue
			}
			matches++
			run = &runs[j]
		}
		if matches > 1 {
			rep.Verdict = Inconclusive
			rep.Reason = "more than one new dispatched run of " + o.Workflow + " on " + ref + " appeared (someone else started one?): not guessing which is ours, run verify again when it is quiet"
			return rep, nil
		}
		if run != nil {
			rep.RunID, rep.RunURL = run.ID, run.URL
			if run.Status == "completed" {
				break
			}
		}
	}
	if run == nil {
		rep.Verdict, rep.Reason = Inconclusive, "the dispatch was accepted but no new run appeared in time"
		return rep, nil
	}
	if run.Status != "completed" {
		rep.Verdict, rep.Reason = Inconclusive, "the run had not finished after "+o.Timeout.String()+": check "+run.URL
		return rep, nil
	}
	rep.Run = run.Conclusion
	jobs, err := d.GH.Jobs(ctx, o.Repo, run.ID)
	if err != nil {
		return rep, fmt.Errorf("read the jobs of run %d: %w", run.ID, err)
	}
	if len(o.Steps) == 0 {
		rep.Verdict = Inconclusive
		rep.Reason = "no OIDC step was named, so there is nothing to read a verdict from"
		return rep, nil
	}
	all, anyFail := true, false
	for _, want := range o.Steps {
		res := StepResult{Name: want, Conclusion: "not-found"}
		for _, j := range jobs {
			for _, s := range j.Steps {
				if s.Name == want {
					res.Conclusion = s.Conclusion
				}
			}
		}
		rep.Steps = append(rep.Steps, res)
		if res.Conclusion != "success" {
			all = false
		}
		if res.Conclusion == "failure" {
			anyFail = true
		}
	}
	switch {
	case all:
		rep.Verdict = Took
	case anyFail:
		rep.Verdict = DidNotTake
		rep.Reason = "the OIDC exchange failed: check the policy on " + o.Values.Registry + " against owner, repository, workflow file and environment"
	default:
		rep.Verdict = Inconclusive
		var miss []string
		for _, s := range rep.Steps {
			if s.Conclusion != "success" {
				miss = append(miss, s.Name+" ("+s.Conclusion+")")
			}
		}
		rep.Reason = "the run ended (" + run.Conclusion + ") without a verdict from: " + strings.Join(miss, ", ") + ". A step earlier in the workflow probably failed: see " + run.URL
	}
	return rep, nil
}
