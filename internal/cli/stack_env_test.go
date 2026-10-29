// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/BaryoDev/BaryoVM/internal/fleet"
)

// envRemote answers like a VM: it records every command and what was piped in.
type envRemote struct {
	cmds   []string
	stdins []string
	list   string
}

func (e *envRemote) Run(cmd string) (string, error) {
	e.cmds = append(e.cmds, cmd)
	if strings.Contains(cmd, "print substr") {
		return e.list, nil
	}
	return "", nil
}

func (e *envRemote) RunInput(cmd, stdin string) (string, error) {
	e.cmds = append(e.cmds, cmd)
	e.stdins = append(e.stdins, stdin)
	return "", nil
}

func envFixture(t *testing.T, sudo bool, envFile string) {
	t.Helper()
	t.Setenv("BARYOVM_HOME", t.TempDir())
	store := &fleet.Store{
		VMs:    []fleet.VM{{Name: "vm1", Host: "10.0.0.1", User: "opc", KeyPath: "/keys/id"}},
		Stacks: []fleet.Stack{{Name: "app", VM: "vm1", Dir: "/opt/app", Sudo: sudo, EnvFile: envFile}},
	}
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
}

func TestStackEnvSetReadsStdinAndKeepsTheValueOffTheCommand(t *testing.T) {
	envFixture(t, true, "")
	r := &envRemote{}
	cmd := newStackEnvCmd()
	cmd.SetIn(strings.NewReader("hunter2 = 'x' $y\n"))
	out, _ := runCmd(t, cmd, r, true, "set", "app", "API_KEY")

	if len(r.cmds) != 1 {
		t.Fatalf("calls: %v", r.cmds)
	}
	if strings.Contains(r.cmds[0], "hunter2") {
		t.Errorf("command carries the value: %s", r.cmds[0])
	}
	if !strings.HasPrefix(r.cmds[0], "sudo -n ") || !strings.Contains(r.cmds[0], "/opt/app/.env") {
		t.Errorf("sudo or default path missing: %s", r.cmds[0])
	}
	if r.stdins[0] != "hunter2 = 'x' $y\n" {
		t.Errorf("stdin = %q", r.stdins[0])
	}
	if strings.Contains(out, "hunter2") {
		t.Errorf("json output carries the value: %s", out)
	}
	e, _ := parseEnvelope(t, out)
	if e.Action != "stack env set" {
		t.Errorf("action = %q", e.Action)
	}
}

func TestStackEnvSetUsesTheRegisteredEnvFile(t *testing.T) {
	envFixture(t, false, "deploy/.env.prod")
	r := &envRemote{}
	cmd := newStackEnvCmd()
	cmd.SetIn(strings.NewReader("v\n"))
	runCmd(t, cmd, r, true, "set", "app", "K")
	if !strings.Contains(r.cmds[0], "/opt/app/deploy/.env.prod") {
		t.Errorf("env file not used: %s", r.cmds[0])
	}
	if strings.HasPrefix(r.cmds[0], "sudo") {
		t.Errorf("sudo on a stack without it: %s", r.cmds[0])
	}
}

func TestStackEnvSetRefusesABadKeyBeforeAnyRemoteCall(t *testing.T) {
	envFixture(t, false, "")
	r := &envRemote{}
	cmd := newStackEnvCmd()
	cmd.SetIn(strings.NewReader("v\n"))
	_, _, err := runCmdE(t, cmd, r, true, "set", "app", "bad-key")
	if err == nil {
		t.Fatal("bad key accepted")
	}
	if len(r.cmds) != 0 {
		t.Errorf("remote called: %v", r.cmds)
	}
}

func TestStackEnvSetRecreateRunsComposeUpAfterTheWrite(t *testing.T) {
	envFixture(t, false, "")
	r := &envRemote{}
	cmd := newStackEnvCmd()
	cmd.SetIn(strings.NewReader("v\n"))
	runCmd(t, cmd, r, true, "set", "app", "K", "--recreate")
	if len(r.cmds) != 2 {
		t.Fatalf("calls: %v", r.cmds)
	}
	if !strings.Contains(r.cmds[1], "up -d --force-recreate") {
		t.Errorf("second call is not a recreate: %s", r.cmds[1])
	}
}

func TestStackEnvUnsetAndList(t *testing.T) {
	envFixture(t, false, "")
	r := &envRemote{list: "A\nB\n"}

	out, _ := runCmd(t, newStackEnvCmd(), r, true, "list", "app")
	var env struct {
		Data struct {
			Keys []string `json:"keys"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	if strings.Join(env.Data.Keys, ",") != "A,B" {
		t.Errorf("keys = %v", env.Data.Keys)
	}

	r2 := &envRemote{}
	runCmd(t, newStackEnvCmd(), r2, true, "unset", "app", "A", "--recreate")
	if len(r2.cmds) != 2 || !strings.Contains(r2.cmds[0], "A") || !strings.Contains(r2.cmds[1], "--force-recreate") {
		t.Errorf("unset calls: %v", r2.cmds)
	}
}

func TestStackEnvListHumanOutputMasksValues(t *testing.T) {
	envFixture(t, false, "")
	r := &envRemote{list: "A\n"}
	out, _ := runCmd(t, newStackEnvCmd(), r, false, "list", "app")
	if !strings.Contains(out, "A=********") {
		t.Errorf("masked line missing: %q", out)
	}
}
