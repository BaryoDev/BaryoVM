// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package remote

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/backup"
	"github.com/BaryoDev/BaryoVM/internal/compose"
	"github.com/BaryoDev/BaryoVM/internal/sshx"
	"github.com/BaryoDev/BaryoVM/internal/update"
	"github.com/BaryoDev/BaryoVM/internal/verify"
	"time"
)

// LogTail is the fixed number of lines per service `logs` returns. The request grammar carries no
// parameters, so the bound is the VM's, not the client's.
const LogTail = 200

// Response is the reply, one JSON document on stdout. It has the shape of the ui.Result envelope
// plus the protocol marker, so the client can tell this wrapper's reply from whatever else a
// forced command might print.
type Response struct {
	Protocol string          `json:"protocol"`
	OK       bool            `json:"ok"`
	Action   string          `json:"action"` // "remote <op>"
	Message  string          `json:"message,omitempty"`
	Data     json.RawMessage `json:"data,omitempty"`
	Error    string          `json:"error,omitempty"`
}

// ActionFor is the action name of an op's reply.
func ActionFor(op Op) string { return "remote " + string(op) }

// Executor runs the allowed operations for one stack. The real one drives the existing engine
// packages through a Runner; tests replace it to prove dispatch and the allowlist without Docker.
type Executor interface {
	Update(sc StackConfig, name string) (update.Result, error)
	Backup(sc StackConfig, name string) (string, error)
	Status(sc StackConfig) (StatusData, error)
	Logs(sc StackConfig) (compose.LogsResult, error)
}

// StatusData is the reply of `status`, and of `backup` (Output only).
type StatusData struct {
	Output     string              `json:"output"`
	Containers []compose.Container `json:"containers,omitempty"`
}

// Server is one request's worth of wrapper.
type Server struct {
	// Command is SSH_ORIGINAL_COMMAND.
	Command    string
	ConfigPath string
	FS         FS
	Exec       Executor
}

// Serve answers one request. It always returns a Response; OK says whether it was carried out.
// A refusal is a Response with OK false, so the client reads one document either way.
func (s Server) Serve() Response {
	req, err := Parse(s.Command)
	if err != nil {
		return refuse("remote", err)
	}
	cfg, err := LoadConfig(s.FS, s.ConfigPath)
	if err != nil {
		return refuse(ActionFor(req.Op), err)
	}
	sc, ok := cfg.Stack(req.Stack)
	if !ok {
		// Not echoing which stacks exist: the name is a lookup key, and the list is the VM's.
		return refuse(ActionFor(req.Op), fmt.Errorf("refused: stack %q is not in the allowlist", req.Stack))
	}
	if !sc.Allows(req.Op) {
		return refuse(ActionFor(req.Op), fmt.Errorf("refused: stack %q does not allow %s", req.Stack, req.Op))
	}
	return Dispatch(s.Exec, req, sc)
}

func refuse(action string, err error) Response {
	return Response{Protocol: Protocol, OK: false, Action: action, Error: err.Error()}
}

// Dispatch runs an already authorised request. It does not check the allowlist; Serve did.
func Dispatch(x Executor, req Request, sc StackConfig) Response {
	action := ActionFor(req.Op)
	var data any
	var err error
	switch req.Op {
	case OpUpdate:
		var r update.Result
		r, err = x.Update(sc, req.Stack)
		data = r
	case OpBackup:
		var out string
		out, err = x.Backup(sc, req.Stack)
		data = StatusData{Output: out}
	case OpStatus:
		data, err = x.Status(sc)
	case OpLogs:
		data, err = x.Logs(sc)
	default:
		return refuse(action, fmt.Errorf("refused: unknown op %q", req.Op))
	}
	resp := Response{Protocol: Protocol, OK: err == nil, Action: action, Message: req.Stack}
	// The data goes out on failure too: a rolled-back update carries its Result with the error.
	if b, merr := json.Marshal(data); merr == nil {
		resp.Data = b
	}
	if err != nil {
		resp.Error = err.Error()
	}
	return resp
}

