// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package status gathers the state of every registered stack into one report, read only.
//
// It owns no new remote command where an existing builder fits: containers come from
// compose.PsJSON, the release and deployed records from internal/releasestate, backups from
// backup.List and the verify assertions from verify.Pass. What it adds is the shape: one SSH
// session per VM, VMs queried concurrently under a bound and a per-VM timeout, and a report in
// which one cell that cannot be read never stops the rest. The package never prints.
package status

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/BaryoDev/BaryoVM/internal/backup"
	"github.com/BaryoDev/BaryoVM/internal/compose"
	"github.com/BaryoDev/BaryoVM/internal/releasestate"
	"github.com/BaryoDev/BaryoVM/internal/remote"
	"github.com/BaryoDev/BaryoVM/internal/sshx"
	"github.com/BaryoDev/BaryoVM/internal/verify"
)

// Session is one open connection to a VM.
type Session interface {
	sshx.Runner
	Close() error
}

// State is the one-word answer for a stack.
const (
	StateUp          = "up"
	StateDegraded    = "degraded"
	StateDown        = "down"
	StateUnknown     = "unknown"
	StateUnreachable = "unreachable"
)

// StackInput is what the caller resolved about one stack before any connection was made.
type StackInput struct {
	Name    string
	Runtime string
	Managed string // the managed database label, "" when none
	Compose compose.Stack
	Loc     releasestate.Loc
	// Backup is nil when the stack declares nothing to back up, which shows as not configured.
	Backup *backup.Config
	// Verify is empty when the stack has no verify spec and no healthUrl.
	Verify verify.Plan
	// Note is something the caller found while resolving the stack, such as an unreadable release
	// manifest. It lands in the stack's notes.
	Note string
}

// Job is one VM and the stacks on it.
type Job struct {
	Name   string
	Host   string
	Forced bool
	Dial   func() (Session, error)
	// Call sends one request to a forced VM. It is used only when Forced is set, and a forced VM is
	// never sent anything else.
	Call   func(r sshx.Runner, req remote.Request) (remote.Response, error)
	Stacks []StackInput
}

// Options bound the work.
type Options struct {
	Workers  int           // VMs queried at once, at least 1
	Timeout  time.Duration // per VM, connection included
	NoVerify bool          // skip the verify assertions
	Now      func() time.Time
}

// VM is one machine's line.
type VM struct {
	Name      string `json:"name"`
	Host      string `json:"host"`
	Reachable bool   `json:"reachable"`
	// Forced marks a VM behind a forced command: only the container list is asked of it, so every
	// other column is limited, not failed.
	Forced    bool   `json:"forced,omitempty"`
	Kernel    string `json:"kernel,omitempty"`
	Error     string `json:"error,omitempty"`
	ElapsedMs int64  `json:"elapsedMs"`
}

// Deployed is the last finished release on the VM.
type Deployed struct {
	Commit string `json:"commit,omitempty"`
	At     string `json:"at,omitempty"`
}

// Release is a release record that is still on the VM: one that is running, or stopped half way.
// A stack whose last release finished has none.
type Release struct {
	State  string `json:"state"` // running | failed | interrupted
	Failed string `json:"failedStep,omitempty"`
	Error  string `json:"error,omitempty"`
	Commit string `json:"commit,omitempty"`
	Since  string `json:"since,omitempty"`
}

// Verify is the verify assertions run now.
type Verify struct {
	Pass   bool   `json:"pass"`
	Steps  int    `json:"steps"`
	Reason string `json:"reason,omitempty"`
}

// Backups is what the backup directory holds.
type Backups struct {
	Count  int    `json:"count"`
	Latest string `json:"latest,omitempty"` // time taken, from the file name
	File   string `json:"file,omitempty"`
}

// Stack is one stack's row. A nil pointer means the column is not configured or not available;
// Notes says which of those it is when the cause was a failure.
type Stack struct {
	Name       string              `json:"name"`
	VM         string              `json:"vm"`
	State      string              `json:"state"`
	Running    int                 `json:"running"`
	Total      int                 `json:"total"`
	Containers []compose.Container `json:"containers,omitempty"`
	Runtime    string              `json:"runtime"`
	Managed    string              `json:"managed,omitempty"`
	Deployed   *Deployed           `json:"deployed"`
	Release    *Release            `json:"release"`
	Verify     *Verify             `json:"verify"`
	Backups    *Backups            `json:"backups"`
	// Limited is set on a forced VM, whose key answers a fixed set of requests.
	Limited   bool     `json:"limited,omitempty"`
	Attention []string `json:"attention"`
	Notes     []string `json:"notes,omitempty"`
}

