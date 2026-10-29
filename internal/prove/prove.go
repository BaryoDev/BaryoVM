// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package prove turns the record `baryovm ship` stored into one page a client can read. It renders
// recorded facts and nothing else: it never calls GitHub, a registry or a VM, and a fact the record
// does not hold is printed as not checked. It never prints; the caller does.
package prove

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BaryoDev/BaryoVM/internal/scan"
	"github.com/BaryoDev/BaryoVM/internal/ship"
)

// Section states.
const (
	OK         = "ok"
	Failed     = "failed"
	NotChecked = "not checked"
)

// Proof is the page. The text and the JSON are both rendered from it.
type Proof struct {
	Name       string   `json:"name"`
	Version    string   `json:"version"`
	Tag        string   `json:"tag,omitempty"`
	Commit     string   `json:"commit,omitempty"`
	Repo       string   `json:"repo,omitempty"`
	Registry   string   `json:"registry,omitempty"`
	Stage      string   `json:"stage"`
	StartedAt  string   `json:"startedAt,omitempty"`
	FinishedAt string   `json:"finishedAt,omitempty"`
	OK         bool     `json:"ok"`
	Failures   []string `json:"failures"`

	Shipped   Shipped   `json:"shipped"`
	Published Published `json:"published"`
	Deployed  Deployed  `json:"deployed"`
}

// Shipped is the source diff since the previous tag.
type Shipped struct {
	Status    string   `json:"status"`
	Note      string   `json:"note,omitempty"`
	From      string   `json:"from,omitempty"`
	To        string   `json:"to,omitempty"`
	ShortStat string   `json:"shortStat,omitempty"`
	Files     []string `json:"files,omitempty"`
	Commits   []string `json:"commits,omitempty"`
}

// Published is what the registry showed after the tag.
type Published struct {
	Status      string           `json:"status"`
	Note        string           `json:"note,omitempty"`
	Packages    []ship.Published `json:"packages,omitempty"`
	Assertions  []ship.Assertion `json:"assertions,omitempty"`
	PublishStep *ship.StepResult `json:"publishStep,omitempty"`
	Run         string           `json:"run,omitempty"`
	Release     string           `json:"release,omitempty"`
}

// Deployed is the deployment check the gate recorded. Without one it says deployment was not verified.
type Deployed struct {
	Status string `json:"status"`
	Note   string `json:"note"`
}

// Build renders a record into a Proof. A dry run is refused: it released nothing.
func Build(rec *ship.Record) (*Proof, error) {
	if rec == nil {
		return nil, errors.New("no record")
	}
	rec = cleanRecord(rec)
	if rec.DryRun {
		return nil, fmt.Errorf("the record for %s %s is from a dry run: nothing was released, so there is nothing to prove", rec.Name, rec.Version)
	}
	p := &Proof{Name: rec.Name, Version: rec.Version, Tag: rec.Tag, Commit: rec.Commit, Repo: rec.Repo,
		Registry: rec.Registry, Stage: rec.Stage, StartedAt: rec.StartedAt, FinishedAt: rec.FinishedAt, Failures: []string{}}

	if !rec.OK {
		msg := "the release did not finish"
		if rec.Error != "" {
			msg += ": " + rec.Error
		}
		p.Failures = append(p.Failures, msg)
	}
	for _, c := range rec.Checks {
		if !c.OK {
			p.Failures = append(p.Failures, "check "+c.Name+" failed: "+c.Reason)
		}
	}
	for _, s := range rec.Steps {
		if s.Status == "failed" {
			p.Failures = append(p.Failures, "step "+s.Name+" failed: "+s.Detail)
		}
	}
	for _, a := range rec.Assertions {
		if !a.OK {
			p.Failures = append(p.Failures, fmt.Sprintf("assertion failed: %s %s (observed %q)", a.Package, a.Assert, a.Observed))
		}
	}

	p.shipped(rec)
	p.published(rec)
	p.deployed(rec)
	p.OK = len(p.Failures) == 0
	return p, nil
}

