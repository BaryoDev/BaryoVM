// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package site

import (
	"fmt"
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/sshx"
)

// StepFunc wraps one labelled step. The cli passes ui.Step; this package does not print.
type StepFunc func(label string, fn func() error) error

// Step names, as they appear in a Report and in a StepError.
const (
	StepInspect    = "inspect"
	StepWebroot    = "webroot"
	StepRestorecon = "restorecon"
	StepHTTPBlock  = "http block"
	StepHTTPReload = "reload (http block)"
	StepCert       = "certificate"
	StepFullBlock  = "full block"
	StepReload     = "reload"
)

// Report is what Add did, returned on success and on failure alike.
type Report struct {
	Domain     string   `json:"domain"`
	Root       string   `json:"root"`
	Config     string   `json:"config"`
	SELinux    bool     `json:"selinux"`
	Steps      []string `json:"steps"`
	FailedStep string   `json:"failedStep,omitempty"`
	RolledBack bool     `json:"rolledBack,omitempty"`
}

// StepError is a failed step. Report.FailedStep carries the same name.
type StepError struct {
	Step string
	Err  error
}

func (e *StepError) Error() string {
	return "site add failed at step \"" + e.Step + "\": " + e.Err.Error()
}
func (e *StepError) Unwrap() error { return e.Err }

func tail(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > 8 {
		lines = lines[len(lines)-8:]
	}
	return strings.Join(lines, "\n")
}

