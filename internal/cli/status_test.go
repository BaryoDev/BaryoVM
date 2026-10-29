// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"github.com/BaryoDev/BaryoVM/internal/remote"
	"github.com/BaryoDev/BaryoVM/internal/sshx"
)

// statusHost answers the read-only commands `status` sends. It is safe to call from the goroutine
// the command runs a VM on, and records every command so a test can prove nothing was written.
type statusHost struct {
	mu      sync.Mutex
	seen    []string
	release string // body of the release record, "" for none
	answers func(cmd string) (string, bool)
}

func (h *statusHost) Run(cmd string) (string, error) {
	h.mu.Lock()
	h.seen = append(h.seen, cmd)
	h.mu.Unlock()
	if h.answers != nil {
		if out, ok := h.answers(cmd); ok {
			return out, nil
		}
	}
	switch {
	case strings.Contains(cmd, "uname -sr"):
		return "Linux 6.1\n", nil
	case strings.Contains(cmd, "ps --format json"):
		return `[{"Name":"app-web-1","Service":"web","State":"running","Image":"web:1"},` +
			`{"Name":"app-db-1","Service":"db","State":"running","Image":"pg:16"}]`, nil
	case strings.Contains(cmd, "deployed.json"):
		return `{"stack":"app","commit":"0123456789abcdef","at":"2026-10-01T00:00:00Z"}`, nil
	case strings.Contains(cmd, ".baryovm-release-"):
		return h.release, nil
	case strings.Contains(cmd, "ls -1t"):
		return "/bk/db-20261001-020000.dump\n/bk/db-20260930-020000.dump\n", nil
	case strings.Contains(cmd, "-w '%{http_code}'"):
		return "200", nil
	}
	return "", errors.New("statusHost: no answer for " + cmd)
}

// writes reports any command that could change the host.
func (h *statusHost) writes() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var bad []string
	for _, c := range h.seen {
		for _, w := range []string{"mkdir", " mv ", "rm -", "rmdir", "base64 -d", "pull", " up ", "restart", "pg_dump"} {
			if strings.Contains(c, w) {
				bad = append(bad, c)
				break
			}
		}
	}
	return bad
}

func statusFleet(t *testing.T, vms []fleet.VM, stacks []fleet.Stack) {
	t.Helper()
	t.Setenv("BARYOVM_HOME", t.TempDir())
	if err := (&fleet.Store{VMs: vms, Stacks: stacks}).Save(); err != nil {
		t.Fatal(err)
	}
}

func vmNamed(name string) fleet.VM {
	return fleet.VM{Name: name, Host: "10.0.0.9", User: "u", KeyPath: "/k"}
}

func stubStatusDial(t *testing.T, byName map[string]func() (remoteSession, error)) {
	t.Helper()
	prev := dialRelease
	dialRelease = func(vm *fleet.VM) (remoteSession, error) {
		f, ok := byName[vm.Name]
		if !ok {
			return nil, errors.New("dial " + vm.Name + ": connection refused")
		}
		return f()
	}
	t.Cleanup(func() { dialRelease = prev })
}

type statusData struct {
	VMs []struct {
		Name      string `json:"name"`
		Reachable bool   `json:"reachable"`
		Forced    bool   `json:"forced"`
		Error     string `json:"error"`
	} `json:"vms"`
	Stacks []struct {
		Name     string `json:"name"`
		VM       string `json:"vm"`
		State    string `json:"state"`
		Running  int    `json:"running"`
		Total    int    `json:"total"`
		Limited  bool   `json:"limited"`
		Deployed *struct {
			Commit string `json:"commit"`
		} `json:"deployed"`
		Release *struct {
			State  string `json:"state"`
			Failed string `json:"failedStep"`
		} `json:"release"`
		Verify *struct {
			Pass bool `json:"pass"`
		} `json:"verify"`
		Backups *struct {
			Count  int    `json:"count"`
			Latest string `json:"latest"`
		} `json:"backups"`
		Attention []string `json:"attention"`
		Notes     []string `json:"notes"`
	} `json:"stacks"`
	Summary struct {
		VMs         int `json:"vms"`
		Unreachable int `json:"unreachable"`
		Stacks      int `json:"stacks"`
		Attention   int `json:"attention"`
	} `json:"summary"`
}

