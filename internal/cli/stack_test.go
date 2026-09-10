// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"github.com/BaryoDev/BaryoVM/internal/sshx"
	"github.com/BaryoDev/BaryoVM/internal/ui"
	"github.com/spf13/cobra"
)

// fakeRunner answers commands by substring, so a whole command can run with no VM behind it. An
// unmatched command is an error rather than an empty string: an empty answer is what most of these
// tests are about, so it must never happen by accident. Keep the patterns disjoint.
type fakeRunner struct {
	answers map[string]string
	seen    []string
}

func (f *fakeRunner) Run(cmd string) (string, error) {
	f.seen = append(f.seen, cmd)
	for match, out := range f.answers {
		if strings.Contains(cmd, match) {
			return out, nil
		}
	}
	return "", fmt.Errorf("fakeRunner: no answer for %q", cmd)
}

// stackFixture registers one VM and one stack in a throwaway fleet, so the commands under test
// resolve a stack without reading the developer's own ~/.baryovm.
func stackFixture(t *testing.T) {
	t.Helper()
	t.Setenv("BARYOVM_HOME", t.TempDir())
	store := &fleet.Store{
		VMs:    []fleet.VM{{Name: "vm1", Host: "10.0.0.1", User: "opc", KeyPath: "/keys/id"}},
		Stacks: []fleet.Stack{{Name: "app", VM: "vm1", Dir: "/opt/app", DBContainer: "pg", DBName: "appdb"}},
	}
	if err := store.Save(); err != nil {
		t.Fatalf("seed fleet: %v", err)
	}
}

// runCmd runs one stack command against a fake VM and returns its stdout and stderr.
//
// It goes through the cobra command rather than the describer underneath on purpose. The wiring is
// part of the fix: a describer nothing is wired to leaves the bug exactly where it was, and a test
// that calls it directly still passes.
func runCmd(t *testing.T, cmd *cobra.Command, r sshx.Runner, jsonMode bool, args ...string) (string, string) {
	t.Helper()

	prev := runOnVM
	runOnVM = func(vm *fleet.VM, fn func(c sshx.Runner) (opOutput, error)) (opOutput, error) { return fn(r) }
	t.Cleanup(func() { runOnVM = prev })

	ui.SetJSON(jsonMode)
	t.Cleanup(func() { ui.SetJSON(false) })

	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW

	cmd.SetArgs(args)
	cmd.SetOut(errW)
	cmd.SetErr(errW)
	cmd.SilenceUsage = true
	runErr := cmd.Execute()

	os.Stdout, os.Stderr = oldOut, oldErr
	outW.Close()
	errW.Close()
	stdout, _ := io.ReadAll(outR)
	stderr, _ := io.ReadAll(errR)
	if runErr != nil {
		t.Fatalf("%s: %v\nstderr: %s", cmd.Name(), runErr, stderr)
	}
	return string(stdout), string(stderr)
}

type envelope struct {
	OK     bool            `json:"ok"`
	Action string          `json:"action"`
	Data   json.RawMessage `json:"data"`
}

// parseEnvelope reads the one result a command emits and returns it with its data compacted, so a
// test can compare the data byte for byte as a consumer would receive it.
func parseEnvelope(t *testing.T, stdout string) (envelope, string) {
	t.Helper()
	var e envelope
	if err := json.Unmarshal([]byte(stdout), &e); err != nil {
		t.Fatalf("envelope is not JSON (%v): %s", err, stdout)
	}
	if !e.OK {
		t.Fatalf("expected a successful result: %s", stdout)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, e.Data); err != nil {
		t.Fatalf("compact data: %v", err)
	}
	return e, compact.String()
}

func dataField(t *testing.T, data string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(data), &m); err != nil {
		t.Fatalf("data is not an object (%v): %s", err, data)
	}
	return m
}

// The reported bug: `stack logs` on a container whose app logs to a file inside it answered
// {"output": ""}, the same answer it gives for the wrong host or a Docker it never reached.
func TestStackLogsEnvelopeDescribesAnEmptyRead(t *testing.T) {
	stackFixture(t)
	f := &fakeRunner{answers: map[string]string{" logs": "", " ps -q": "9a1f0c2d3e4f\n"}}

	stdout, _ := runCmd(t, newStackLogsCmd(), f, true, "app")

	e, data := parseEnvelope(t, stdout)
	if e.Action != "stack logs" {
		t.Errorf("action: got %q", e.Action)
	}
	if data == `{"output":""}` {
		t.Fatalf("an empty log still reaches the consumer as a bare empty output: %s", data)
	}
	m := dataField(t, data)
	if m["lines"] != float64(0) {
		t.Errorf("expected lines 0, got %v in %s", m["lines"], data)
	}
	if m["state"] != "silent" {
		t.Errorf("the containers are up and silent, got state %v in %s", m["state"], data)
	}
	if note, _ := m["note"].(string); note == "" {
		t.Errorf("no note saying what an empty read means: %s", data)
	}
}