// Summary counts the report.
type Summary struct {
	VMs         int `json:"vms"`
	Unreachable int `json:"unreachable"`
	Stacks      int `json:"stacks"`
	Attention   int `json:"attention"` // stacks with at least one reason to look
}

// Report is the whole picture, and the one struct both the table and -o json are drawn from.
type Report struct {
	VMs     []VM    `json:"vms"`
	Stacks  []Stack `json:"stacks"`
	Summary Summary `json:"summary"`
}

// Run queries every job and returns the report in the order the jobs were given.
func Run(jobs []Job, o Options) Report {
	if o.Workers < 1 {
		o.Workers = 1
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	type result struct {
		vm     VM
		stacks []Stack
	}
	results := make([]result, len(jobs))
	sem := make(chan struct{}, o.Workers)
	var wg sync.WaitGroup
	for i := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			vm, st := runJob(jobs[i], o)
			results[i] = result{vm, st}
		}(i)
	}
	wg.Wait()

	rep := Report{VMs: []VM{}, Stacks: []Stack{}}
	for _, r := range results {
		rep.VMs = append(rep.VMs, r.vm)
		rep.Stacks = append(rep.Stacks, r.stacks...)
		if !r.vm.Reachable {
			rep.Summary.Unreachable++
		}
	}
	rep.Summary.VMs = len(rep.VMs)
	rep.Summary.Stacks = len(rep.Stacks)
	for _, s := range rep.Stacks {
		if len(s.Attention) > 0 {
			rep.Summary.Attention++
		}
	}
	return rep
}

// runJob bounds one VM by o.Timeout. A VM that does not answer in time is unreachable with the
// reason, and its session is closed so the goroutine behind it is not left holding a connection.
func runJob(j Job, o Options) (VM, []Stack) {
	start := time.Now()
	done := make(chan outcome, 1)
	var mu sync.Mutex
	var sess Session
	timedOut := false
	go func() {
		s, err := j.Dial()
		if err != nil {
			done <- unreachable(j, err.Error(), start)
			return
		}
		mu.Lock()
		if timedOut {
			mu.Unlock()
			_ = s.Close()
			return
		}
		sess = s
		mu.Unlock()
		defer s.Close()
		vm, st := collect(s, j, o, start)
		done <- outcome{vm, st}
	}()
	var timeout <-chan time.Time
	if o.Timeout > 0 {
		t := time.NewTimer(o.Timeout)
		defer t.Stop()
		timeout = t.C
	}
	select {
	case r := <-done:
		return r.vm, r.stacks
	case <-timeout:
		mu.Lock()
		timedOut = true
		if sess != nil {
			_ = sess.Close()
		}
		mu.Unlock()
		r := unreachable(j, fmt.Sprintf("no answer within %s", o.Timeout), start)
		return r.vm, r.stacks
	}
}

type outcome struct {
	vm     VM
	stacks []Stack
}

func unreachable(j Job, reason string, start time.Time) outcome {
	vm := VM{Name: j.Name, Host: j.Host, Forced: j.Forced, Error: reason, ElapsedMs: time.Since(start).Milliseconds()}
	stacks := make([]Stack, 0, len(j.Stacks))
	for _, in := range j.Stacks {
		stacks = append(stacks, Stack{
			Name: in.Name, VM: j.Name, State: StateUnreachable, Runtime: runtimeName(in.Runtime),
			Managed: in.Managed, Limited: j.Forced, Attention: []string{"VM unreachable: " + reason},
		})
	}
	return outcome{vm, stacks}
}

func runtimeName(r string) string {
	if r == "" {
		return "docker"
	}
	return r
}

func collect(r sshx.Runner, j Job, o Options, start time.Time) (VM, []Stack) {
	vm := VM{Name: j.Name, Host: j.Host, Reachable: true, Forced: j.Forced}
	stacks := make([]Stack, 0, len(j.Stacks))
	if !j.Forced {
		// One cheap command that proves the session executes what it is sent.
		if k, err := r.Run("uname -sr"); err == nil {
			vm.Kernel = strings.TrimSpace(k)
		}
	}
	for _, in := range j.Stacks {
		var s Stack
		if j.Forced {
			s = forcedStack(r, j, in)
		} else {
			s = stackStatus(r, j.Name, in, o)
		}
		stacks = append(stacks, s)
	}
	vm.ElapsedMs = time.Since(start).Milliseconds()
	return vm, stacks
}