func (p *Proof) shipped(rec *ship.Record) {
	d := rec.Diff
	if d == nil {
		p.Shipped = Shipped{Status: NotChecked, Note: "the record holds no source diff"}
		return
	}
	p.Shipped = Shipped{Status: OK, From: d.From, To: d.To, ShortStat: d.ShortStat, Files: d.Files, Commits: d.Commits}
	if d.From == "" {
		p.Shipped.Note = "no previous tag, so no earlier version to diff against"
	}
}

func (p *Proof) published(rec *ship.Record) {
	pub := Published{Packages: rec.Published, Assertions: rec.Assertions, PublishStep: rec.Publish, Release: rec.Release}
	if rec.Run != nil {
		pub.Run = rec.Run.URL
	}
	failed := rec.Publish != nil && rec.Publish.Status == "failed"
	for _, a := range rec.Assertions {
		if !a.OK {
			failed = true
		}
	}
	switch {
	case failed:
		pub.Status = Failed
	case len(rec.Published) == 0 && len(rec.Assertions) == 0 && rec.Publish == nil:
		pub.Status = NotChecked
		if rec.Stage == ship.StageTag {
			pub.Note = "this release was made with stack publish, which stops at the tag: nothing about the published artifact was checked"
		} else {
			pub.Note = "the record holds no registry result"
		}
	default:
		pub.Status = OK
		if len(rec.Assertions) == 0 {
			pub.Note = "no artifact assertions were recorded"
		}
	}
	p.Published = pub
}

func (p *Proof) deployed(rec *ship.Record) {
	for _, c := range rec.Checks {
		if c.Name != ship.CheckDeployed {
			continue
		}
		switch {
		case !c.OK:
			p.Deployed = Deployed{Status: Failed, Note: "the deployed check failed: " + c.Reason}
		case c.Observed == "" || c.Observed == "not required":
			p.Deployed = Deployed{Status: NotChecked, Note: "deployment not verified: the publish spec does not require a deployed stack"}
		default:
			p.Deployed = Deployed{Status: OK, Note: "at the release gate: " + c.Observed}
		}
		return
	}
	p.Deployed = Deployed{Status: NotChecked, Note: "deployment not verified: the record holds no deployed check"}
}

// Text is the plain page.
func (p *Proof) Text() string {
	var b strings.Builder
	date := p.FinishedAt
	if date == "" {
		date = p.StartedAt
	}
	head := p.Name + " " + p.Version
	right := strings.TrimSpace(shortSHA(p.Commit) + "  " + humanDate(date))
	pad := 60 - len(head) - len(right)
	if pad < 2 {
		pad = 2
	}
	fmt.Fprintf(&b, "%s%s%s\n\n", head, strings.Repeat(" ", pad), right)

	row := func(label, status string, lines ...string) {
		tag := strings.ToUpper(label)
		switch status {
		case Failed:
			tag += " (FAILED)"
		case NotChecked:
			tag += " (not checked)"
		}
		fmt.Fprintf(&b, "%s\n", tag)
		for _, l := range lines {
			fmt.Fprintf(&b, "  %s\n", l)
		}
		b.WriteString("\n")
	}

	s := p.Shipped
	var sl []string
	if s.Status == NotChecked {
		sl = append(sl, s.Note)
	} else {
		if s.From != "" {
			sl = append(sl, fmt.Sprintf("source changes since %s, up to %s", s.From, s.To))
		} else {
			sl = append(sl, "source up to "+s.To)
		}
		if s.Note != "" {
			sl = append(sl, s.Note)
		}
		if s.ShortStat != "" {
			sl = append(sl, s.ShortStat)
		}
		for _, f := range s.Files {
			sl = append(sl, "file: "+f)
		}
		for _, c := range s.Commits {
			sl = append(sl, "commit: "+c)
		}
	}
	row("shipped", s.Status, sl...)

	pb := p.Published
	var pl []string
	if pb.Note != "" {
		pl = append(pl, pb.Note)
	}
	if len(pb.Packages) > 0 {
		pl = append(pl, fmt.Sprintf("%d %s resolved", len(pb.Packages), plural(len(pb.Packages), "package")))
		for _, k := range pb.Packages {
			pl = append(pl, fmt.Sprintf("  %s at %s", k.Package, k.Version))
		}
	}
	if pb.PublishStep != nil {
		pl = append(pl, fmt.Sprintf("publish step %s: %s", pb.PublishStep.Name, pb.PublishStep.Status))
	}
	for _, a := range pb.Assertions {
		mark := "ok"
		if !a.OK {
			mark = "FAILED"
		}
		l := fmt.Sprintf("%s: %s: %s", mark, a.Package, a.Assert)
		if a.Observed != "" {
			l += " (observed: " + a.Observed + ")"
		}
		pl = append(pl, l)
	}
	if pb.Run != "" {
		pl = append(pl, "workflow run: "+pb.Run)
	}
	if pb.Release != "" {
		pl = append(pl, "release: "+pb.Release)
	}
	row("published", pb.Status, pl...)

	row("deployed", p.Deployed.Status, p.Deployed.Note)

	if p.OK {
		b.WriteString("RESULT  every recorded step and assertion passed\n")
	} else {
		b.WriteString("RESULT  FAILED\n")
		for _, f := range p.Failures {
			fmt.Fprintf(&b, "  %s\n", f)
		}
	}
	return b.String()
}