// Issue #19's rule: an empty success and a different empty success must not serialize the same.
// `compose logs` exits 0 with no output both for a silent container and for a stack that is not
// running, and the two want different things done about them.
func TestStackLogsTellsASilentStackFromOneThatIsNotRunning(t *testing.T) {
	stackFixture(t)

	silent := &fakeRunner{answers: map[string]string{" logs": "", " ps -q": "9a1f0c2d3e4f\n"}}
	stopped := &fakeRunner{answers: map[string]string{" logs": "", " ps -q": ""}}

	silentOut, _ := runCmd(t, newStackLogsCmd(), silent, true, "app")
	stoppedOut, _ := runCmd(t, newStackLogsCmd(), stopped, true, "app")

	_, silentData := parseEnvelope(t, silentOut)
	_, stoppedData := parseEnvelope(t, stoppedOut)
	if silentData == stoppedData {
		t.Fatalf("a silent stack and a stopped one are the same bytes on the wire: %s", silentData)
	}
	if dataField(t, stoppedData)["state"] != "not-running" {
		t.Errorf("expected state not-running, got %s", stoppedData)
	}
	// And the command must have asked: the distinction costs one extra call and only on an empty read.
	if len(stopped.seen) != 2 {
		t.Errorf("expected a logs read and a container check, got %v", stopped.seen)
	}
}

// The same shape has to carry real logs, and must not claim an empty read when there was one.
func TestStackLogsReportTheirLineCount(t *testing.T) {
	stackFixture(t)
	f := &fakeRunner{answers: map[string]string{" logs": "app-1  | started\napp-1  | listening on 8080\n"}}

	stdout, _ := runCmd(t, newStackLogsCmd(), f, true, "app")

	_, data := parseEnvelope(t, stdout)
	m := dataField(t, data)
	if m["lines"] != float64(2) {
		t.Errorf("expected lines 2, got %v in %s", m["lines"], data)
	}
	if m["state"] != "read" {
		t.Errorf("expected state read, got %v in %s", m["state"], data)
	}
	if _, ok := m["note"]; ok {
		t.Errorf("a read with logs in it must not carry the empty-read note: %s", data)
	}
}

// The note is chrome about the absence of data, not data. On stdout it ends up inside the file when
// someone redirects `stack logs app > out.txt`.
func TestTheEmptyReadNoteGoesToStderr(t *testing.T) {
	stackFixture(t)
	f := &fakeRunner{answers: map[string]string{" logs": "", " ps -q": ""}}

	stdout, stderr := runCmd(t, newStackLogsCmd(), f, false, "app")

	if strings.TrimSpace(stdout) != "" {
		t.Errorf("human chrome landed on stdout: %q", stdout)
	}
	if !strings.Contains(stderr, "nothing to log") {
		t.Errorf("the note did not reach stderr: %q", stderr)
	}
}

// The described runner must not have changed the envelope for the ops that say nothing about their
// output. These four are a published shape and nothing here is meant to move them.
func TestUndescribedOpsStillEmitABareOutputMap(t *testing.T) {
	stackFixture(t)
	f := &fakeRunner{answers: map[string]string{" ps": "NAME     STATUS\napp-1    Up 2 days\n"}}

	stdout, _ := runCmd(t, newStackPsCmd(), f, true, "app")

	_, data := parseEnvelope(t, stdout)
	const want = `{"output":"NAME     STATUS\napp-1    Up 2 days\n"}`
	if data != want {
		t.Errorf("stack ps envelope changed:\n got: %s\nwant: %s", data, want)
	}
}

// Issue #19 asked for the other commands with the same shape. `stack backups` answers an empty
// listing with a line of prose, which leaves an agent pattern-matching English to learn there is
// nothing to restore from.
func TestStackBackupsCountsWhatItListed(t *testing.T) {
	stackFixture(t)
	two := &fakeRunner{answers: map[string]string{"ls -1t": "/home/opc/app-backups/db-20260902-010101.dump\n" +
		"/home/opc/app-backups/db-20260901-010101.dump\n"}}
	none := &fakeRunner{answers: map[string]string{"ls -1t": "(no backups yet)\n"}}

	twoOut, _ := runCmd(t, newStackBackupsCmd(), two, true, "app")
	noneOut, _ := runCmd(t, newStackBackupsCmd(), none, true, "app")

	_, twoData := parseEnvelope(t, twoOut)
	_, noneData := parseEnvelope(t, noneOut)
	if m := dataField(t, twoData); m["count"] != float64(2) {
		t.Errorf("expected count 2, got %s", twoData)
	}
	m := dataField(t, noneData)
	if m["count"] != float64(0) {
		t.Errorf("expected count 0, got %s", noneData)
	}
	if note, _ := m["note"].(string); note == "" {
		t.Errorf("an empty listing needs a note: %s", noneData)
	}
	if twoData == noneData {
		t.Fatalf("two backups and none serialize the same: %s", twoData)
	}
}
