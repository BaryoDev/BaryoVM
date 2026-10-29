// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"github.com/BaryoDev/BaryoVM/internal/sshx"
	"github.com/BaryoDev/BaryoVM/internal/ui"
	"github.com/spf13/cobra"
)

// runJSON drives the whole root command, the way the binary does: pre-scan for -o json, cobra,
// the handler, then reportError. It returns stdout and the exit code. Every other test in this
// package builds one subcommand and skips this path, which is how a second document printed by
// Execute went unseen.
func runJSON(t *testing.T, args ...string) (string, int) {
	t.Helper()
	t.Cleanup(func() { ui.SetJSON(false); outputFormat = "human" })
	var code int
	out := captureStdout(t, func() {
		root := newRoot()
		root.SetOut(io.Discard)
		root.SetErr(io.Discard)
		code = run(root, args)
	})
	return out, code
}

// oneDocument fails unless stdout is exactly one JSON object with a non-empty action, and
// returns it.
func oneDocument(t *testing.T, stdout string) ui.Result {
	t.Helper()
	if n := documents(t, stdout); n != 1 {
		t.Fatalf("stdout holds %d JSON documents, want 1:\n%s", n, stdout)
	}
	var r ui.Result
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("not an envelope (%v): %s", err, stdout)
	}
	if r.Action == "" {
		t.Errorf("envelope has no action: %s", stdout)
	}
	return r
}

// deadFleet registers one VM nothing listens on and one stack on it, in a throwaway home, so every
// remote command fails fast and offline.
func deadFleet(t *testing.T) {
	t.Helper()
	t.Setenv("BARYOVM_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	store := &fleet.Store{
		VMs:    []fleet.VM{{Name: "ghost", Host: "127.0.0.1", Port: 1, User: "u", KeyPath: "/nonexistent/key"}},
		Stacks: []fleet.Stack{{Name: "app", VM: "ghost", Dir: "/opt/app", DBContainer: "pg", DBName: "appdb"}},
	}
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
}

// Failure case 1: the handler emits its own failure and returns the error, and Execute used to
// emit a second document with the action "baryovm" (the old root.go reportError).
func TestFailureIsOneDocument(t *testing.T) {
	deadFleet(t)
	out, code := runJSON(t, "stack", "ps", "app", "-o", "json")
	if code == 0 {
		t.Error("exit code 0 on a failed command")
	}
	r := oneDocument(t, out)
	if r.OK || r.Error == "" || r.Action != "stack ps" {
		t.Errorf("want a failed stack ps envelope, got %+v", r)
	}
}

// Failure case 2: an error raised before the handler's own Emit used to be reported under the
// action "baryovm".
func TestFailureBeforeTheHandlerEmitsKeepsTheAction(t *testing.T) {
	deadFleet(t)
	out, _ := runJSON(t, "stack", "ps", "nope", "-o", "json")
	r := oneDocument(t, out)
	if r.OK || r.Action != "stack ps" {
		t.Errorf("want action %q, got %+v", "stack ps", r)
	}
}

// Failure case 3: a flag parse error used to reach stderr as text, because -o json is only read
// after cobra parses the flags.
func TestFlagParseErrorIsJSON(t *testing.T) {
	for _, form := range [][]string{
		{"vm", "list", "--bogus", "-o", "json"},
		{"-o", "json", "vm", "list", "--bogus"},
		{"vm", "list", "--bogus", "-ojson"},
		{"vm", "list", "--bogus", "--output=json"},
		{"vm", "list", "--bogus", "--output", "json"},
	} {
		out, code := runJSON(t, form...)
		if code == 0 {
			t.Errorf("%v: exit code 0", form)
		}
		r := oneDocument(t, out)
		if r.OK || r.Action != "vm list" || !strings.Contains(r.Error, "unknown flag") {
			t.Errorf("%v: got %+v", form, r)
		}
	}
}