func runStatus(t *testing.T, args ...string) (statusData, string, string) {
	t.Helper()
	stdout, stderr, err := runCmdE(t, newStatusCmd(), nil, true, args...)
	if err != nil {
		t.Fatalf("status: %v\n%s", err, stdout)
	}
	env, data := healthEnvelope(t, stdout)
	if !env.OK || env.Action != "status" {
		t.Fatalf("envelope: %+v", env)
	}
	var d statusData
	if err := json.Unmarshal([]byte(data), &d); err != nil {
		t.Fatal(err)
	}
	return d, data, stderr
}

func fullStack() fleet.Stack {
	return fleet.Stack{Name: "app", VM: "vm1", Dir: "/opt/app", DBContainer: "pg", DBName: "appdb",
		HealthURL: "http://127.0.0.1:8080/health"}
}

func TestStatusHealthyVMIsOneConnectionAndReadOnly(t *testing.T) {
	statusFleet(t, []fleet.VM{vmNamed("vm1")}, []fleet.Stack{fullStack()})
	host := &statusHost{}
	var dials int32
	stubStatusDial(t, map[string]func() (remoteSession, error){"vm1": func() (remoteSession, error) {
		atomic.AddInt32(&dials, 1)
		return closableRunner{host}, nil
	}})

	d, _, _ := runStatus(t)
	if n := atomic.LoadInt32(&dials); n != 1 {
		t.Fatalf("dialed %d times, want 1", n)
	}
	if len(d.Stacks) != 1 || len(d.VMs) != 1 || !d.VMs[0].Reachable {
		t.Fatalf("%+v", d)
	}
	s := d.Stacks[0]
	if s.State != "up" || s.Running != 2 || s.Total != 2 {
		t.Fatalf("containers: %+v", s)
	}
	if s.Deployed == nil || s.Deployed.Commit != "0123456789abcdef" {
		t.Fatalf("deployed: %+v", s.Deployed)
	}
	if s.Release != nil {
		t.Fatalf("a finished release leaves no record, got %+v", s.Release)
	}
	if s.Verify == nil || !s.Verify.Pass {
		t.Fatalf("verify: %+v", s.Verify)
	}
	if s.Backups == nil || s.Backups.Count != 2 || s.Backups.Latest != "2026-10-01T02:00:00" {
		t.Fatalf("backups: %+v", s.Backups)
	}
	if len(s.Attention) != 0 || d.Summary.Attention != 0 {
		t.Fatalf("a healthy stack needs no look: %v", s.Attention)
	}
	if w := host.writes(); len(w) != 0 {
		t.Fatalf("status sent a command that can change the host: %v", w)
	}
}

func TestStatusUnreachableVMStillReportsTheOthers(t *testing.T) {
	other := fullStack()
	other.Name, other.VM = "site", "vm2"
	statusFleet(t, []fleet.VM{vmNamed("vm1"), vmNamed("vm2")}, []fleet.Stack{fullStack(), other})
	stubStatusDial(t, map[string]func() (remoteSession, error){"vm1": func() (remoteSession, error) {
		return closableRunner{&statusHost{}}, nil
	}}) // vm2 is not in the map: connection refused

	d, _, _ := runStatus(t)
	if len(d.VMs) != 2 || !d.VMs[0].Reachable || d.VMs[1].Reachable {
		t.Fatalf("%+v", d.VMs)
	}
	if !strings.Contains(d.VMs[1].Error, "connection refused") {
		t.Fatalf("the reason is missing: %q", d.VMs[1].Error)
	}
	if d.Stacks[0].State != "up" || d.Stacks[1].State != "unreachable" {
		t.Fatalf("%+v", d.Stacks)
	}
	if d.Summary.Unreachable != 1 || d.Summary.Attention != 1 {
		t.Fatalf("%+v", d.Summary)
	}
}

