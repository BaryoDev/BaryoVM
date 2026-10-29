// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package toolchain

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Run `go test ./internal/toolchain -update` to rewrite the golden files, then read the diff.
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

func line(bin string, args []string) string {
	return strings.Join(append([]string{bin}, args...), " ")
}

func TestRootCmdGolden(t *testing.T) {
	var b strings.Builder
	for _, c := range []struct {
		label string
		euid  int
		name  string
		args  []string
	}{
		{"user apt", 1000, "apt-get", []string{"install", "-y", "rsync"}},
		{"root apt", 0, "apt-get", []string{"install", "-y", "rsync"}},
		{"user script", 1000, "/bin/sh", []string{"/tmp/get-docker.sh"}},
		{"root script", 0, "/bin/sh", []string{"/tmp/get-docker.sh"}},
	} {
		bin, args := rootCmd(c.euid, c.name, c.args)
		b.WriteString(c.label + ": " + line(bin, args) + "\n")
	}
	golden(t, "root_cmd", b.String())
}

// A non-root user must always get `sudo -n`; a bare sudo hangs under -o json.
func TestRootCmdNeverBareSudo(t *testing.T) {
	bin, args := rootCmd(501, "dnf", []string{"install", "-y", "rsync"})
	if bin != "sudo" || len(args) == 0 || args[0] != "-n" {
		t.Fatalf("want sudo -n first, got %s", line(bin, args))
	}
}

func TestRootCmdDoesNotMutateArgs(t *testing.T) {
	in := []string{"install", "-y", "rsync"}
	rootCmd(1000, "dnf", in)
	if strings.Join(in, " ") != "install -y rsync" {
		t.Fatalf("args changed: %v", in)
	}
}

func TestRegistryKnowsTheLocalTools(t *testing.T) {
	for _, name := range []string{"docker", "rsync"} {
		if registry[name] == nil {
			t.Errorf("no installer registered for %s", name)
		}
	}
}

func TestEnsureCLIUnknownToolSaysSo(t *testing.T) {
	_, err := EnsureCLI("baryovm-no-such-tool-xyz")
	if err == nil {
		t.Fatal("want an error for an unknown tool")
	}
	want := "baryovm-no-such-tool-xyz is not installed and BaryoVM has no installer for it"
	if err.Error() != want {
		t.Fatalf("got %q, want %q", err.Error(), want)
	}
}
