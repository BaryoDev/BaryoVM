// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package ship

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// Stages. "tag" is `stack publish`: gate, tag, push the tag, stop. "full" is `ship`.
const (
	StageTag  = "tag"
	StageFull = "full"
)

// Options is one invocation.
type Options struct {
	Name    string // the stack or project name, for the record
	Version string // empty: whatever versionFrom says
	DryRun  bool
	Stage   string // StageFull by default
}

// Step is one line of what a run did.
type StepResult struct {
	Name   string `json:"name"`
	Status string `json:"status"` // ok | failed | skipped
	Detail string `json:"detail,omitempty"`
}

// DiffSummary is the shipped source since the previous tag.
type DiffSummary struct {
	From      string   `json:"from,omitempty"` // previous tag, empty when there is none
	To        string   `json:"to"`
	ShortStat string   `json:"shortStat,omitempty"`
	Files     []string `json:"files,omitempty"`
	Commits   []string `json:"commits,omitempty"`
}

// Assertion is one thing ship checked about a published artifact, with what it saw. #29 renders
// these as they are.
type Assertion struct {
	Package  string `json:"package"`
	Assert   string `json:"assert"`
	OK       bool   `json:"ok"`
	Observed string `json:"observed,omitempty"`
}

// Published is a package seen resolving at the version.
type Published struct {
	Package string `json:"package"`
	Version string `json:"version"`
}

// Record is everything a run reports, and what `ship` stores for #29 `baryovm prove`: the checks,
// the shipped diff, the run and its publish step, and each published assertion with the value
// observed, so a proof is rendered from it with nothing re-derived.
type Record struct {
	Schema     int          `json:"schema"`
	Name       string       `json:"name"`
	Stage      string       `json:"stage"`
	DryRun     bool         `json:"dryRun"`
	Version    string       `json:"version,omitempty"`
	Tag        string       `json:"tag,omitempty"`
	Commit     string       `json:"commit,omitempty"`
	Repo       string       `json:"repo,omitempty"`
	Registry   string       `json:"registry"`
	Resumed    bool         `json:"resumed"`
	StartedAt  string       `json:"startedAt"`
	FinishedAt string       `json:"finishedAt,omitempty"`
	Checks     []Check      `json:"checks"`
	Plan       []string     `json:"plan,omitempty"`
	Diff       *DiffSummary `json:"diff,omitempty"`
	Steps      []StepResult `json:"steps"`
	Run        *WorkflowRun `json:"run,omitempty"`
	Publish    *StepResult  `json:"publishStep,omitempty"`
	Published  []Published  `json:"published,omitempty"`
	Assertions []Assertion  `json:"assertions,omitempty"`
	Release    string       `json:"release,omitempty"`
	OK         bool         `json:"ok"`
	Error      string       `json:"error,omitempty"`
}

// RefusedError is a gate refusal. Its text names every failed check's cause.
type RefusedError struct{ Checks []Check }

func (e *RefusedError) Error() string {
	var r []string
	for _, c := range e.Checks {
		r = append(r, c.Name+": "+c.Reason)
	}
	return "release refused: " + strings.Join(r, "; ")
}

// ErrDeclined means the person said no at the confirmation.
var ErrDeclined = errors.New("not confirmed: nothing was tagged")

