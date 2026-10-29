// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package verify is the one definition of "the deploy worked" that release, update and rollback
// share.
//
// A stack carries a Spec in fleet.json: a list of checks, each a URL path with assertions on what
// it serves or a command, plus how long to keep trying. Spec.Plan turns it into commands to run on
// the VM and a verdict function per command, and Retry runs them under a bounded budget.
//
// Before this package, release and update each had their own notion of healthy with their own
// patience, and "any 2xx" was one of them. A site serving a framework's install screen answers 200
// to all of them. A check that names a marker the real site serves, and fails when it is missing,
// is the difference.
//
// The package builds command strings and judges their output and never dials anything, so all of
// it is testable without a host. It imports only sshx, so internal/fleet can hold the Spec type.
package verify

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// Limits on what a spec may ask for. A hand-edited fleet.json is the only way past the CLI, and a
// check that fetches without end or compares against a megabyte of markers is a misconfiguration
// this tool should refuse rather than run.
const (
	maxChecks     = 32
	maxStrings    = 64
	maxStringLen  = 1024
	maxCommandLen = 4096
)

// Spec is a stack's verify spec. The zero value, and a nil *Spec, mean "nothing configured here":
// the stack's healthUrl, if any, is then the single check.
type Spec struct {
	// BaseURL is the origin the checks' paths are fetched from, e.g. http://127.0.0.1:8091. Empty
	// means the origin of the stack's healthUrl. Only an origin: the paths below are the routes.
	BaseURL string `json:"baseUrl,omitempty"`

	// Checks run in order on every attempt; the first to fail ends the attempt.
	Checks []Check `json:"checks,omitempty"`

	// Attempts, DelaySeconds and TimeoutSeconds bound how long to wait for a stack to come back.
	// Zero means the caller's default (release 5 x 3s, update 20 x 3s). Timeout is the total
	// wall-clock budget, so a slow check cannot stretch a few attempts into an hour.
	Attempts       int `json:"attempts,omitempty"`
	DelaySeconds   int `json:"delaySeconds,omitempty"`
	TimeoutSeconds int `json:"timeoutSeconds,omitempty"`

	// extra holds keys a newer BaryoVM wrote that this one does not know, so a downgrade does not
	// delete them. Plan refuses a spec that has any: a mistyped assertion key must not turn into a
	// check that silently asserts nothing.
	extra map[string]json.RawMessage
}

// Check is one thing to verify. Exactly one of Path and Command is set.
type Check struct {
	// Path is a route on the stack's own origin, starting with "/". It is deliberately not a URL:
	// a check can only ask the stack it belongs to, never another host. A recipe or a project file
	// that supplies a spec cannot point a fetch elsewhere because there is no field to do it with.
	Path string `json:"path,omitempty"`

	// Status is the expected HTTP status. Zero means any 2xx.
	Status int `json:"status,omitempty"`
	// MinBytes is the smallest body that counts as served.
	MinBytes int `json:"minBytes,omitempty"`
	// Contains lists strings the body must contain, e.g. a marker only the real site serves.
	Contains []string `json:"contains,omitempty"`
	// Absent lists strings the body must not contain, evaluated only after the status and the body
	// size passed (see judgeURL).
	Absent []string `json:"absent,omitempty"`
	// JSONMatch asserts values in a JSON body. Keys are dotted paths ("build.version"), values are
	// compared as text.
	JSONMatch map[string]string `json:"jsonMatch,omitempty"`

	// Command runs on the VM in the stack's directory and passes when it exits zero.
	Command string `json:"command,omitempty"`

	// rawURL is set only for the check made from a bare healthUrl, which is fetched verbatim. It
	// has no JSON form, so a spec read from a file can never carry one.
	rawURL string
	extra  map[string]json.RawMessage
}

// FromHealthURL is what a stack with only a healthUrl means: one check that wants any 2xx from
// that exact URL.
func FromHealthURL(healthURL string) Spec {
	return Spec{Checks: []Check{{rawURL: healthURL}}}
}

// HasChecks reports whether the spec names anything to run.
func (s *Spec) HasChecks() bool { return s != nil && len(s.Checks) > 0 }

// Validate reports the first thing wrong with the spec, with the check's position.
func (s *Spec) Validate() error {
	if s == nil {
		return nil
	}
	if len(s.extra) > 0 {
		return fmt.Errorf("verify: unknown field(s) %s: a mistyped key would verify nothing, and a newer baryovm may know it", keys(s.extra))
	}
	if len(s.Checks) > maxChecks {
		return fmt.Errorf("verify: %d checks, at most %d", len(s.Checks), maxChecks)
	}
	if s.Attempts < 0 || s.DelaySeconds < 0 || s.TimeoutSeconds < 0 {
		return fmt.Errorf("verify: attempts, delaySeconds and timeoutSeconds cannot be negative")
	}
	if s.BaseURL != "" {
		if _, err := origin(s.BaseURL); err != nil {
			return fmt.Errorf("verify: baseUrl: %w", err)
		}
	}
	for i, c := range s.Checks {
		if err := c.validate(); err != nil {
			return fmt.Errorf("verify: check %d: %w", i+1, err)
		}
	}
	return nil
}

