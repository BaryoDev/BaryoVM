// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package remote

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/BaryoDev/BaryoVM/internal/compose"
	"github.com/BaryoDev/BaryoVM/internal/update"
)

// fakeExec records which op ran and with which stack config.
type fakeExec struct {
	calls []string
	got   StackConfig
	err   error
}

func (f *fakeExec) Update(sc StackConfig, name string) (update.Result, error) {
	f.calls, f.got = append(f.calls, "update "+name), sc
	return update.Result{}, f.err
}
func (f *fakeExec) Backup(sc StackConfig, name string) (string, error) {
	f.calls, f.got = append(f.calls, "backup "+name), sc
	return "saved", f.err
}
func (f *fakeExec) Status(sc StackConfig) (StatusData, error) {
	f.calls, f.got = append(f.calls, "status"), sc
	return StatusData{Output: "up"}, f.err
}
func (f *fakeExec) Logs(sc StackConfig) (compose.LogsResult, error) {
	f.calls, f.got = append(f.calls, "logs"), sc
	return compose.LogsResult{}, f.err
}

func serve(cmd string, x Executor) Response {
	return Server{Command: cmd, ConfigPath: cfgPath, FS: goodFS(), Exec: x}.Serve()
}

func TestServeDispatchesEachAllowedOp(t *testing.T) {
	want := map[string]string{
		"update": "update playground",
		"backup": "backup playground",
		"status": "status",
		"logs":   "logs",
	}
	for op, call := range want {
		x := &fakeExec{}
		r := serve("baryovm-remote v1 "+op+" playground", x)
		if !r.OK || r.Protocol != Protocol || r.Action != "remote "+op || r.Message != "playground" {
			t.Errorf("%s: reply %+v", op, r)
		}
		if len(x.calls) != 1 || x.calls[0] != call {
			t.Errorf("%s: calls %v, want [%s]", op, x.calls, call)
		}
		if x.got.Dir != "/opt/playground" {
			t.Errorf("%s: the stack config must come from the file, got %+v", op, x.got)
		}
	}
}

func TestServeRefusesWithoutCallingTheExecutor(t *testing.T) {
	cases := map[string]string{
		"op not allowed for the stack": "baryovm-remote v1 update readonly",
		"stack not in the file":        "baryovm-remote v1 status nope",
		"exec":                         "baryovm-remote v1 exec playground",
		"release":                      "baryovm-remote v1 release playground",
		"injection":                    "baryovm-remote v1 status playground;id",
		"extra words":                  "baryovm-remote v1 status playground now",
		"not a request":                "bash -i",
		"no command":                   "",
	}
	for name, cmd := range cases {
		x := &fakeExec{}
		r := serve(cmd, x)
		if r.OK || r.Error == "" || r.Protocol != Protocol {
			t.Errorf("%s: should be a refusal reply, got %+v", name, r)
		}
		if len(x.calls) != 0 {
			t.Errorf("%s: executor ran: %v", name, x.calls)
		}
	}
}

func TestServeRefusesWhenTheAllowlistIsUnsafe(t *testing.T) {
	x := &fakeExec{}
	f := goodFS()
	f.info[cfgPath] = FileInfo{Mode: 0o666, UID: 0}
	r := Server{Command: "baryovm-remote v1 status playground", ConfigPath: cfgPath, FS: f, Exec: x}.Serve()
	if r.OK || len(x.calls) != 0 {
		t.Errorf("a writable allowlist must stop everything: %+v calls=%v", r, x.calls)
	}
}

func TestServeReportsAnOpFailureWithItsData(t *testing.T) {
	x := &fakeExec{err: errors.New("health check failed, rolled back")}
	r := serve("baryovm-remote v1 update playground", x)
	if r.OK || !strings.Contains(r.Error, "rolled back") || len(r.Data) == 0 {
		t.Errorf("a failed update must carry the error and its result: %+v", r)
	}
}

func TestParseResponse(t *testing.T) {
	req := Request{Op: OpStatus, Stack: "playground"}
	good, _ := json.Marshal(Response{Protocol: Protocol, OK: true, Action: "remote status", Message: "playground"})
	if r, err := ParseResponse(string(good), req); err != nil || !r.OK {
		t.Errorf("good reply: %+v %v", r, err)
	}
	refusal, _ := json.Marshal(Response{Protocol: Protocol, OK: false, Action: "remote", Error: "refused"})
	if r, err := ParseResponse(string(refusal), req); err != nil || r.OK {
		t.Errorf("a refusal is a valid reply: %+v %v", r, err)
	}
	other, _ := json.Marshal(Response{Protocol: Protocol, OK: true, Action: "remote status", Message: "other"})
	if _, err := ParseResponse(string(other), req); err == nil {
		t.Error("a reply for another stack must be an error")
	}
	for name, out := range map[string]string{
		"empty":         "",
		"a script":      "deploy finished\n",
		"other json":    `{"ok":true}`,
		"wrong version": `{"protocol":"baryovm-remote/v2","ok":true}`,
	} {
		if _, err := ParseResponse(out, req); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// recRunner records commands and answers "ok".
type recRunner struct {
	cmds []string
}

func (r *recRunner) Run(cmd string) (string, error) {
	r.cmds = append(r.cmds, cmd)
	return "ok", nil
}

func TestRunnerExecutorBuildsCommandsFromTheAllowlistOnly(t *testing.T) {
	c, err := ParseConfig([]byte(goodConfig))
	if err != nil {
		t.Fatal(err)
	}
	sc, _ := c.Stack("playground")
	r := &recRunner{}
	x := RunnerExecutor{R: r}

	if _, err := x.Status(sc); err != nil {
		t.Fatal(err)
	}
	if _, err := x.Logs(sc); err != nil {
		t.Fatal(err)
	}
	if len(r.cmds) == 0 {
		t.Fatal("nothing ran")
	}
	for _, cmd := range r.cmds {
		if !strings.Contains(cmd, "/opt/playground") {
			t.Errorf("command not scoped to the stack dir: %s", cmd)
		}
		if !strings.Contains(cmd, "sudo -n") {
			t.Errorf("sudo stack must use sudo -n: %s", cmd)
		}
	}
	joined := strings.Join(r.cmds, "\n")
	if !strings.Contains(joined, "200") {
		t.Errorf("logs must be bounded to %d lines: %s", LogTail, joined)
	}
}

func TestRunnerExecutorBackupUsesTheConfiguredDatabase(t *testing.T) {
	c, _ := ParseConfig([]byte(goodConfig))
	sc, _ := c.Stack("playground")
	r := &recRunner{}
	_, _ = RunnerExecutor{R: r}.Backup(sc, "playground")
	joined := strings.Join(r.cmds, "\n")
	if !strings.Contains(joined, "pg") || !strings.Contains(joined, "app") {
		t.Errorf("backup did not use dbContainer and dbName from the file: %s", joined)
	}
}

func TestLocalRunnerErrorDoesNotCarryTheScript(t *testing.T) {
	script := "echo oops-from-docker >&2; exit 3 # secret-path-/srv/app"
	_, err := LocalRunner{}.Run(script)
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), "secret-path") || strings.Contains(err.Error(), "exit 3 #") {
		t.Errorf("the error carries the script: %v", err)
	}
	if !strings.Contains(err.Error(), "exit status 3") || !strings.Contains(err.Error(), "oops-from-docker") {
		t.Errorf("the error lost the exit status or stderr: %v", err)
	}
}
