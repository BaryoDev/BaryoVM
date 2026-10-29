// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package verify

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BaryoDev/BaryoVM/internal/sshx"
	"golang.org/x/crypto/ssh"
)

// Mode says who is verifying, because a bare healthUrl has always meant two slightly different
// things and "verifies exactly as today" includes that.
//
// Release probed it with `curl --fail`, which passes a 3xx. Update asked for the status code and
// wanted a 2xx. Both are kept for a check that asserts nothing beyond "answers"; the moment a
// check asserts anything (a status, a marker) the two modes judge it identically.
type Mode int

const (
	ForRelease Mode = iota
	ForUpdate
)

// Source names where a plan came from, for the report a release prints.
const (
	SourceSpec      = "verify"
	SourceHealthURL = "healthUrl"
	SourceNone      = "none"
)

// Context is what a plan needs to know about the stack it verifies.
type Context struct {
	Mode Mode
	// HealthURL is the stack's healthUrl: the whole check when there is no spec, and the origin the
	// spec's paths are fetched from when the spec sets no baseUrl.
	HealthURL string
	// Dir and Sudo place a command check: in the stack's directory, as root if the stack is.
	Dir  string
	Sudo bool
	// DefaultAttempts and DefaultDelay apply when the spec sets none. Release and update differ.
	DefaultAttempts int
	DefaultDelay    time.Duration
	// CommandTimeout bounds one command check on the VM. Plan sets it from the spec's timeout.
	CommandTimeout time.Duration
}

// commandJudge passes on exit zero. timeout(1) exits 124 when it killed the command, which is
// reported as a timeout and not as a plain failure.
func commandJudge(command string, secs int) func(string, error) (bool, string) {
	return func(out string, runErr error) (bool, string) {
		if runErr != nil && exitCode(runErr) == 124 {
			return false, fmt.Sprintf("timed out after %ds: %s", secs, command)
		}
		return judgeExit(out, runErr)
	}
}

func exitCode(err error) int {
	var ee *ssh.ExitError
	if errors.As(err, &ee) {
		return ee.ExitStatus()
	}
	if strings.Contains(err.Error(), "exit status 124") {
		return 124
	}
	return 0
}

// Plan is what to run on the VM, and how long to keep trying.
type Plan struct {
	Source string
	Steps  []Step
	Budget Budget
}

// Empty reports whether there is nothing to verify with.
func (p Plan) Empty() bool { return len(p.Steps) == 0 }

// Commands lists the command of every step, in order.
func (p Plan) Commands() []string {
	out := make([]string, 0, len(p.Steps))
	for _, s := range p.Steps {
		out = append(out, s.Cmd)
	}
	return out
}

// Step is one command run on the VM and the verdict on what it printed.
type Step struct {
	// Label names the step in a failure: "GET /", "command: sh check.sh".
	Label string
	Cmd   string
	judge func(out string, runErr error) (bool, string)
}

// ExitStep passes when cmd exits zero. It is how manifest verify commands and command checks are
// judged.
func ExitStep(label, cmd string) Step {
	return Step{Label: label, Cmd: cmd, judge: judgeExit}
}

// Judge returns whether the step passed, and when it did not, why.
func (s Step) Judge(out string, runErr error) (bool, string) {
	if s.judge == nil {
		return judgeExit(out, runErr)
	}
	return s.judge(out, runErr)
}

func judgeExit(out string, runErr error) (bool, string) {
	if runErr != nil {
		return false, withOutput(runErr.Error(), out)
	}
	return true, ""
}