func TestStatusVMThatNeverAnswersTimesOutAndDoesNotStallTheRest(t *testing.T) {
	other := fullStack()
	other.Name, other.VM = "site", "vm2"
	statusFleet(t, []fleet.VM{vmNamed("vm1"), vmNamed("vm2")}, []fleet.Stack{fullStack(), other})
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	stubStatusDial(t, map[string]func() (remoteSession, error){
		"vm1": func() (remoteSession, error) { <-release; return nil, errors.New("late") },
		"vm2": func() (remoteSession, error) { return closableRunner{&statusHost{}}, nil },
	})

	d, _, _ := runStatus(t, "--timeout", "100ms")
	if d.VMs[0].Reachable || !strings.Contains(d.VMs[0].Error, "no answer within") {
		t.Fatalf("vm1: %+v", d.VMs[0])
	}
	if !d.VMs[1].Reachable || d.Stacks[1].State != "up" {
		t.Fatalf("vm2 must be unaffected: %+v %+v", d.VMs[1], d.Stacks[1])
	}
}

func TestStatusForcedVMGetsOnlyItsStatusRequest(t *testing.T) {
	st := fullStack()
	statusFleet(t, []fleet.VM{{Name: "vm1", Host: "10.0.0.9", User: "u", KeyPath: "/k", Remote: fleet.RemoteForced}}, []fleet.Stack{st})
	var mu sync.Mutex
	var sent []string
	reply := remote.Response{Protocol: remote.Protocol, OK: true, Action: "remote status", Message: "app",
		Data: json.RawMessage(`{"output":"","containers":[{"name":"app-web-1","service":"web","state":"running","image":"web:1"}]}`)}
	body, _ := json.Marshal(reply)
	var dials int32
	prev := dialForcedSession
	dialForcedSession = func(vm *fleet.VM) (remoteSession, error) {
		atomic.AddInt32(&dials, 1)
		return closableRunner{runnerFunc(func(cmd string) (string, error) {
			mu.Lock()
			sent = append(sent, cmd)
			mu.Unlock()
			return string(body), nil
		})}, nil
	}
	t.Cleanup(func() { dialForcedSession = prev })
	stubStatusDial(t, nil) // an ordinary dial of this VM would be a bug

	d, _, _ := runStatus(t)
	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 1 || sent[0] != "baryovm-remote v1 status app" {
		t.Fatalf("a forced VM must see only its request, got %q", sent)
	}
	if atomic.LoadInt32(&dials) != 1 {
		t.Fatalf("dials: %d", dials)
	}
	s := d.Stacks[0]
	if !s.Limited || s.State != "up" || s.Total != 1 || !d.VMs[0].Forced {
		t.Fatalf("%+v", s)
	}
	if s.Deployed != nil || s.Release != nil || s.Verify != nil || s.Backups != nil {
		t.Fatalf("a forced VM cannot answer the other columns: %+v", s)
	}
}

func TestStatusForcedVMThatRefusesIsReportedNotFatal(t *testing.T) {
	statusFleet(t, []fleet.VM{{Name: "vm1", Host: "10.0.0.9", User: "u", KeyPath: "/k", Remote: fleet.RemoteForced}}, []fleet.Stack{fullStack()})
	prev := dialForcedSession
	dialForcedSession = func(*fleet.VM) (remoteSession, error) {
		return closableRunner{runnerFunc(func(string) (string, error) { return "not a reply", nil })}, nil
	}
	t.Cleanup(func() { dialForcedSession = prev })

	d, _, _ := runStatus(t)
	if d.Stacks[0].State != "unknown" || len(d.Stacks[0].Attention) == 0 {
		t.Fatalf("%+v", d.Stacks[0])
	}
}

