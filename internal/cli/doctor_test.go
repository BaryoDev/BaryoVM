package cli

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
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

// runDoctorJSON runs the command the way an agent does and returns both halves
// of what such a caller reads: the envelope and the exit status.
func runDoctorJSON(t *testing.T) (ui.Result, error) {
	t.Helper()
	ui.SetJSON(true)
	t.Cleanup(func() { ui.SetJSON(false) })
	var runErr error
	out := captureStdout(t, func() {
		cmd := newDoctorCmd()
		cmd.SetArgs(nil)
		cmd.SilenceErrors = true
		cmd.SilenceUsage = true
		runErr = cmd.Execute()
	})
	var r ui.Result
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("doctor did not emit a JSON envelope: %v (output %q)", err, out)
	}
	return r, runErr
}

// decodeChecks reads the checks back out of the envelope, so a test sees the
// same JSON an agent would rather than the in-process structs.
func decodeChecks(t *testing.T, r ui.Result) []doctorCheck {
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
	return checks
}

func checkNames(t *testing.T, r ui.Result) map[string]bool {
	t.Helper()
	names := map[string]bool{}
	for _, c := range decodeChecks(t, r) {
		names[c.Name] = c.Present
	}
	return names
}

func checkNamed(t *testing.T, r ui.Result, name string) doctorCheck {
	t.Helper()
	for _, c := range decodeChecks(t, r) {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("doctor did not check %s", name)
	return doctorCheck{}
}

// The tools stack release shells out to are the ones doctor exists to report on.
// Without these a release dies in the sync step, after the pre-release backup.
func TestDoctorChecksTheToolsAReleaseShellsOutTo(t *testing.T) {
	emptyEnv(t)
	r, _ := runDoctorJSON(t)
	names := checkNames(t, r)
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
	r, err := runDoctorJSON(t)
	names := checkNames(t, r)
	if names["rsync"] || names["ssh"] || names["docker"] {
		t.Fatalf("the empty PATH leaked a real tool, so this proves nothing: %v", names)
	}
	if r.OK {
		t.Error("every check failed and doctor still reported ok: true")
	}
	// Every other command in the repo pairs ok:false with a reason and a
	// non-zero exit, which is the only pattern a caller can be expected to read.
	if err == nil {
		t.Error("doctor reported ok: false and still exited 0")
	}
	if !strings.Contains(r.Error, "rsync") {
		t.Errorf("the envelope gives no reason naming the missing tool: %q", r.Error)
	}
}

// The regression the required/optional split exists to stop: the credential
// files are for cloud provisioning, so a machine with every release tool and no
// cloud account is ready. doctor saying otherwise makes its one answer useless.
func TestDoctorIsOKOnAMachineWithNoCloudCredentials(t *testing.T) {
	t.Setenv("PATH", stageTools(t, "docker", "rsync", "ssh"))
	t.Setenv("HOME", t.TempDir())

	r, err := runDoctorJSON(t)
	for _, name := range []string{"aws credentials", "oci config"} {
		c := checkNamed(t, r, name)
		if c.Present {
			t.Fatalf("%s was found under an empty HOME, so this proves nothing", name)
		}
		if !c.Optional {
			t.Errorf("%s counts towards ok, so no machine without a cloud account can pass", name)
		}
	}
	if !r.OK {
		t.Errorf("every release tool is present and doctor still reported ok: false (%s)", r.Error)
	}
	if err != nil {
		t.Errorf("doctor failed on a healthy machine: %v", err)
	}
}

// The hint is only worth having if the JSON surface carries it; the MAUI app and
// the MCP server never see the human lines.
func TestTheJSONEnvelopeCarriesTheHint(t *testing.T) {
	emptyEnv(t)
	r, _ := runDoctorJSON(t)
	c := checkNamed(t, r, "rsync")
	if c.Present {
		t.Fatal("rsync was found on an empty PATH, so this proves nothing")
	}
	if c.Hint == "" {
		t.Fatalf("rsync is missing on %s and the envelope offers no advice", runtime.GOOS)
	}
	if !strings.Contains(c.Hint, "install") {
		t.Errorf("the rsync hint does not say how to install it: %q", c.Hint)
	}
}

// --fix installs rsync, but it will never install ssh, so "missing" on its own
// leaves the user running --fix again expecting a different answer.
func TestFixSaysWhyItCannotInstallSsh(t *testing.T) {
	emptyEnv(t)
	restore := checkedCLIs
	checkedCLIs = []struct {
		name     string
		optional bool
	}{{"ssh", false}}
	t.Cleanup(func() { checkedCLIs = restore })

	checks := doctorChecks(true, "linux")
	if len(checks) == 0 {
		t.Fatal("doctor reported no checks at all")
	}
	ssh := checks[len(checks)-1]
	if ssh.Name != "ssh" || ssh.Present {
		t.Fatalf("expected a missing ssh check, got %+v", ssh)
	}
	if !strings.Contains(ssh.Detail, "no installer") {
		t.Errorf("--fix could not install ssh and did not say why: %q", ssh.Detail)
	}
	if !strings.Contains(ssh.Hint, "openssh") {
		t.Errorf("the ssh hint does not name the package to install: %q", ssh.Hint)
	}
}

// stageTools puts a fake executable for each name in a temp dir and returns it,
// for use as PATH.
func stageTools(t *testing.T, names ...string) string {
	t.Helper()
	bin := t.TempDir()
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return bin
}

// The other half: ok must still be true when everything is there, or the check
// above would pass on a doctor that is simply always unhappy.
func TestDoctorIsOKWhenEverythingIsPresent(t *testing.T) {
	bin, home := stageTools(t, "docker", "rsync", "ssh"), t.TempDir()
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

	r, runErr := runDoctorJSON(t)
	names := checkNames(t, r)
	for name, present := range names {
		if !present {
			t.Errorf("%s was staged as present and doctor still missed it", name)
		}
	}
	if !r.OK {
		t.Error("everything was present and doctor reported ok: false")
	}
	if runErr != nil {
		t.Errorf("everything was present and doctor still failed: %v", runErr)
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

	// Every platform gets an answer, but the platform's own answer: --fix really
	// does install rsync on linux and darwin, and the WSL advice is noise there.
	for _, goos := range []string{"linux", "darwin"} {
		h := hintFor(t, doctorChecks(false, goos), "rsync")
		if h == "" {
			t.Errorf("rsync is missing on %s, where --fix can install it, and doctor offered nothing", goos)
		}
		if strings.Contains(h, "WSL") || strings.Contains(h, "Git Bash") {
			t.Errorf("windows advice leaked onto %s: %q", goos, h)
		}
		if !strings.Contains(h, "--fix") {
			t.Errorf("the %s rsync hint does not mention --fix, which installs it: %q", goos, h)
		}
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
