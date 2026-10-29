// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/BaryoDev/BaryoVM/internal/ui"
	"github.com/spf13/cobra"
)

func runGroup(t *testing.T, args ...string) error {
	t.Helper()
	root := newRoot()
	root.SetArgs(args)
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	_, err := root.ExecuteC()
	return err
}

func TestUnknownSubcommandFailsForEveryGroup(t *testing.T) {
	for _, group := range []string{"vm", "stack"} {
		args := append(strings.Fields(group), "nosuch")
		err := runGroup(t, args...)
		if err == nil {
			t.Fatalf("%s nosuch: want an error, got nil", group)
		}
		want := `unknown command "nosuch" for "baryovm ` + group + `"`
		if err.Error() != want {
			t.Errorf("%s nosuch: got %q, want %q", group, err.Error(), want)
		}
	}
}

func TestGroupWithoutArgsPrintsHelp(t *testing.T) {
	root := newRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"vm"})
	if _, err := root.ExecuteC(); err != nil {
		t.Fatalf("vm with no args: %v", err)
	}
	if !strings.Contains(out.String(), "Manage the VMs in your fleet") {
		t.Errorf("no help printed: %q", out.String())
	}
}

func TestEveryGroupHasTheGuard(t *testing.T) {
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, s := range c.Commands() {
			walk(s)
		}
		if c.HasParent() && c.HasSubCommands() && !c.Runnable() {
			t.Errorf("%s is a group with no guard", c.CommandPath())
		}
	}
	walk(newRoot())
}

func TestUnknownSubcommandGivesTheJSONEnvelope(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old; ui.SetJSON(false); outputFormat = "human" }()

	runErr := runGroup(t, "-o", "json", "vm", "nosuch")
	code := reportError(runErr)
	w.Close()
	os.Stdout = old
	body, _ := io.ReadAll(r)

	if code == 0 {
		t.Fatal("exit code 0")
	}
	var env struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("not JSON: %q: %v", body, err)
	}
	if env.OK || env.Error != `unknown command "nosuch" for "baryovm vm"` {
		t.Errorf("envelope = %+v", env)
	}
}

// Execute ends in os.Exit, so it runs in a child copy of the test binary.
func TestExecuteExitsNonZeroWithTheJSONEnvelope(t *testing.T) {
	if os.Getenv("BARYOVM_TEST_EXECUTE") == "1" {
		os.Args = []string{"baryovm", "-o", "json", "vm", "nosuch"}
		Execute()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestExecuteExitsNonZeroWithTheJSONEnvelope$")
	cmd.Env = append(os.Environ(), "BARYOVM_TEST_EXECUTE=1", "HOME="+t.TempDir())
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() == 0 {
		t.Fatalf("want a non-zero exit, got %v", err)
	}
	var env struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("not JSON: %q: %v", stdout.String(), err)
	}
	if env.OK || env.Error != `unknown command "nosuch" for "baryovm vm"` {
		t.Errorf("envelope = %+v", env)
	}
}