// Failure case 4: -o json used to leave cobra's help block on stdout as bare text.
func TestHelpIsAnEnvelopeUnderJSON(t *testing.T) {
	for _, args := range [][]string{{"stack", "-o", "json"}, {"stack", "ps", "--help", "-o", "json"}, {"vm", "-o", "json"}} {
		out, _ := runJSON(t, args...)
		r := oneDocument(t, out)
		var d struct {
			Usage string `json:"usage"`
		}
		if err := json.Unmarshal(mustMarshal(t, r.Data), &d); err != nil || !strings.Contains(d.Usage, "Usage:") {
			t.Errorf("%v: no usage text in data: %s", args, out)
		}
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A bare -o json inside the remote part of vm exec is the remote command's argument, not ours.
func TestJSONRequestedStopsAtDoubleDash(t *testing.T) {
	if jsonRequested([]string{"vm", "exec", "x", "--", "-o", "json"}) {
		t.Error("read -o json after -- as ours")
	}
	if !jsonRequested([]string{"vm", "exec", "x", "-o", "json", "--", "ls"}) {
		t.Error("missed -o json before --")
	}
	if jsonRequested([]string{"-o", "human"}) || jsonRequested([]string{"-o"}) {
		t.Error("read a non-json format as json")
	}
}

// Every command in the tree, found by walking it, so a command added later is covered without
// touching this file: a bad flag and --help each give one envelope naming the command.
func TestEveryCommandAnswersInOneDocument(t *testing.T) {
	deadFleet(t)
	var walk func(c *cobra.Command)
	n := 0
	walk = func(c *cobra.Command) {
		for _, s := range c.Commands() {
			walk(s)
		}
		if !c.HasParent() || c.Name() == "help" || c.Name() == "completion" {
			return
		}
		path := strings.Fields(actionOf(c.CommandPath()))
		n++
		if c.DisableFlagParsing {
			// gh, git and vm exec hand their flags to another program, so --help and a bad
			// flag are not ours to answer; the table below covers their failures.
			return
		}
		out, _ := runJSON(t, append([]string{"-o", "json"}, append(path, "--help")...)...)
		if r := oneDocument(t, out); r.Action != strings.Join(path, " ") {
			t.Errorf("%v --help: action %q", path, r.Action)
		}
		if !c.Runnable() {
			return
		}
		out, code := runJSON(t, append([]string{"-o", "json"}, append(path, "--no-such-flag")...)...)
		r := oneDocument(t, out)
		if code == 0 || r.OK || r.Action != strings.Join(path, " ") {
			t.Errorf("%v --no-such-flag: code %d, %+v", path, code, r)
		}
	}
	walk(newRoot())
	if n < 40 {
		t.Fatalf("walked only %d commands, the tree walk is broken", n)
	}
}

// The table: commands that fail at the network or at argument checks, each parsed as one document.
// Nothing here reaches a real host: the VM is 127.0.0.1:1.
func TestFailingCommandsGiveOneDocument(t *testing.T) {
	deadFleet(t)
	cases := [][]string{
		{"vm", "ping", "ghost"},
		{"vm", "ping", "nope"},
		{"vm", "bootstrap", "ghost"},
		{"vm", "exec", "ghost", "--", "true"},
		{"vm", "exec", "nope", "--", "true"},
		{"vm", "exec"},
		{"vm", "health", "ghost"},
		{"vm", "threats", "ghost"},
		{"vm", "harden", "ghost"},
		{"vm", "patch", "ghost"},
		{"vm", "prune", "ghost"},
		{"vm", "remove", "nope"},
		{"vm", "forget-key", "nope"},
		{"vm", "runner", "ghost", "--repo", "o/r"},
		{"vm", "runner", "remove", "ghost", "--repo", "o/r"},
		{"stack", "ps", "app"},
		{"stack", "logs", "app"},
		{"stack", "pull", "app"},
		{"stack", "deploy", "app"},
		{"stack", "update", "app"},
		{"stack", "backup", "app"},
		{"stack", "backups", "app"},
		{"stack", "restore", "app", "--yes"},
		{"stack", "rollback", "app"},
		{"stack", "release", "app"},
		{"stack", "env", "list", "app"},
		{"stack", "remove", "nope"},
		{"stack", "tls", "app"},
		{"stack", "nosuch"},
		{"deploy", "ghost", "--image", "nginx"},
		{"up", "ghost"},
		{"scan"},
		{"doctor", "--no-such-flag"},
	}
	for _, c := range cases {
		t.Run(strings.Join(c, " "), func(t *testing.T) {
			out, code := runJSON(t, append([]string{"-o", "json"}, c...)...)
			r := oneDocument(t, out)
			if code != 0 && r.OK {
				t.Errorf("exit %d but the document says ok: %s", code, out)
			}
			if r.Action == "baryovm" && len(c) > 1 {
				t.Errorf("the action lost the command: %s", out)
			}
		})
	}
}

// The commands that work offline give one document on success too.
func TestSucceedingCommandsGiveOneDocument(t *testing.T) {
	deadFleet(t)
	cases := [][]string{
		{"version"},
		{"vm", "list"},
		{"stack", "list"},
		{"vm", "add", "box", "--host", "10.9.9.9", "--user", "u", "--key", "/k", "--os", "linux"},
		{"vm", "remove", "box"},
		{"doctor"},
	}
	for _, c := range cases {
		t.Run(strings.Join(c, " "), func(t *testing.T) {
			out, _ := runJSON(t, append([]string{"-o", "json"}, c...)...)
			oneDocument(t, out)
		})
	}
}

// Remote output that is not valid UTF-8 and carries a stray brace used to be the worry for a
// payload built by hand. It is a string inside the envelope, so the document stays one document
// and the brace stays inside the string.
func TestRemoteOutputCannotBreakTheDocument(t *testing.T) {
	stackFixture(t)
	nasty := "\xff\xfe{ \"ok\": true}\n{\n\x00\x1b[31m"
	f := &fakeRunner{answers: map[string]string{
		" ps":   nasty,
		" logs": nasty,
		" pull": nasty,
	}}
	prev := runOnVM
	runOnVM = func(vm *fleet.VM, fn func(c sshx.Runner) (opOutput, error)) (opOutput, error) { return fn(f) }
	t.Cleanup(func() { runOnVM = prev })

	for _, op := range []string{"ps", "logs", "pull"} {
		out, _ := runJSON(t, "stack", op, "app", "-o", "json")
		r := oneDocument(t, out)
		if !r.OK {
			t.Errorf("stack %s: %s", op, out)
		}
	}

	// And the envelope itself, with the bytes in every string field.
	out := captureStdout(t, func() {
		ui.SetJSON(true)
		defer ui.SetJSON(false)
		ui.Emit(ui.Result{OK: false, Action: "x", Message: nasty, Error: nasty, Data: map[string]string{"stdout": nasty}})
	})
	oneDocument(t, out)
}

// A command that hands its flags to another program owns every word after its name: -o json there
// is the wrapped tool's, not ours. The old prescan read all of os.Args and switched JSON on.
func TestPrescanLeavesFlagsOfAWrappingCommandAlone(t *testing.T) {
	root := newRoot()
	wrap := &cobra.Command{Use: "wrapfake", DisableFlagParsing: true, RunE: func(*cobra.Command, []string) error { return nil }}
	root.AddCommand(wrap)

	cases := []struct {
		args []string
		want bool
	}{
		{[]string{"wrapfake", "log", "-o", "json"}, false},
		{[]string{"wrapfake", "x", "--output", "json"}, false},
		{[]string{"wrapfake", "--output=json"}, false},
		{[]string{"-o", "json", "wrapfake", "log"}, true},
		{[]string{"vm", "exec", "box", "-o", "json", "--", "ls"}, false},
		{[]string{"vm", "list", "-o", "json"}, true},
	}
	for _, c := range cases {
		if got := jsonRequested(ownArgs(root, c.args)); got != c.want {
			t.Errorf("%v: json=%v, want %v", c.args, got, c.want)
		}
	}
}