func withOutput(msg, out string) string {
	if t := lastLines(out, 4); t != "" {
		return msg + ": " + t
	}
	return msg
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// Plan builds what to run for a stack. spec may be nil.
//
// The order of precedence is the one the callers already had: a spec with checks is the stack's
// definition of healthy; with none, the healthUrl is the single check; with neither the plan is
// empty and the caller decides whether that is a warning (release) or a refusal (update --auto).
func (s *Spec) Plan(ctx Context) (Plan, error) {
	if err := s.Validate(); err != nil {
		return Plan{}, err
	}
	p := Plan{Source: SourceNone, Budget: s.budget(ctx)}
	ctx.CommandTimeout = p.Budget.Timeout

	checks := []Check(nil)
	switch {
	case s.HasChecks():
		checks = s.Checks
		p.Source = SourceSpec
	case strings.TrimSpace(ctx.HealthURL) != "":
		checks = FromHealthURL(ctx.HealthURL).Checks
		p.Source = SourceHealthURL
	}

	base := ""
	if s != nil {
		base = s.BaseURL
	}
	if base == "" {
		base = ctx.HealthURL
	}
	for i, c := range checks {
		step, err := c.step(ctx, base)
		if err != nil {
			return Plan{}, fmt.Errorf("verify: check %d: %w", i+1, err)
		}
		p.Steps = append(p.Steps, step)
	}
	return p, nil
}

func (s *Spec) budget(ctx Context) Budget {
	b := Budget{Attempts: ctx.DefaultAttempts, Delay: ctx.DefaultDelay}
	if s != nil {
		if s.Attempts > 0 {
			b.Attempts = s.Attempts
		}
		if s.DelaySeconds > 0 {
			b.Delay = time.Duration(s.DelaySeconds) * time.Second
		}
		b.Timeout = time.Duration(s.TimeoutSeconds) * time.Second
	}
	// Timeout stays zero unless the spec set it: a flag or the manifest may still change attempts
	// and delay, and the caller's Bounded() derives the time from the final values.
	explicit := b.Timeout
	b = b.Bounded()
	b.Timeout = explicit
	if b.Timeout > MaxTimeout {
		b.Timeout = MaxTimeout
	}
	return b
}

func (c Check) step(ctx Context, base string) (Step, error) {
	if c.rawURL != "" {
		return plainStep(ctx.Mode, "GET "+c.rawURL, c.rawURL), nil
	}
	if strings.TrimSpace(c.Command) != "" {
		cmd := c.Command
		if d := strings.TrimSpace(ctx.Dir); d != "" {
			cmd = "cd " + sshx.Quote(d) + " && " + cmd
		}
		secs := int(ctx.CommandTimeout / time.Second)
		if secs < 1 {
			secs = int(DefaultTimeout / time.Second)
		}
		// timeout runs inside the shell sudo starts, so a hung command is killed on the VM.
		cmd = fmt.Sprintf("timeout %d sh -c %s", secs, sshx.Quote(cmd))
		return Step{Label: "command: " + c.Command, Cmd: sshx.SudoShell(ctx.Sudo, cmd), judge: commandJudge(c.Command, secs)}, nil
	}
	full, err := c.fullURL(base)
	if err != nil {
		return Step{}, err
	}
	// A path check in a spec is always judged on the status it returns: 2xx unless it names one,
	// and no status at all fails. Only the healthUrl one-check keeps the old exit-code probe.
	return Step{Label: "GET " + c.Path, Cmd: fetchCmd(full), judge: c.judgeURL}, nil
}

// fullURL joins the check's path onto the stack's own origin, and then checks that the result still
// names that origin. The path validation already rules out a second host; this is the same rule
// asked of the finished URL, so a gap in one is caught by the other.
func (c Check) fullURL(base string) (string, error) {
	if strings.TrimSpace(base) == "" {
		return "", fmt.Errorf("path %q needs a stack healthUrl or a baseUrl to say which site to ask", c.Path)
	}
	o, err := origin(base)
	if err != nil {
		return "", err
	}
	full := o + c.Path
	u, err := url.Parse(full)
	if err != nil {
		return "", fmt.Errorf("path %q makes no URL: %w", c.Path, err)
	}
	b, _ := url.Parse(o)
	if u.Host != b.Host || u.Scheme != b.Scheme || u.User != nil {
		return "", fmt.Errorf("path %q would leave the stack's own origin %s", c.Path, o)
	}
	return full, nil
}

// curlGuards are on every URL fetch. No -L, and --max-redirs 0 as well so the intent is in the
// command: a redirect to another host is a 3xx answer to be judged, never followed. --proto stops a
// redirect or a URL from switching to file:// or gopher://.
const curlGuards = "--globoff --silent --show-error --max-time 10 --max-redirs 0 --proto '=http,https'"

// plainStep is the old healthUrl probe, kept as it was for each caller.
func plainStep(mode Mode, label, fullURL string) Step {
	if mode == ForUpdate {
		return Step{
			Label: label,
			Cmd:   "curl -s -o /dev/null -w '%{http_code}' --max-time 10 --max-redirs 0 " + sshx.Quote(fullURL),
			judge: func(out string, runErr error) (bool, string) {
				if HealthyFromProbe(out, runErr) {
					return true, ""
				}
				return false, probeReason(out, runErr)
			},
		}
	}
	return Step{
		Label: label,
		Cmd:   "curl --fail --silent --show-error --max-time 10 -o /dev/null " + sshx.Quote(fullURL),
		judge: judgeExit,
	}
}

// HealthyFromProbe reads a `curl -w '%{http_code}'` probe: a 2xx is healthy.
//
// A container that is still starting, or has just died, refuses the connection and curl exits
// non-zero (7). That is the ordinary case while waiting, not a fault in the check, so it is "not
// healthy yet" and never an error. Reporting it as an error made a failed update read as though
// the health check itself had broken, which sent the reader looking in the wrong place.
func HealthyFromProbe(out string, runErr error) bool {
	if runErr != nil {
		return false
	}
	return strings.HasPrefix(strings.TrimSpace(out), "2")
}

func probeReason(out string, runErr error) string {
	if runErr != nil {
		return "no answer: " + runErr.Error()
	}
	if t := strings.TrimSpace(out); t != "" {
		return "status " + t + ", want 2xx"
	}
	return "no status returned"
}

const (
	statusMarker = "--baryovm-status:"
	// MaxBody caps what a fetch reads, so an assertion cannot make the VM stream a large file back.
	MaxBody = 2 << 20
)

func fetchCmd(fullURL string) string {
	return "curl " + curlGuards + " --max-filesize " + strconv.Itoa(MaxBody) +
		" --write-out " + sshx.Quote("\n"+statusMarker+"%{http_code}") + " " + sshx.Quote(fullURL)
}

// judgeURL decides a fetch, in an order that matters.
//
// The status and the size are proven first, and absent is evaluated last. An empty body, a 502 and
// a DNS failure all satisfy every "must not contain": a negative check that passes on nothing is the
// defect this package exists to remove. So absent never runs against a response that was not
// first shown to be a real body, and a body of zero bytes cannot satisfy it at all.
func (c Check) judgeURL(out string, runErr error) (bool, string) {
	if runErr != nil {
		return false, withOutput("no answer: "+runErr.Error(), "")
	}
	i := strings.LastIndex(out, "\n"+statusMarker)
	if i < 0 {
		return false, "no status in the response"
	}
	body := out[:i]
	code, err := strconv.Atoi(strings.TrimSpace(out[i+1+len(statusMarker):]))
	if err != nil {
		return false, "no status in the response"
	}

	switch {
	case c.Status != 0 && code != c.Status:
		return false, fmt.Sprintf("status %d, want %d", code, c.Status)
	case c.Status == 0 && (code < 200 || code > 299):
		return false, fmt.Sprintf("status %d, want 2xx", code)
	}
	if len(body) < c.MinBytes {
		return false, fmt.Sprintf("minBytes: body is %d bytes, want at least %d", len(body), c.MinBytes)
	}
	for _, want := range c.Contains {
		if !strings.Contains(body, want) {
			return false, fmt.Sprintf("contains: %q is not in the response", want)
		}
	}
	if len(c.JSONMatch) > 0 {
		if reason := matchJSON(body, c.JSONMatch); reason != "" {
			return false, reason
		}
	}
	if len(c.Absent) > 0 {
		if len(body) == 0 {
			return false, "absent: the body is empty, so nothing can be said to be absent from it"
		}
		for _, bad := range c.Absent {
			if strings.Contains(body, bad) {
				return false, fmt.Sprintf("absent: %q is in the response", bad)
			}
		}
	}
	return true, ""
}

func matchJSON(body string, want map[string]string) string {
	var doc any
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		return "jsonMatch: the body is not JSON"
	}
	names := make([]string, 0, len(want))
	for k := range want {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		got, ok := lookup(doc, k)
		if !ok {
			return fmt.Sprintf("jsonMatch: %q is not in the response", k)
		}
		if got != want[k] {
			return fmt.Sprintf("jsonMatch: %q is %q, want %q", k, got, want[k])
		}
	}
	return ""
}

// lookup follows a dotted path through objects and renders the value as text.
func lookup(doc any, path string) (string, bool) {
	cur := doc
	for _, part := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return "", false
		}
		if cur, ok = m[part]; !ok {
			return "", false
		}
	}
	switch v := cur.(type) {
	case string:
		return v, true
	case nil:
		return "null", true
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return "", false
		}
		return string(b), true
	}
}
