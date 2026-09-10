package cli

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BaryoDev/BaryoVM/internal/ui"
)

// emptyEnv points PATH and HOME at empty temp dirs, so every tool lookup and
// every credential-file check misses.
func emptyEnv(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	read := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		read <- string(b)
	}()
	fn()
	os.Stdout = old
	w.Close()
	out := <-read
	r.Close()
	return out
}

func runDoctorJSON(t *testing.T) ui.Result {
	t.Helper()
	ui.SetJSON(true)
	t.Cleanup(func() { ui.SetJSON(false) })
	out := captureStdout(t, func() {
		cmd := newDoctorCmd()
		cmd.SetArgs(nil)
		if err := cmd.Execute(); err != nil {
			t.Errorf("doctor returned an error: %v", err)
		}
	})
	var r ui.Result
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("doctor did not emit a JSON envelope: %v (output %q)", err, out)
	}
	return r
}

func checkNames(t *testing.T, r ui.Result) map[string]bool {
	t.Helper()
	raw, err := json.Marshal(r.Data)
	if err != nil {
		t.Fatal(err)
	}
	var checks []doctorCheck
	if err := json.Unmarshal(raw, &checks); err != nil {
		t.Fatal(err)
	}
	if len(checks) == 0 {
		t.Fatal("doctor reported no checks at all")
	}
	names := map[string]bool{}
	for _, c := range checks {
		names[c.Name] = c.Present
	}
	return names
}

// The tools stack release shells out to are the ones doctor exists to report on.
// Without these a release dies in the sync step, after the pre-release backup.
func TestDoctorChecksTheToolsAReleaseShellsOutTo(t *testing.T) {
	emptyEnv(t)
	names := checkNames(t, runDoctorJSON(t))
	for _, want := range []string{"docker", "rsync", "ssh"} {
		if _, ok := names[want]; !ok {
			t.Errorf("doctor does not check %s; it checked %v", want, keys(names))
		}
	}
}

// doctor.go emitted OK: true unconditionally, so an agent reading -o json was
// told the machine was ready while the human output warned it was not.
func TestDoctorIsNotOKWhenAToolIsMissing(t *testing.T) {
	emptyEnv(t)
	r := runDoctorJSON(t)
	names := checkNames(t, r)
	if names["rsync"] || names["ssh"] || names["docker"] {
		t.Fatalf("the empty PATH leaked a real tool, so this proves nothing: %v", names)
	}
	if r.OK {
		t.Error("every check failed and doctor still reported ok: true")
	}
}

// The other half: ok must still be true when everything is there, or the check
// above would pass on a doctor that is simply always unhappy.
func TestDoctorIsOKWhenEverythingIsPresent(t *testing.T) {
	bin, home := t.TempDir(), t.TempDir()
	for _, tool := range []string{"docker", "rsync", "ssh"} {
		if err := os.WriteFile(filepath.Join(bin, tool), []byte("#!/bin/sh\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct{ dir, file string }{{".aws", "credentials"}, {".oci", "config"}} {
		if err := os.MkdirAll(filepath.Join(home, c.dir), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, c.dir, c.file), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	t.Setenv("HOME", home)

	r := runDoctorJSON(t)
	names := checkNames(t, r)
	for name, present := range names {
		if !present {
			t.Errorf("%s was staged as present and doctor still missed it", name)
		}
	}
	if !r.OK {
		t.Error("everything was present and doctor reported ok: false")
	}
}

// Windows has no rsync, so "missing" on its own sends the user looking for a
// package that does not exist.
func TestDoctorTellsAWindowsUserWhereToGetRsync(t *testing.T) {
	emptyEnv(t)

	hint := hintFor(t, doctorChecks(false, "windows"), "rsync")
	if hint == "" {
		t.Fatal("rsync is missing on windows and doctor offered no advice")
	}
	for _, want := range []string{"WSL", "Git Bash"} {
		if !strings.Contains(hint, want) {
			t.Errorf("the windows rsync hint does not mention %s: %q", want, hint)
		}
	}

	// Linux has rsync in every package manager, so the Windows advice would be
	// noise there.
	if h := hintFor(t, doctorChecks(false, "linux"), "rsync"); h != "" {
		t.Errorf("windows advice leaked onto linux: %q", h)
	}
}

func hintFor(t *testing.T, checks []doctorCheck, name string) string {
	t.Helper()
	for _, c := range checks {
		if c.Name == name {
			if c.Present {
				t.Fatalf("%s was found on an empty PATH, so this proves nothing", name)
			}
			return c.Hint
		}
	}
	t.Fatalf("doctor did not check %s", name)
	return ""
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