func (c Check) validate() error {
	if len(c.extra) > 0 {
		return fmt.Errorf("unknown field(s) %s: a mistyped assertion would check nothing", keys(c.extra))
	}
	if c.rawURL != "" {
		return nil
	}
	hasCmd := strings.TrimSpace(c.Command) != ""
	hasPath := c.Path != ""
	switch {
	case hasCmd && hasPath:
		return fmt.Errorf("set either path or command, not both")
	case !hasCmd && !hasPath:
		return fmt.Errorf("set a path or a command")
	}
	if hasCmd {
		if len(c.Command) > maxCommandLen {
			return fmt.Errorf("command is longer than %d bytes", maxCommandLen)
		}
		if c.Status != 0 || c.MinBytes != 0 || len(c.Contains)+len(c.Absent)+len(c.JSONMatch) > 0 {
			return fmt.Errorf("a command check passes on exit zero: status, minBytes, contains, absent and jsonMatch belong to a path check")
		}
		return nil
	}
	if err := validPath(c.Path); err != nil {
		return err
	}
	if c.Status != 0 && (c.Status < 100 || c.Status > 599) {
		return fmt.Errorf("status %d is not an HTTP status", c.Status)
	}
	if c.MinBytes < 0 {
		return fmt.Errorf("minBytes cannot be negative")
	}
	if len(c.Contains) > maxStrings || len(c.Absent) > maxStrings || len(c.JSONMatch) > maxStrings {
		return fmt.Errorf("at most %d entries per assertion list", maxStrings)
	}
	for _, list := range [][]string{c.Contains, c.Absent} {
		for _, s := range list {
			if s == "" || len(s) > maxStringLen {
				return fmt.Errorf("an assertion string must be 1 to %d bytes", maxStringLen)
			}
		}
	}
	for k, v := range c.JSONMatch {
		if k == "" || len(k) > maxStringLen || len(v) > maxStringLen {
			return fmt.Errorf("a jsonMatch key must be 1 to %d bytes", maxStringLen)
		}
	}
	return nil
}

// validPath accepts only a route on the origin: it must start with a single slash. "//host/x"
// is a scheme-relative URL naming another host, a backslash is read as a slash by some clients,
// and whitespace or control characters have no place in a path we put in a shell argument.
func validPath(p string) error {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") {
		return fmt.Errorf("path %q must be a route starting with a single /, not a URL", p)
	}
	if len(p) > maxStringLen {
		return fmt.Errorf("path is longer than %d bytes", maxStringLen)
	}
	for _, r := range p {
		if r <= ' ' || r == 0x7f || r == '\\' {
			return fmt.Errorf("path %q contains a space, control character or backslash", p)
		}
	}
	return nil
}

// origin reduces a URL to scheme://host[:port], refusing anything that is not plain http(s) with
// no credentials.
func origin(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("%q is not a URL", raw)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("%q must be an http or https URL", raw)
	}
	if u.Hostname() == "" {
		return "", fmt.Errorf("%q has no host", raw)
	}
	if u.User != nil {
		return "", fmt.Errorf("%q carries credentials: a URL here is written to the process list of the VM", raw)
	}
	return u.Scheme + "://" + u.Host, nil
}

func keys(m map[string]json.RawMessage) string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return strings.Join(ks, ", ")
}

// The unknown-field handling below keeps what this version does not recognise, the way fleet.json
// does for stacks (internal/fleet/unknown.go), so editing a spec with an older binary is lossless.

func capture(b []byte, known ...string) (map[string]json.RawMessage, error) {
	var all map[string]json.RawMessage
	if err := json.Unmarshal(b, &all); err != nil {
		return nil, err
	}
	for _, k := range known {
		delete(all, k)
	}
	if len(all) == 0 {
		return nil, nil
	}
	return all, nil
}

func merge(b []byte, extra map[string]json.RawMessage) ([]byte, error) {
	if len(extra) == 0 {
		return b, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	for k, v := range extra {
		if _, taken := m[k]; !taken {
			m[k] = v
		}
	}
	return json.Marshal(m)
}

func (s *Spec) UnmarshalJSON(b []byte) error {
	type plain Spec
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	extra, err := capture(b, "baseUrl", "checks", "attempts", "delaySeconds", "timeoutSeconds")
	if err != nil {
		return err
	}
	*s = Spec(p)
	s.extra = extra
	return nil
}

func (s Spec) MarshalJSON() ([]byte, error) {
	type plain Spec
	b, err := json.Marshal(plain(s))
	if err != nil {
		return nil, err
	}
	return merge(b, s.extra)
}

func (c *Check) UnmarshalJSON(b []byte) error {
	type plain Check
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	extra, err := capture(b, "path", "status", "minBytes", "contains", "absent", "jsonMatch", "command")
	if err != nil {
		return err
	}
	*c = Check(p)
	c.extra = extra
	return nil
}

func (c Check) MarshalJSON() ([]byte, error) {
	type plain Check
	b, err := json.Marshal(plain(c))
	if err != nil {
		return nil, err
	}
	return merge(b, c.extra)
}
