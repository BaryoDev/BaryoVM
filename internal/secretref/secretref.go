// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package secretref is the one way a secret reaches a deploy: a reference to where the value lives,
// resolved at use time, never stored (#93, #14).
//
// There are three kinds and nothing else:
//
//	env:NAME     a variable in the local environment
//	file:PATH    a local file, its trailing newline trimmed
//	cmd:<argv>   the stdout of a local command, for a signed-in CLI, e.g. cmd:gh auth token
//
// The cmd form is split into arguments and run directly, with no shell, under a timeout. Its stderr
// is thrown away, so a CLI that prints the value on the wrong stream cannot leak it into an error.
//
// A reference that does not resolve is an error that names the reference and never a value. The
// value goes to the caller and nowhere else: not into an error, a log line or a JSON envelope.
// Callers deliver it to the VM on stdin or in an owner-only file, never on a command line.
package secretref

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Kind is the part of a reference before the first colon.
type Kind string

const (
	Env  Kind = "env"
	File Kind = "file"
	Cmd  Kind = "cmd"
)

// DefaultTimeout bounds a cmd reference, so a CLI waiting on a login prompt cannot hang a release.
const DefaultTimeout = 30 * time.Second

var nameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidName reports whether name is a plain environment variable name.
func ValidName(name string) bool { return nameRe.MatchString(name) }

// Ref is a parsed reference.
type Ref struct {
	Kind    Kind
	Locator string   // NAME, PATH or the argv text
	Argv    []string // cmd only
}

// String is the reference as written.
func (r Ref) String() string { return string(r.Kind) + ":" + r.Locator }

// IsRef reports whether s starts like a reference. It does not say the reference is well formed:
// Parse does, so a typo after `env:` is an error and not a literal.
func IsRef(s string) bool {
	for _, k := range []Kind{Env, File, Cmd} {
		if strings.HasPrefix(s, string(k)+":") {
			return true
		}
	}
	return false
}

// Parse checks a reference's syntax. It touches nothing.
func Parse(s string) (Ref, error) {
	kind, loc, ok := strings.Cut(s, ":")
	if !ok {
		return Ref{}, fmt.Errorf("secret reference %q: write env:NAME, file:PATH or cmd:<command>", s)
	}
	switch Kind(kind) {
	case Env:
		if !ValidName(loc) {
			return Ref{}, fmt.Errorf("secret reference %q: %q is not a valid environment variable name", s, loc)
		}
		return Ref{Kind: Env, Locator: loc}, nil
	case File:
		if strings.TrimSpace(loc) == "" {
			return Ref{}, fmt.Errorf("secret reference %q: file: needs a path", s)
		}
		return Ref{Kind: File, Locator: loc}, nil
	case Cmd:
		argv, err := SplitArgv(loc)
		if err != nil {
			return Ref{}, fmt.Errorf("secret reference %q: %w", s, err)
		}
		if len(argv) == 0 {
			return Ref{}, fmt.Errorf("secret reference %q: cmd: needs a command", s)
		}
		return Ref{Kind: Cmd, Locator: loc, Argv: argv}, nil
	}
	return Ref{}, fmt.Errorf("secret reference %q: unknown kind %q: use env:, file: or cmd:", s, kind)
}

// SplitArgv splits a command into arguments the way a person would read it: spaces separate, and
// single or double quotes group. There is no shell: no expansion, no pipes, no redirection.
func SplitArgv(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	var quote rune
	have := false
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, have = r, true
		case r == ' ' || r == '\t':
			if have || cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
				have = false
			}
		default:
			cur.WriteRune(r)
		}
	}
	if quote != 0 {
		return nil, errors.New("unbalanced quote in the command")
	}
	if have || cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out, nil
}

// Resolver turns references into values. The fields are the seams a test replaces.
type Resolver struct {
	Getenv   func(string) string
	ReadFile func(string) ([]byte, error)
	// Run executes argv and returns its stdout. Its stderr must not be returned or logged.
	Run func(ctx context.Context, argv []string) ([]byte, error)
	// Timeout bounds Run. Zero means DefaultTimeout.
	Timeout time.Duration
	// AllowCmd lets cmd: references run. A committed file (a project file, a release manifest)
	// must not be able to run a program on the machine that reads it, so those callers leave this
	// false and only the command line, which the operator typed, sets it.
	AllowCmd bool
}

// Default is a Resolver over the real environment, file system and exec. AllowCmd is off.
func Default() Resolver {
	return Resolver{Getenv: os.Getenv, ReadFile: os.ReadFile, Run: runExec}
}

// WithCmd returns r with cmd: references allowed.
func (r Resolver) WithCmd() Resolver { r.AllowCmd = true; return r }

func runExec(ctx context.Context, argv []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) // #nosec G204 -- the operator's own reference, run without a shell
	// Output leaves cmd.Stderr nil, so stderr goes to the null device and cannot reach an error.
	return cmd.Output()
}

// Resolve returns the value ref points at. The error names the reference and the reason, never a
// value.
func (r Resolver) Resolve(ref string) (string, error) {
	p, err := Parse(ref)
	if err != nil {
		return "", err
	}
	var v string
	switch p.Kind {
	case Env:
		v = r.Getenv(p.Locator)
		if v == "" {
			return "", fmt.Errorf("secret reference %q did not resolve: %s is not set or is empty", ref, p.Locator)
		}
	case File:
		b, err := r.ReadFile(expand(p.Locator))
		if err != nil {
			return "", fmt.Errorf("secret reference %q did not resolve: cannot read the file (%s)", ref, reason(err))
		}
		v = trimNewline(string(b))
	case Cmd:
		if !r.AllowCmd {
			return "", fmt.Errorf("secret reference %q: cmd: is only accepted on the command line, not from a file that may be committed: use env: or file:", ref)
		}
		t := r.Timeout
		if t <= 0 {
			t = DefaultTimeout
		}
		ctx, cancel := context.WithTimeout(context.Background(), t)
		defer cancel()
		b, err := r.Run(ctx, p.Argv)
		if err != nil {
			if ctx.Err() != nil {
				return "", fmt.Errorf("secret reference %q did not resolve: the command took longer than %s", ref, t)
			}
			return "", fmt.Errorf("secret reference %q did not resolve: the command failed (%s): is the CLI signed in?", ref, reason(err))
		}
		v = trimNewline(string(b))
	}
	if v == "" {
		return "", fmt.Errorf("secret reference %q did not resolve: the value is empty", ref)
	}
	return v, nil
}

// reason is the cause of err without any text that could hold a value: a path error keeps its
// underlying errno, an exit error keeps its status, anything else is not repeated.
func reason(err error) string {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return fmt.Sprintf("exit status %d", ee.ExitCode())
	}
	if errors.Is(err, exec.ErrNotFound) {
		return "command not found"
	}
	if errors.Is(err, os.ErrNotExist) {
		return "no such file"
	}
	return "error"
}

func trimNewline(s string) string {
	s = strings.TrimSuffix(s, "\n")
	return strings.TrimSuffix(s, "\r")
}

func expand(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}