type runnerFunc func(string) (string, error)

func (f runnerFunc) Run(c string) (string, error) { return f(c) }

func TestStatusStackWithNothingConfiguredIsDashesNotAnError(t *testing.T) {
	statusFleet(t, []fleet.VM{vmNamed("vm1")}, []fleet.Stack{{Name: "app", VM: "vm1", Dir: "/opt/app", NoDatabase: true}})
	host := &statusHost{answers: func(cmd string) (string, bool) {
		if strings.Contains(cmd, "deployed.json") {
			return "", true
		}
		return "", false
	}}
	stubStatusDial(t, map[string]func() (remoteSession, error){"vm1": func() (remoteSession, error) { return closableRunner{host}, nil }})

	d, _, stderr := runStatus(t)
	s := d.Stacks[0]
	if s.State != "up" || s.Deployed != nil || s.Verify != nil || s.Backups != nil || s.Release != nil {
		t.Fatalf("%+v", s)
	}
	if len(s.Notes) != 0 || len(s.Attention) != 0 {
		t.Fatalf("not configured is not a failure: notes %v attention %v", s.Notes, s.Attention)
	}
	if stderr != "" {
		t.Fatalf("JSON mode must keep the human chrome silent, got %q", stderr)
	}
	for _, c := range host.seen {
		if strings.Contains(c, "ls -1t") || strings.Contains(c, "curl") {
			t.Fatalf("asked for something that is not configured: %s", c)
		}
	}
}

func TestStatusFailedReleaseAndFailingVerifyNeedALook(t *testing.T) {
	statusFleet(t, []fleet.VM{vmNamed("vm1")}, []fleet.Stack{fullStack()})
	host := &statusHost{
		release: `{"stack":"app","started":"2026-10-02T00:00:00Z","steps":[{"name":"build","status":"failed","error":"boom"}],"failed":"build","error":"boom"}`,
		answers: func(cmd string) (string, bool) {
			if strings.Contains(cmd, "-w '%{http_code}'") {
				return "502", true
			}
			return "", false
		},
	}
	stubStatusDial(t, map[string]func() (remoteSession, error){"vm1": func() (remoteSession, error) { return closableRunner{host}, nil }})

	d, _, _ := runStatus(t)
	s := d.Stacks[0]
	if s.Release == nil || s.Release.State != "failed" || s.Release.Failed != "build" {
		t.Fatalf("release: %+v", s.Release)
	}
	if s.Verify == nil || s.Verify.Pass {
		t.Fatalf("verify: %+v", s.Verify)
	}
	if len(s.Attention) < 2 || d.Summary.Attention != 1 {
		t.Fatalf("attention: %v %+v", s.Attention, d.Summary)
	}

	// --no-verify leaves the column unasked.
	d, _, _ = runStatus(t, "--no-verify")
	if d.Stacks[0].Verify != nil {
		t.Fatalf("verify ran despite --no-verify: %+v", d.Stacks[0].Verify)
	}
}

func TestStatusAColumnThatCannotBeReadIsANoteNotAFailure(t *testing.T) {
	statusFleet(t, []fleet.VM{vmNamed("vm1")}, []fleet.Stack{fullStack()})
	host := &statusHost{answers: func(cmd string) (string, bool) {
		if strings.Contains(cmd, "ls -1t") {
			return "", false // no answer: the fake errors
		}
		return "", false
	}}
	// The backup listing errors; every other column must still render.
	broken := runnerFunc(func(cmd string) (string, error) {
		if strings.Contains(cmd, "ls -1t") {
			return "", errors.New("cannot read /bk")
		}
		return host.Run(cmd)
	})
	stubStatusDial(t, map[string]func() (remoteSession, error){"vm1": func() (remoteSession, error) { return closableRunner{broken}, nil }})

	d, _, _ := runStatus(t)
	s := d.Stacks[0]
	if s.Backups != nil || s.State != "up" || s.Deployed == nil || s.Verify == nil {
		t.Fatalf("%+v", s)
	}
	if len(s.Notes) != 1 || !strings.HasPrefix(s.Notes[0], "backups:") {
		t.Fatalf("notes: %v", s.Notes)
	}
}