// Run is the whole flow. The record is returned in every case, with Error set on a failure.
func Run(ctx context.Context, d Deps, s Spec, o Options) (*Record, error) {
	if d.Now == nil {
		d.Now = time.Now
	}
	d.Timings = d.Timings.withDefaults()
	if o.Stage == "" {
		o.Stage = StageFull
	}
	rec := &Record{Schema: 1, Name: o.Name, Stage: o.Stage, DryRun: o.DryRun, Registry: s.Registry,
		StartedAt: d.Now().UTC().Format(time.RFC3339), Checks: []Check{}, Steps: []StepResult{}}
	if err := s.Validate(); err != nil {
		return rec.fail(d, err)
	}
	if d.Registry == nil || d.Git == nil || d.ReadFile == nil {
		return rec.fail(d, errors.New("ship: missing dependencies"))
	}

	checks, st := preflight(ctx, d, s, o.Version)
	rec.Checks = checks
	rec.Version, rec.Tag, rec.Commit, rec.Repo = st.version, st.tag, st.head, st.repo
	rec.Resumed = st.haveLocal || st.haveRemote
	var bad []Check
	for _, c := range checks {
		if !c.OK {
			bad = append(bad, c)
		}
	}
	if len(bad) > 0 {
		return rec.fail(d, &RefusedError{Checks: bad})
	}

	rec.Diff = diffSummary(ctx, d, s, st)

	if o.DryRun {
		rec.Plan = plan(s, st, o)
		rec.OK = true
		rec.FinishedAt = d.Now().UTC().Format(time.RFC3339)
		return rec, nil
	}

	// The token is resolved before the irreversible step: a missing sign-in must stop a release
	// before the tag is pushed, not after.
	var gh GitHub
	if o.Stage == StageFull {
		if d.GitHub == nil {
			return rec.fail(d, errors.New("ship: no GitHub client"))
		}
		var err error
		if gh, err = d.GitHub(); err != nil {
			return rec.fail(d, err)
		}
	}

	if d.Confirm != nil && !rec.Resumed {
		yes, err := d.Confirm(summarise(rec))
		if err != nil {
			return rec.fail(d, err)
		}
		if !yes {
			return rec.fail(d, ErrDeclined)
		}
	}

	if err := rec.tagAndPush(ctx, d, s, st); err != nil {
		return rec.fail(d, err)
	}
	if o.Stage == StageTag {
		rec.OK = true
		rec.FinishedAt = d.Now().UTC().Format(time.RFC3339)
		return rec, nil
	}

	run, err := rec.watch(ctx, d, s, st, gh)
	if err != nil {
		return rec.fail(d, err)
	}
	if err := rec.publishStepRan(ctx, d, s, st, gh, run); err != nil {
		return rec.fail(d, err)
	}
	if err := rec.indexed(ctx, d, st); err != nil {
		return rec.fail(d, err)
	}
	if err := rec.artifact(ctx, d, s, st); err != nil {
		return rec.fail(d, err)
	}
	if err := rec.release(ctx, d, st, gh); err != nil {
		return rec.fail(d, err)
	}
	rec.OK = true
	rec.FinishedAt = d.Now().UTC().Format(time.RFC3339)
	return rec, nil
}

func (r *Record) fail(d Deps, err error) (*Record, error) {
	r.OK, r.Error = false, err.Error()
	r.FinishedAt = d.Now().UTC().Format(time.RFC3339)
	return r, err
}

func (r *Record) step(name, status, detail string) {
	r.Steps = append(r.Steps, StepResult{Name: name, Status: status, Detail: detail})
}

func plan(s Spec, st *gateState, o Options) []string {
	var p []string
	if !st.haveLocal && !st.haveRemote {
		p = append(p, "git tag "+st.tag)
	}
	if !st.haveRemote {
		p = append(p, "git push "+s.remote()+" refs/tags/"+st.tag)
	}
	if o.Stage == StageFull {
		p = append(p,
			"find the run the tag started in "+st.repo,
			"watch it to the end",
			"assert the step matching \""+s.publishStep()+"\" ran and was not skipped",
			"poll the registry until "+strings.Join(st.packages, ", ")+" resolve at "+st.version,
			"read each package's published metadata and assert it",
			"create the GitHub release for "+st.tag)
	}
	return p
}

func summarise(r *Record) string {
	var b strings.Builder
	fmt.Fprintf(&b, "release %s as %s at %s\n", r.Version, r.Tag, short(r.Commit))
	if r.Diff != nil {
		if r.Diff.From == "" {
			b.WriteString("no earlier tag, so there is no diff to show\n")
		} else {
			fmt.Fprintf(&b, "since %s: %s\n", r.Diff.From, strings.TrimSpace(r.Diff.ShortStat))
			for _, f := range r.Diff.Files {
				b.WriteString("  " + f + "\n")
			}
		}
	}
	return b.String()
}

