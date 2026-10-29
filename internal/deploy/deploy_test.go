// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package deploy

import (
	"flag"
	"os"
	"path/filepath"
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