// forcedStack asks a forced VM for its status op and nothing else.
func forcedStack(r sshx.Runner, j Job, in StackInput) Stack {
	s := Stack{Name: in.Name, VM: j.Name, Runtime: runtimeName(in.Runtime), Managed: in.Managed,
		Limited: true, State: StateUnknown, Attention: []string{}}
	s.Notes = append(s.Notes, "forced-command VM: only the container list is available")
	if j.Call == nil {
		s.Notes = append(s.Notes, "status: no way to call the VM")
		return s
	}
	resp, err := j.Call(r, remote.Request{Op: remote.OpStatus, Stack: in.Name})
	if err != nil {
		s.Notes = append(s.Notes, "status: "+err.Error())
		s.Attention = append(s.Attention, "status request failed: "+err.Error())
		return s
	}
	var d remote.StatusData
	if len(resp.Data) > 0 {
		if err := json.Unmarshal(resp.Data, &d); err != nil {
			s.Notes = append(s.Notes, "status: unreadable reply: "+err.Error())
			return s
		}
	}
	applyContainers(&s, d.Containers)
	return s
}

func stackStatus(r sshx.Runner, vm string, in StackInput, o Options) Stack {
	s := Stack{Name: in.Name, VM: vm, Runtime: runtimeName(in.Runtime), Managed: in.Managed,
		State: StateUnknown, Attention: []string{}}
	if in.Note != "" {
		s.Notes = append(s.Notes, in.Note)
	}

	if list, err := compose.PsJSON(r, in.Compose); err != nil {
		s.Notes = append(s.Notes, "containers: "+err.Error())
	} else {
		applyContainers(&s, list)
	}

	if d, err := releasestate.ReadDeployed(r, in.Loc); err != nil {
		s.Notes = append(s.Notes, "deployed: "+err.Error())
	} else if d != nil {
		s.Deployed = &Deployed{Commit: d.Commit, At: d.At}
	}

	if st, err := releasestate.Read(r, in.Loc); err != nil {
		s.Notes = append(s.Notes, "release: "+err.Error())
	} else if st != nil {
		s.Release = describeRelease(st, o.Now())
		switch s.Release.State {
		case "failed":
			s.Attention = append(s.Attention, "release failed at "+st.Failed+": run `baryovm stack release "+in.Name+" --resume` or `--clear`")
		case "interrupted":
			s.Attention = append(s.Attention, "a release stopped part way and left its record")
		}
	}

	if in.Backup != nil {
		if out, err := backup.List(r, *in.Backup); err != nil {
			s.Notes = append(s.Notes, "backups: "+err.Error())
		} else {
			lr := backup.DescribeList(out)
			b := &Backups{Count: lr.Count}
			if lr.Count > 0 {
				b.File, b.Latest = lr.Entries[0].File, lr.Entries[0].Taken
			}
			s.Backups = b
			if lr.Count == 0 {
				s.Attention = append(s.Attention, "no database backups yet")
			}
		}
	}

	if !o.NoVerify && !in.Verify.Empty() {
		ok, why := verify.Pass(r, in.Verify.Steps)
		s.Verify = &Verify{Pass: ok, Steps: len(in.Verify.Steps), Reason: why}
		if !ok {
			s.Attention = append(s.Attention, "verify failing: "+why)
		}
	}
	return s
}

func describeRelease(st *releasestate.State, now time.Time) *Release {
	rel := &Release{Failed: st.Failed, Commit: st.Commit, Since: st.Started}
	if st.Error != "" {
		rel.Error = releasestate.Redact(st.Error)
	}
	switch {
	case st.Failed != "":
		rel.State = "failed"
	case st.Active(now):
		rel.State = "running"
	default:
		rel.State = "interrupted"
	}
	return rel
}

// applyContainers sets the counts and the one-word state from the container list.
func applyContainers(s *Stack, list []compose.Container) {
	s.Containers = list
	s.Total = len(list)
	unhealthy := 0
	for _, c := range list {
		if c.State == "running" {
			s.Running++
			if c.Health == "unhealthy" {
				unhealthy++
			}
		}
	}
	switch {
	case s.Total == 0:
		s.State = StateDown
		s.Attention = append(s.Attention, "no containers")
	case s.Running == 0:
		s.State = StateDown
		s.Attention = append(s.Attention, "no container is running")
	case s.Running < s.Total || unhealthy > 0:
		s.State = StateDegraded
		if s.Running < s.Total {
			s.Attention = append(s.Attention, fmt.Sprintf("%d of %d containers running", s.Running, s.Total))
		}
		if unhealthy > 0 {
			s.Attention = append(s.Attention, fmt.Sprintf("%d container(s) unhealthy", unhealthy))
		}
	default:
		s.State = StateUp
	}
}