// diffSummary is what is being shipped: files and commits since the previous tag. It is best
// effort, because a missing earlier tag is a first release, not a failure.
func diffSummary(ctx context.Context, d Deps, s Spec, st *gateState) *DiffSummary {
	sum := &DiffSummary{To: short(st.head)}
	rev := "HEAD"
	if st.haveLocal || st.haveRemote {
		rev = st.tag + "^"
	}
	out, err := d.git(ctx, "describe", "--tags", "--abbrev=0", rev)
	prev := strings.TrimSpace(out)
	if err != nil || prev == "" {
		return sum
	}
	sum.From = prev
	if o, err := d.git(ctx, "diff", "--shortstat", prev, "HEAD"); err == nil {
		sum.ShortStat = strings.TrimSpace(o)
	}
	if o, err := d.git(ctx, "diff", "--name-status", prev, "HEAD"); err == nil {
		sum.Files = capList(nonEmpty(o), 200)
	}
	if o, err := d.git(ctx, "log", "--oneline", prev+"..HEAD"); err == nil {
		sum.Commits = capList(nonEmpty(o), 200)
	}
	return sum
}

func capList(l []string, n int) []string {
	for i := range l {
		l[i] = strings.TrimSpace(l[i])
	}
	if len(l) > n {
		l = append(l[:n], fmt.Sprintf("... and %d more", len(l)-n))
	}
	return l
}

// tagAndPush is the irreversible step. A tag that already points at HEAD is not made again, and
// one already on the remote is not pushed again: that is the resume.
func (r *Record) tagAndPush(ctx context.Context, d Deps, s Spec, st *gateState) error {
	switch {
	case st.haveLocal || st.haveRemote:
		r.step("tag", "skipped", "tag "+st.tag+" already exists at HEAD")
	default:
		if _, err := d.git(ctx, "tag", "--", st.tag); err != nil {
			r.step("tag", "failed", err.Error())
			return err
		}
		r.step("tag", "ok", st.tag)
	}
	if st.haveRemote {
		r.step("push-tag", "skipped", "already on "+s.remote())
		return nil
	}
	if _, err := d.git(ctx, "push", s.remote(), "refs/tags/"+st.tag); err != nil {
		r.step("push-tag", "failed", err.Error())
		return fmt.Errorf("the tag %s exists locally but the push failed, run the same command again to continue: %w", st.tag, err)
	}
	r.step("push-tag", "ok", st.tag+" to "+s.remote())
	return nil
}

// poll runs fn until it reports done or total passes. An error from fn is kept and retried: a
// registry or API that hiccups mid-watch must not end a release that is already tagged.
// A cancelled context (Ctrl-C) ends it at once, and the message says a rerun resumes.
func (r *Record) poll(ctx context.Context, d Deps, total time.Duration, what string, fn func() (bool, error)) error {
	deadline := d.Now().Add(total)
	var last error
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("interrupted while waiting for %s: run the same command again to continue from here: %w", what, err)
		}
		done, err := fn()
		if done && err == nil {
			return nil
		}
		last = err
		if !d.Now().Before(deadline) {
			if last != nil {
				return fmt.Errorf("gave up waiting for %s after %s: %w", what, total, last)
			}
			return fmt.Errorf("gave up waiting for %s after %s", what, total)
		}
		if d.Sleep != nil {
			d.Sleep(d.Timings.Poll)
			continue
		}
		select {
		case <-ctx.Done():
		case <-time.After(d.Timings.Poll):
		}
	}
}