func plural(n int, w string) string {
	if n == 1 {
		return w
	}
	return w + "s"
}

func shortSHA(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}

// humanDate turns an RFC3339 stamp into "19 Aug 2026", or returns it unchanged.
func humanDate(s string) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	return t.Format("02 Jan 2006")
}

// Load reads the record for name at version. An empty version means the highest recorded one.
// A record that is missing is refused with the path that was looked for.
func Load(home, name, version string) (*ship.Record, error) {
	if version == "" {
		v, err := Latest(home, name)
		if err != nil {
			return nil, err
		}
		version = v
	} else if err := ship.ValidVersion(version); err != nil {
		return nil, err
	}
	path, err := ship.RecordPath(home, name, version)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("no release record for %s %s: looked for %s (it is written by `baryovm ship`)", name, version, path)
	}
	if err != nil {
		return nil, err
	}
	var rec ship.Record
	if err := json.Unmarshal(b, &rec); err != nil {
		return nil, fmt.Errorf("the record %s is not readable: %s", path, scan.CleanText(err.Error()))
	}
	if rec.Schema != 1 {
		return nil, fmt.Errorf("the record %s has schema %d, this build reads schema 1", path, rec.Schema)
	}
	if rec.Name != name || rec.Version != version {
		return nil, fmt.Errorf("the record %s says %s %s, not %s %s: refusing to present it", path, scan.CleanText(rec.Name), scan.CleanText(rec.Version), name, version)
	}
	return &rec, nil
}

// Latest is the highest recorded version of name by semver order, never by file time.
func Latest(home, name string) (string, error) {
	// RecordPath validates the name; its directory is the one that is listed.
	probe, err := ship.RecordPath(home, name, "0")
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(probe)
	ents, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("no release records for %s: looked in %s (they are written by `baryovm ship`)", name, dir)
	}
	if err != nil {
		return "", err
	}
	var vs []string
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".json") {
			continue
		}
		v := strings.TrimSuffix(n, ".json")
		if ship.ValidVersion(v) == nil {
			vs = append(vs, v)
		}
	}
	if len(vs) == 0 {
		return "", fmt.Errorf("no release records for %s: %s holds none", name, dir)
	}
	sort.Slice(vs, func(i, j int) bool { return CompareVersions(vs[i], vs[j]) < 0 })
	return vs[len(vs)-1], nil
}