// ParseResponse reads a reply on the client, and checks it answers req. A forced command that is
// not this wrapper prints something else or nothing, and that is the failure this protocol exists
// to make loud, so anything that is not a v1 reply to this very request is an error.
func ParseResponse(out string, req Request) (Response, error) {
	var r Response
	dec := json.NewDecoder(strings.NewReader(out))
	if err := dec.Decode(&r); err != nil || r.Protocol != Protocol {
		return Response{}, errors.New("the VM did not answer with a baryovm-remote reply: " +
			"its forced command is not `baryovm remote serve`, or the wrapper is not installed. " +
			"Nothing was done by baryovm")
	}
	if r.OK && (r.Action != ActionFor(req.Op) || r.Message != req.Stack) {
		return Response{}, fmt.Errorf("the VM answered a different request (%s %s) than the one sent (%s %s)", r.Action, r.Message, ActionFor(req.Op), req.Stack)
	}
	return r, nil
}

// LocalRunner runs commands on this machine, through sh. It is the seam that lets the engine
// packages, which build commands for a remote shell, run on the VM itself.
//
// Every command it is given was built by those packages from values in the allowlist file, each
// passed through sshx.Quote. No byte of the client's request is part of any of them.
type LocalRunner struct{}

// Run executes cmd and returns stdout, or an error carrying the exit status and stderr but not cmd.
func (LocalRunner) Run(cmd string) (string, error) {
	c := exec.Command("sh", "-c", cmd) // #nosec G204 -- built by the engine packages from the allowlist, every value quoted
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	if err := c.Run(); err != nil {
		// The script stays out of the error: it travels back to the restricted key in the reply.
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			return out.String(), fmt.Errorf("local command failed: %w", err)
		}
		return out.String(), fmt.Errorf("local command failed: %w: %s", err, msg)
	}
	return out.String(), nil
}

// RunnerExecutor is the real Executor: the engine packages over a Runner. On the VM the Runner is
// LocalRunner.
type RunnerExecutor struct {
	R sshx.Runner
}

func composeStack(sc StackConfig) compose.Stack {
	return compose.Stack{Dir: sc.Dir, File: sc.File, Sudo: sc.Sudo}
}

func backupConfig(sc StackConfig, name string) backup.Config {
	return backup.Config{
		Sudo: sc.Sudo, Name: name, Dir: sc.Dir, EnvFile: sc.EnvFile,
		DBContainer: sc.DBContainer, DBName: sc.DBName, DBUser: sc.DBUser,
		BackupDir: sc.BackupDir, Keep: sc.Keep,
	}
}

// Update is what `stack update <name>` does: pull, back up, recreate, wait for the health URL, and
// put the previous images back if it does not come up. The same update.Run, so the safety rules
// live in one place.
func (e RunnerExecutor) Update(sc StackConfig, name string) (update.Result, error) {
	plan, err := (*verify.Spec)(nil).Plan(verify.Context{
		Mode: verify.ForUpdate, HealthURL: sc.HealthURL, Dir: sc.Dir, Sudo: sc.Sudo,
		DefaultAttempts: 20, DefaultDelay: 3 * time.Second,
	})
	if err != nil {
		return update.Result{}, err
	}
	r := update.SSHRunner{Client: e.R, Stack: composeStack(sc), Verify: plan}
	if sc.HasBackup() {
		cfg := backupConfig(sc, name)
		r.DoBackup = func() (string, error) { return backup.Backup(e.R, cfg) }
	}
	return update.Run(r, update.Options{
		Services:       sc.Services,
		HasHealthCheck: !plan.Empty(),
		HasBackup:      sc.HasBackup(),
		HealthAttempts: plan.Budget.Attempts,
		HealthDelay:    plan.Budget.Delay,
		HealthTimeout:  plan.Budget.Timeout,
	})
}

// Backup takes the stack's database and config backup.
func (e RunnerExecutor) Backup(sc StackConfig, name string) (string, error) {
	return backup.Backup(e.R, backupConfig(sc, name))
}

// Status lists the stack's containers.
func (e RunnerExecutor) Status(sc StackConfig) (StatusData, error) {
	cs := composeStack(sc)
	out, err := compose.Ps(e.R, cs)
	if err != nil {
		return StatusData{}, err
	}
	d := StatusData{Output: out}
	if list, perr := compose.PsJSON(e.R, cs); perr == nil {
		d.Containers = list
	}
	return d, nil
}

// Logs returns a bounded tail of the stack's logs.
func (e RunnerExecutor) Logs(sc StackConfig) (compose.LogsResult, error) {
	return compose.ReadLogs(e.R, composeStack(sc), sc.Services, LogTail)
}
