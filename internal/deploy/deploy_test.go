// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package deploy

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Run `go test ./internal/deploy -update` to rewrite the golden files, then read the diff.
var update = flag.Bool("update", false, "rewrite golden files")

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with -update to create it): %v", err)
	}
	if string(want) != got {
		t.Errorf("%s differs from %s\n--- want\n%s\n--- got\n%s", name, path, want, got)
	}
}

func TestRunCmdGolden(t *testing.T) {
	cases := []struct {
		name string
		spec Spec
	}{
		{"run_minimal", Spec{Name: "web", Image: "example/app:1"}},
		{"run_full", Spec{
			Name:    "web",
			Image:   "example/app:1.2",
			Ports:   []string{"8080:80", "127.0.0.1:9000:9000"},
			Env:     map[string]string{"B": "2", "A": "it's"},
			Restart: "always",
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			golden(t, c.name, runCmd(c.spec)+"\n")
		})
	}
}

// #93: the value of `deploy -e TOKEN=x` must not be in anything sent as a command. This failed on
// the code that wrote `-e 'TOKEN=...'` into the docker run line.
func TestNoEnvValueInAnyRemoteCommand(t *testing.T) {
	const val = "x9-sekret-value"
	spec := Spec{Name: "web", Image: "app:1", Pull: true, Env: map[string]string{"TOKEN": val, "Q": "it's"}}
	for _, c := range append(Commands(spec), runCmd(spec)) {
		if strings.Contains(c, val) || strings.Contains(c, "it's") || strings.Contains(c, "-e ") {
			t.Errorf("a command carries an env value or -e: %s", c)
		}
	}
	golden(t, "run_env_file", runCmd(spec)+"\n")
	if got := EnvInput(spec); got != "Q=it's\nTOKEN="+val+"\n" {
		t.Errorf("stdin env file: %q", got)
	}
}

func TestEnvFileIsOwnerOnlyAndRemoved(t *testing.T) {
	c := runCmd(Spec{Name: "w", Image: "i", Env: map[string]string{"A": "1"}})
	// mktemp creates 0600; the trap removes it on success and failure.
	for _, want := range []string{"mktemp", "trap", "rm -f", "--env-file"} {
		if !strings.Contains(c, want) {
			t.Errorf("missing %q in %s", want, c)
		}
	}
}

func TestNoEnvKeepsThePlainCommand(t *testing.T) {
	if got := runCmd(Spec{Name: "w", Image: "i"}); strings.Contains(got, "env-file") || strings.HasPrefix(got, "sh -c") {
		t.Errorf("no env needs no file: %s", got)
	}
}

func TestValidateRefusesBadEnvWithoutTheValue(t *testing.T) {
	if err := Validate(Spec{Env: map[string]string{"1BAD": "v"}}); err == nil {
		t.Error("bad key")
	}
	err := Validate(Spec{Env: map[string]string{"OK": "line1\nsekret-line2"}})
	if err == nil || strings.Contains(err.Error(), "sekret") {
		t.Errorf("want an error naming the key only: %v", err)
	}
}

func TestRedactRemovesValues(t *testing.T) {
	got := redact("docker: bad TOKEN=abc123 here", map[string]string{"TOKEN": "abc123"})
	if strings.Contains(got, "abc123") {
		t.Errorf("not redacted: %s", got)
	}
}

// Env order must not depend on map iteration, or the command differs run to run.
func TestRunCmdEnvIsSorted(t *testing.T) {
	spec := Spec{Name: "w", Image: "i", Env: map[string]string{"C": "3", "A": "1", "B": "2", "D": "4"}}
	first := runCmd(spec)
	for i := 0; i < 20; i++ {
		if got := runCmd(spec); got != first {
			t.Fatalf("command changed between calls:\n%s\n%s", first, got)
		}
	}
}