// Add provisions the site on the VM behind r.
//
// The order is fixed by the certificate: certbot's webroot challenge needs an HTTP block that is
// already serving /.well-known/acme-challenge/, and the final block needs the certificate files to
// exist or nginx -t refuses it. So: write the HTTP-only block, test, reload, get the certificate,
// write the full block, test, reload.
//
// Any failing step leaves nginx in the state it was in before the command. Once the server block
// file has been touched, a failure puts the previous file back (or removes the new one), and
// reloads nginx if an earlier step in this run had already reloaded it with the new file. The
// previous file is copied aside once, before the first write, so every rollback goes back to the
// original and not to the intermediate HTTP block.
func Add(r sshx.Runner, o Options, step StepFunc) (rep Report, err error) {
	if err := o.Validate(); err != nil {
		return Report{Domain: o.Domain, Root: o.Root}, err
	}
	rep = Report{Domain: o.Domain, Root: o.Root, Config: ConfPath(o.Domain), Steps: []string{}}

	run := func(cmd string) (string, error) {
		out, err := r.Run(cmd)
		if err != nil {
			return out, fmt.Errorf("%w: %s", err, tail(out))
		}
		return out, nil
	}

	var (
		state    State
		ssl      SSLFiles
		touched  bool // the server block file has been written
		reloaded bool // nginx has been reloaded with the new file
	)

	// rollback is the single exit for a failure after the first write.
	rollback := func(cause error) error {
		if !touched {
			return cause
		}
		if _, rerr := run(RestoreCmd(o.Domain, state != Absent)); rerr != nil {
			return fmt.Errorf("%w; restoring %s also failed, check it by hand: %v", cause, ConfPath(o.Domain), rerr)
		}
		if reloaded {
			if _, rerr := run(TestCmd()); rerr != nil {
				return fmt.Errorf("%w; the previous config was put back but nginx -t rejects it: %v", cause, rerr)
			}
			if _, rerr := run(ReloadCmd()); rerr != nil {
				return fmt.Errorf("%w; the previous config was put back but the reload failed: %v", cause, rerr)
			}
		}
		rep.RolledBack = true
		return cause
	}
	do := func(name string, fn func() error) error {
		if e := step(name, fn); e != nil {
			rep.FailedStep = name
			return &StepError{Step: name, Err: rollback(e)}
		}
		rep.Steps = append(rep.Steps, name)
		return nil
	}
	// write puts content at the path and keeps it only if nginx -t accepts it. nginx is not
	// reloaded here: a reload of a config that failed its test would take down every other site.
	write := func(content string) error {
		touched = true
		if _, e := run(WriteCmd(o.Domain, content)); e != nil {
			return e
		}
		if _, e := run(TestCmd()); e != nil {
			return fmt.Errorf("nginx -t rejected the new config, nginx was not reloaded: %w", e)
		}
		return nil
	}

	if err := do(StepInspect, func() error {
		out, e := run(ProbeConfCmd(o.Domain))
		if e != nil {
			return e
		}
		state = State(strings.TrimSpace(out))
		switch state {
		case Absent, Managed, Foreign:
		default:
			return fmt.Errorf("could not tell what is at %s: %q", ConfPath(o.Domain), strings.TrimSpace(out))
		}
		if state != Absent && !o.Force {
			return fmt.Errorf("%s already exists on the VM: pass --force to replace it (the old file is kept if a step fails)", ConfPath(o.Domain))
		}
		if out, e = run(ProbeSSLCmd()); e != nil {
			return e
		}
		ssl = SSLFiles{Options: strings.Contains(out, "options"), DHParam: strings.Contains(out, "dhparam")}
		if out, e = run(ProbeSELinuxCmd()); e != nil {
			return e
		}
		rep.SELinux = strings.TrimSpace(out) == "selinux"
		return nil
	}); err != nil {
		return rep, err
	}

	if err := do(StepWebroot, func() error {
		_, e := run(MkdirCmd(o.Root))
		return e
	}); err != nil {
		return rep, err
	}
	// Before the first request, so the challenge directory and the webroot are readable by nginx
	// from the start. Only where SELinux is on.
	if rep.SELinux {
		if err := do(StepRestorecon, func() error {
			_, e := run(RestoreconCmd(o.Root))
			return e
		}); err != nil {
			return rep, err
		}
	}

	if err := do(StepHTTPBlock, func() error {
		if state != Absent {
			if _, e := run(BackupCmd(o.Domain)); e != nil {
				return e
			}
		}
		return write(HTTPBlock(o))
	}); err != nil {
		return rep, err
	}
	if err := do(StepHTTPReload, func() error {
		if _, e := run(ReloadCmd()); e != nil {
			return e
		}
		reloaded = true
		return nil
	}); err != nil {
		return rep, err
	}
	if err := do(StepCert, func() error {
		_, e := run(CertbotCmd(o.Domain, o.Email))
		return e
	}); err != nil {
		return rep, err
	}
	if err := do(StepFullBlock, func() error { return write(FullBlock(o, ssl)) }); err != nil {
		return rep, err
	}
	if err := do(StepReload, func() error {
		_, e := run(ReloadCmd())
		return e
	}); err != nil {
		return rep, err
	}

	if state != Absent {
		_, _ = run(DropBackupCmd(o.Domain))
	}
	return rep, nil
}

// PlanStep is one command a dry run lists.
type PlanStep struct {
	Step    string
	Command string
}

// Plan is the command list of a clean first run, in the order Add runs it, for --dry-run. The two
// writes are shown as "write <path>" because their payload is the config printed next to the plan.
// It assumes no existing site, certbot's helper files present, and SELinux on, so the plan shows
// the longest path; the probes decide at run time.
func Plan(o Options) []PlanStep {
	write := func(what string) string { return "write " + ConfPath(o.Domain) + " (" + what + ")" }
	return []PlanStep{
		{StepInspect, ProbeConfCmd(o.Domain)},
		{StepInspect, ProbeSSLCmd()},
		{StepInspect, ProbeSELinuxCmd()},
		{StepWebroot, MkdirCmd(o.Root)},
		{StepRestorecon, RestoreconCmd(o.Root)},
		{StepHTTPBlock, write("http block")},
		{StepHTTPBlock, TestCmd()},
		{StepHTTPReload, ReloadCmd()},
		{StepCert, CertbotCmd(o.Domain, o.Email)},
		{StepFullBlock, write("full block")},
		{StepFullBlock, TestCmd()},
		{StepReload, ReloadCmd()},
	}
}