func (r *Record) watch(ctx context.Context, d Deps, s Spec, st *gateState, gh GitHub) (*WorkflowRun, error) {
	var run *WorkflowRun
	err := r.poll(ctx, d, d.Timings.RunWait, "the tag push to start a run", func() (bool, error) {
		c, cancel := d.call(ctx)
		defer cancel()
		got, err := gh.FindRun(c, st.repo, st.tag, st.head, s.Workflow)
		if got != nil {
			run = got
		}
		return got != nil, err
	})
	if err != nil {
		r.step("find-run", "failed", err.Error())
		return nil, fmt.Errorf("no run found for tag %s: does the workflow trigger on tags? %w", st.tag, err)
	}
	r.step("find-run", "ok", fmt.Sprintf("run %d %s", run.ID, run.URL))
	r.Run = run
	err = r.poll(ctx, d, d.Timings.Watch, fmt.Sprintf("run %d to finish", run.ID), func() (bool, error) {
		c, cancel := d.call(ctx)
		defer cancel()
		got, err := gh.GetRun(c, st.repo, run.ID)
		if err != nil {
			return false, err
		}
		run = got
		return got.Status == "completed", nil
	})
	r.Run = run
	if err != nil {
		r.step("watch", "failed", err.Error())
		return nil, err
	}
	if run.Conclusion != "success" {
		r.step("watch", "failed", "run concluded "+run.Conclusion)
		return nil, fmt.Errorf("run %d concluded %s: %s", run.ID, run.Conclusion, run.URL)
	}
	r.step("watch", "ok", "run concluded success")
	return run, nil
}

// publishStepRan is the check that is skipped most: a publish guarded by an if: reports a green
// run when the guard is false. The step must exist and have succeeded.
func (r *Record) publishStepRan(ctx context.Context, d Deps, s Spec, st *gateState, gh GitHub, run *WorkflowRun) error {
	c, cancel := d.call(ctx)
	steps, err := gh.Steps(c, st.repo, run.ID)
	cancel()
	if err != nil {
		r.step("publish-step", "failed", err.Error())
		return fmt.Errorf("cannot read the jobs of run %d: %w", run.ID, err)
	}
	var matched, bad []Step
	for _, sp := range steps {
		if !stepMatches(sp, s) {
			continue
		}
		matched = append(matched, sp)
		if sp.Conclusion != "success" {
			bad = append(bad, sp)
		}
	}
	label := func(sp Step) string {
		if sp.Name == "" {
			return "job " + sp.Job
		}
		return sp.Job + " / " + sp.Name
	}
	var msg string
	switch {
	case len(matched) == 0:
		msg = fmt.Sprintf("no step in run %d is named %q: set publish.publishStep to the publishing step's exact name", run.ID, s.publishStep())
	case len(bad) > 0:
		msg = fmt.Sprintf("the run is green but the publish step %q concluded %q: it did not run, so nothing was published (check its if: condition)", label(bad[0]), bad[0].Conclusion)
		res := StepResult{Name: label(bad[0]), Status: "failed", Detail: "conclusion " + bad[0].Conclusion}
		r.Publish = &res
	default:
		res := StepResult{Name: label(matched[0]), Status: "ok", Detail: fmt.Sprintf("%d matching step(s), all success", len(matched))}
		r.Publish = &res
		r.step("publish-step", "ok", res.Name)
		return nil
	}
	r.step("publish-step", "failed", msg)
	return errors.New(msg)
}

// stepMatches is an exact, case-insensitive match on the step's own name, not the job's: a job that
// happens to be called publish does not make its checkout step the publish step, and "Verify
// publish" is not "publish". A job with no steps is matched by its job name.
func stepMatches(sp Step, s Spec) bool {
	if s.PublishJob != "" && !strings.EqualFold(sp.Job, s.PublishJob) {
		return false
	}
	name := sp.Name
	if name == "" {
		name = sp.Job
	}
	return strings.EqualFold(strings.TrimSpace(name), strings.TrimSpace(s.publishStep()))
}

func (r *Record) indexed(ctx context.Context, d Deps, st *gateState) error {
	pending := map[string]bool{}
	for _, p := range st.packages {
		pending[p] = true
	}
	err := r.poll(ctx, d, d.Timings.Index, "every package to resolve on the registry", func() (bool, error) {
		var lastErr error
		for _, p := range st.packages {
			if !pending[p] {
				continue
			}
			c, cancel := d.call(ctx)
			ok, err := d.Registry.Published(c, p, st.version)
			cancel()
			if err != nil {
				lastErr = err
				continue
			}
			if ok {
				delete(pending, p)
				r.Published = append(r.Published, Published{Package: p, Version: st.version})
			}
		}
		return len(pending) == 0, lastErr
	})
	if err != nil {
		var left []string
		for _, p := range st.packages {
			if pending[p] {
				left = append(left, p)
			}
		}
		r.step("registry", "failed", "not resolving: "+strings.Join(left, ", "))
		return fmt.Errorf("%w (still missing: %s)", err, strings.Join(left, ", "))
	}
	r.step("registry", "ok", strings.Join(st.packages, ", ")+" resolve at "+st.version)
	return nil
}