func TestStatusJSONAndTableComeFromTheSameReport(t *testing.T) {
	statusFleet(t, []fleet.VM{vmNamed("vm1")}, []fleet.Stack{fullStack()})
	stubStatusDial(t, map[string]func() (remoteSession, error){"vm1": func() (remoteSession, error) { return closableRunner{&statusHost{}}, nil }})

	_, raw, _ := runStatus(t)
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &top); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"vms", "stacks", "summary"} {
		if _, ok := top[k]; !ok {
			t.Errorf("data has no %q", k)
		}
	}
	var stacks []map[string]json.RawMessage
	if err := json.Unmarshal(top["stacks"], &stacks); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"name", "vm", "state", "running", "total", "runtime", "deployed", "release", "verify", "backups", "attention"} {
		if _, ok := stacks[0][k]; !ok {
			t.Errorf("stack has no stable key %q", k)
		}
	}

	// The human form is drawn from the same struct, on stderr, with stdout left empty.
	stdout, stderr, err := runCmdE(t, newStatusCmd(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "" {
		t.Fatalf("human output belongs on stderr, stdout has %q", stdout)
	}
	for _, want := range []string{"VM", "STACK", "STATE", "DEPLOYED", "vm1", "app", "up", "2/2", "0123456", "2, 2026-10-01"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("table lacks %q:\n%s", want, stderr)
		}
	}
}

func TestStatusScopeAndUnknownNames(t *testing.T) {
	other := fullStack()
	other.Name, other.VM = "site", "vm2"
	statusFleet(t, []fleet.VM{vmNamed("vm1"), vmNamed("vm2")}, []fleet.Stack{fullStack(), other})
	stubStatusDial(t, map[string]func() (remoteSession, error){
		"vm1": func() (remoteSession, error) { return closableRunner{&statusHost{}}, nil },
		"vm2": func() (remoteSession, error) { return closableRunner{&statusHost{}}, nil },
	})

	d, _, _ := runStatus(t, "--stack", "site")
	if len(d.Stacks) != 1 || d.Stacks[0].Name != "site" || len(d.VMs) != 1 {
		t.Fatalf("%+v", d)
	}
	for _, args := range [][]string{{"--vm", "nope"}, {"--stack", "nope"}, {"--vm", "vm1", "--stack", "site"}} {
		stdout, _, err := runCmdE(t, newStatusCmd(), nil, true, args...)
		if err == nil {
			t.Errorf("%v should be refused", args)
		}
		if env, _ := healthEnvelope(t, stdout); env.OK || env.Action != "status" {
			t.Errorf("%v: refusal must still be an envelope: %s", args, stdout)
		}
	}
}

func TestStatusReleaseRecordNextToTheManifestRoot(t *testing.T) {
	dir := t.TempDir()
	manifest := dir + "/release.json"
	if err := os.WriteFile(manifest, []byte(`{"localRoot":"`+dir+`","remoteRoot":"/srv/www/shop"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	st := fullStack()
	st.ReleaseFile = manifest
	statusFleet(t, []fleet.VM{vmNamed("vm1")}, []fleet.Stack{st})
	host := &statusHost{}
	stubStatusDial(t, map[string]func() (remoteSession, error){"vm1": func() (remoteSession, error) { return closableRunner{host}, nil }})

	runStatus(t)
	found := false
	for _, c := range host.seen {
		if strings.Contains(c, sshx.Quote("/srv/www/.baryovm-release-app.deployed.json")) {
			found = true
		}
	}
	if !found {
		t.Fatalf("the deployed record should be read beside the manifest's remoteRoot, saw %q", host.seen)
	}
}