// CompareVersions orders two versions by semver: numeric parts first, a prerelease below its
// release, prerelease identifiers compared one by one, build metadata ignored.
func CompareVersions(a, b string) int {
	an, ap := split(a)
	bn, bp := split(b)
	n := len(an)
	if len(bn) > n {
		n = len(bn)
	}
	for i := 0; i < n; i++ {
		x, y := 0, 0
		if i < len(an) {
			x = an[i]
		}
		if i < len(bn) {
			y = bn[i]
		}
		if x != y {
			return cmpInt(x, y)
		}
	}
	switch {
	case ap == "" && bp == "":
		return 0
	case ap == "":
		return 1
	case bp == "":
		return -1
	}
	as, bs := strings.Split(ap, "."), strings.Split(bp, ".")
	m := len(as)
	if len(bs) < m {
		m = len(bs)
	}
	for i := 0; i < m; i++ {
		xi, xerr := strconv.Atoi(as[i])
		yi, yerr := strconv.Atoi(bs[i])
		switch {
		case xerr == nil && yerr == nil:
			if xi != yi {
				return cmpInt(xi, yi)
			}
		case xerr == nil:
			return -1
		case yerr == nil:
			return 1
		default:
			if c := strings.Compare(as[i], bs[i]); c != 0 {
				return c
			}
		}
	}
	return cmpInt(len(as), len(bs))
}

func split(v string) ([]int, string) {
	if i := strings.IndexByte(v, '+'); i >= 0 {
		v = v[:i]
	}
	pre := ""
	if i := strings.IndexByte(v, '-'); i >= 0 {
		v, pre = v[:i], v[i+1:]
	}
	var n []int
	for _, s := range strings.Split(v, ".") {
		x, _ := strconv.Atoi(s)
		n = append(n, x)
	}
	return n, pre
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// cleanRecord returns a copy of rec with every string passed through scan.CleanText. The strings
// come from outside BaryoVM (git subjects, registry metadata, the GitHub API, a hand-edited file)
// and must not carry terminal control sequences or bidi overrides to a screen or into JSON.
func cleanRecord(rec *ship.Record) *ship.Record {
	c := scan.CleanText
	out := *rec
	out.Name, out.Stage, out.Version, out.Tag = c(rec.Name), c(rec.Stage), c(rec.Version), c(rec.Tag)
	out.Commit, out.Repo, out.Registry = c(rec.Commit), c(rec.Repo), c(rec.Registry)
	out.StartedAt, out.FinishedAt, out.Release, out.Error = c(rec.StartedAt), c(rec.FinishedAt), c(rec.Release), c(rec.Error)
	out.Checks = nil
	for _, k := range rec.Checks {
		out.Checks = append(out.Checks, ship.Check{Name: c(k.Name), OK: k.OK, Observed: c(k.Observed), Reason: c(k.Reason)})
	}
	out.Plan = nil
	for _, s := range rec.Plan {
		out.Plan = append(out.Plan, c(s))
	}
	if rec.Diff != nil {
		d := *rec.Diff
		d.From, d.To, d.ShortStat = c(d.From), c(d.To), c(d.ShortStat)
		d.Files, d.Commits = cleanAll(d.Files), cleanAll(d.Commits)
		out.Diff = &d
	}
	out.Steps = nil
	for _, s := range rec.Steps {
		out.Steps = append(out.Steps, cleanStep(s))
	}
	if rec.Run != nil {
		r := *rec.Run
		r.Name, r.Path, r.URL, r.Status, r.Conclusion, r.HeadSHA = c(r.Name), c(r.Path), c(r.URL), c(r.Status), c(r.Conclusion), c(r.HeadSHA)
		out.Run = &r
	}
	if rec.Publish != nil {
		s := cleanStep(*rec.Publish)
		out.Publish = &s
	}
	out.Published = nil
	for _, p := range rec.Published {
		out.Published = append(out.Published, ship.Published{Package: c(p.Package), Version: c(p.Version)})
	}
	out.Assertions = nil
	for _, a := range rec.Assertions {
		out.Assertions = append(out.Assertions, ship.Assertion{Package: c(a.Package), Assert: c(a.Assert), OK: a.OK, Observed: c(a.Observed)})
	}
	return &out
}

func cleanStep(s ship.StepResult) ship.StepResult {
	return ship.StepResult{Name: scan.CleanText(s.Name), Status: scan.CleanText(s.Status), Detail: scan.CleanText(s.Detail)}
}

func cleanAll(in []string) []string {
	var out []string
	for _, s := range in {
		out = append(out, scan.CleanText(s))
	}
	return out
}