// artifact reads the published metadata, not the source file: only what shipped proves what
// shipped. The metadata's own version is always asserted, then each configured line.
func (r *Record) artifact(ctx context.Context, d Deps, s Spec, st *gateState) error {
	var failed []string
	for _, p := range st.packages {
		c, cancel := d.call(ctx)
		meta, err := d.Registry.Metadata(c, p, st.version)
		cancel()
		if err != nil {
			r.step("artifact", "failed", err.Error())
			return fmt.Errorf("cannot read the published metadata of %s: %w", p, err)
		}
		got, verr := d.Registry.MetadataVersion(meta)
		a := Assertion{Package: p, Assert: "metadata version is " + st.version, Observed: got}
		a.OK = verr == nil && d.Registry.SameVersion(got, st.version)
		if verr != nil {
			a.Observed = verr.Error()
		}
		r.Assertions = append(r.Assertions, a)
		if !a.OK {
			failed = append(failed, fmt.Sprintf("%s: %s, saw %q", p, a.Assert, a.Observed))
		}
		for _, want := range s.Assert {
			w := strings.NewReplacer("{version}", st.version, "{package}", p).Replace(want)
			obs := lineContaining(string(meta), w)
			x := Assertion{Package: p, Assert: "metadata contains " + w, OK: obs != "", Observed: obs}
			r.Assertions = append(r.Assertions, x)
			if !x.OK {
				failed = append(failed, fmt.Sprintf("%s: metadata does not contain %q", p, w))
			}
		}
	}
	if len(failed) > 0 {
		msg := "the published artifact does not match: " + strings.Join(failed, "; ")
		r.step("artifact", "failed", msg)
		return errors.New(msg)
	}
	r.step("artifact", "ok", fmt.Sprintf("%d assertion(s) held", len(r.Assertions)))
	return nil
}

func lineContaining(text, sub string) string {
	for _, l := range strings.Split(text, "\n") {
		if strings.Contains(l, sub) {
			l = strings.TrimSpace(l)
			if len(l) > 240 {
				l = l[:240]
			}
			return l
		}
	}
	return ""
}

func (r *Record) release(ctx context.Context, d Deps, st *gateState, gh GitHub) error {
	c, cancel := d.call(ctx)
	u, exists, err := gh.ReleaseURL(c, st.repo, st.tag)
	cancel()
	if err != nil {
		r.step("github-release", "failed", err.Error())
		return err
	}
	if exists {
		r.Release = u
		r.step("github-release", "skipped", "already exists: "+u)
		return nil
	}
	c, cancel = d.call(ctx)
	defer cancel()
	u, err = gh.CreateRelease(c, st.repo, st.tag, st.tag, st.notes)
	if err != nil {
		r.step("github-release", "failed", err.Error())
		return fmt.Errorf("published and verified, but the GitHub release was not created: %w", err)
	}
	r.Release = u
	r.step("github-release", "ok", u)
	return nil
}

// RecordPath is where a record is kept under home, one file per name and version.
// It refuses a name or version that would put the file anywhere else.
func RecordPath(home, name, version string) (string, error) {
	base := filepath.Join(home, "ship", name)
	p := filepath.Join(base, version+".json")
	root := filepath.Join(home, "ship")
	if name == "" || version == "" || !strings.HasPrefix(base, root+string(filepath.Separator)) || filepath.Dir(p) != base {
		return "", fmt.Errorf("the record path for %q %q would leave %s: use a plain stack name and version", name, version, root)
	}
	return p, nil
}
