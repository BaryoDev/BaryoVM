// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func newHostKey(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	k, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k)))
}

// projectDir writes a project file for three stacks on one VM, plus their manifests, into a fresh
// directory and returns the directory and the pinned host key. BARYOVM_HOME is empty: there is no
// fleet.json, which is what a CI runner looks like.
func projectDir(t *testing.T, mutate func(p map[string]any)) (string, string) {
	t.Helper()
	t.Setenv("BARYOVM_HOME", t.TempDir())
	t.Setenv("BARYOVM_DEPLOY_KEY_PATH", "/keys/ci-id")
	t.Setenv("BARYOVM_STRICT_HOST_KEYS", "")
	dir := t.TempDir()
	hostKey := newHostKey(t)

	var stacks []any
	var order []any
	for _, n := range []string{"api", "console", "site"} {
		src := filepath.Join(dir, "src-"+n)
		if err := os.MkdirAll(filepath.Join(src, "web"), 0o755); err != nil {
			t.Fatal(err)
		}
		m, _ := json.Marshal(map[string]any{
			"localRoot": src, "remoteRoot": "/srv/" + n, "sync": []string{"web"},
			"noCompose": true, "verify": []string{"verify-" + n}, "verifyAttempts": 1,
		})
		if err := os.WriteFile(filepath.Join(dir, n+".release.json"), m, 0o600); err != nil {
			t.Fatal(err)
		}
		stacks = append(stacks, map[string]any{"name": n, "vm": "client-vm", "dir": "/opt/" + n, "releaseFile": n + ".release.json"})
		order = append(order, n)
	}
	p := map[string]any{
		"project": "demo",
		"vms": []any{map[string]any{"name": "client-vm", "host": "203.0.113.10", "user": "deploy",
			"keyPath": "env:BARYOVM_DEPLOY_KEY_PATH", "hostKey": hostKey}},
		"stacks": stacks,
		"order":  order,
	}
	if mutate != nil {
		mutate(p)
	}
	writeProject(t, dir, p)
	return dir, hostKey
}

func writeProject(t *testing.T, dir string, p map[string]any) string {
	t.Helper()
	b, _ := json.MarshalIndent(p, "", "  ")
	path := filepath.Join(dir, fleet.DefaultProjectFile)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// projectRun runs `project release` through cobra against a fake VM. dialed collects the VMs the
// release connected to, as the engine saw them.
func projectRun(t *testing.T, f *fakeRunner, args ...string) (stdout string, dialed []fleet.VM, err error) {
	t.Helper()
	withLookPath(t, hasRsync)
	prevDial, prevLocal := dialRelease, runLocal
	dialRelease = func(vm *fleet.VM) (remoteSession, error) {
		dialed = append(dialed, *vm)
		return fakeSession{f}, nil
	}
	runLocal = func(c *exec.Cmd) (string, error) {
		f.seen = append(f.seen, "local: "+strings.Join(c.Args, " "))
		return "", nil
	}
	t.Cleanup(func() { dialRelease, runLocal = prevDial, prevLocal })
	stdout, _, err = runCmdE(t, newProjectReleaseCmd(), f, true, args...)
	return stdout, dialed, err
}

type projectReport struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
	Data  struct {
		Project string `json:"project"`
		Stacks  []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
			Error  string `json:"error"`
		} `json:"stacks"`
	} `json:"data"`
}

func parseReport(t *testing.T, stdout string) projectReport {
	t.Helper()
	var r projectReport
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("not one JSON document (%v): %q", err, stdout)
	}
	return r
}

func verifyAnswers(names ...string) map[string]string {
	a := map[string]string{}
	for _, n := range names {
		a["verify-"+n] = ""
	}
	return a
}

func TestProjectReleaseNeedsNoVMAddAndReleasesInOrder(t *testing.T) {
	dir, hostKey := projectDir(t, nil)
	f := &fakeRunner{answers: verifyAnswers("api", "console", "site")}

	stdout, dialed, err := projectRun(t, f, "--file", filepath.Join(dir, fleet.DefaultProjectFile))
	if err != nil {
		t.Fatalf("release: %v\n%s", err, strings.Join(f.seen, "\n"))
	}
	r := parseReport(t, stdout)
	if !r.OK || len(r.Data.Stacks) != 3 {
		t.Fatalf("want three stacks released: %+v", r)
	}
	for i, n := range []string{"api", "console", "site"} {
		if r.Data.Stacks[i].Name != n || r.Data.Stacks[i].Status != "released" {
			t.Errorf("stack %d = %+v, want %s released", i, r.Data.Stacks[i], n)
		}
	}
	api, console, site := indexOfCmd(f.seen, "/srv/api"), indexOfCmd(f.seen, "/srv/console"), indexOfCmd(f.seen, "/srv/site")
	if api < 0 || api >= console || console >= site {
		t.Errorf("stacks must run in order api, console, site; got positions %d %d %d:\n%s", api, console, site, strings.Join(f.seen, "\n"))
	}
	if len(dialed) == 0 || dialed[0].Host != "203.0.113.10" || dialed[0].KeyPath != "/keys/ci-id" || dialed[0].User != "deploy" {
		t.Errorf("the VM should come from the project file and the key from env:BARYOVM_DEPLOY_KEY_PATH, got %+v", dialed)
	}
	if got := fleetFileExists(); got {
		t.Error("a project release must not write fleet.json")
	}
	if kh, err := os.ReadFile(filepath.Join(os.Getenv("BARYOVM_HOME"), "known_hosts")); err == nil {
		t.Errorf("a project pin is for the run and must not be written to known_hosts, it has:\n%s", kh)
	}
	_ = hostKey
}

func fleetFileExists() bool {
	_, err := os.Stat(filepath.Join(os.Getenv("BARYOVM_HOME"), "fleet.json"))
	return err == nil
}

func TestProjectReleaseStopsAtTheFirstFailedVerify(t *testing.T) {
	dir, _ := projectDir(t, nil)
	// No answer for verify-console: the fake fails that check.
	f := &fakeRunner{answers: verifyAnswers("api", "site")}

	stdout, _, err := projectRun(t, f, "--file", filepath.Join(dir, fleet.DefaultProjectFile))
	if err == nil {
		t.Fatal("a failed verify must fail the project release")
	}
	r := parseReport(t, stdout)
	if r.OK {
		t.Errorf("the report must say it failed: %+v", r)
	}
	got := []string{}
	for _, s := range r.Data.Stacks {
		got = append(got, s.Name+"="+s.Status)
	}
	if strings.Join(got, ",") != "api=released,console=failed,site=skipped" {
		t.Errorf("partial release should be visible as partial, got %v", got)
	}
	if r.Data.Stacks[1].Error == "" {
		t.Error("the failed stack should carry its error")
	}
	if indexOfCmd(f.seen, "/srv/site") >= 0 {
		t.Errorf("a later stack was touched after the failure:\n%s", strings.Join(f.seen, "\n"))
	}
}

func TestProjectReleaseHonoursTheOrderField(t *testing.T) {
	dir, _ := projectDir(t, func(p map[string]any) { p["order"] = []any{"site", "api"} })
	f := &fakeRunner{answers: verifyAnswers("api", "site")}

	stdout, _, err := projectRun(t, f, "--file", filepath.Join(dir, fleet.DefaultProjectFile))
	if err != nil {
		t.Fatal(err)
	}
	r := parseReport(t, stdout)
	if len(r.Data.Stacks) != 2 || r.Data.Stacks[0].Name != "site" || r.Data.Stacks[1].Name != "api" {
		t.Errorf("order should be site, api and leave console alone: %+v", r.Data.Stacks)
	}
	if indexOfCmd(f.seen, "/srv/console") >= 0 {
		t.Error("console is not in order, so it must not be released")
	}
}

// Risk: a host key is missing in CI and the release trusts first use.
func TestProjectReleaseRequiresAPinnedHostKeyOrStrictHostKeys(t *testing.T) {
	dir, _ := projectDir(t, func(p map[string]any) {
		delete(p["vms"].([]any)[0].(map[string]any), "hostKey")
	})
	file := filepath.Join(dir, fleet.DefaultProjectFile)

	f := &fakeRunner{answers: verifyAnswers("api", "console", "site")}
	stdout, dialed, err := projectRun(t, f, "--file", file)
	if err == nil || !strings.Contains(err.Error(), "vms[0].hostKey") || !strings.Contains(err.Error(), "first use") {
		t.Fatalf("want a refusal naming vms[0].hostKey, got %v", err)
	}
	if len(dialed) != 0 || len(f.seen) != 0 {
		t.Errorf("nothing may connect before the refusal: dialed %d, ran %v", len(dialed), f.seen)
	}
	if r := parseReport(t, stdout); r.OK {
		t.Error("the refusal must be reported as a failure")
	}

	// --strict-host-keys is the same switch as BARYOVM_STRICT_HOST_KEYS=1 (sshx.StrictHostKeys): the
	// unknown host is then refused at connect, so a missing pin cannot become a learned one.
	t.Setenv("BARYOVM_STRICT_HOST_KEYS", "1")
	f = &fakeRunner{answers: verifyAnswers("api", "console", "site")}
	if _, _, err := projectRun(t, f, "--file", file); err != nil {
		t.Errorf("strict mode should let an unpinned VM through to the strict connect: %v", err)
	}
}

func TestProjectReleaseRefusesAPinThatDisagreesWithKnownHosts(t *testing.T) {
	dir, _ := projectDir(t, nil)
	for _, which := range []string{"own", "user"} {
		t.Run(which, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			path := filepath.Join(os.Getenv("BARYOVM_HOME"), "known_hosts")
			if which == "user" {
				path = filepath.Join(home, ".ssh", "known_hosts")
			}
			seedKnownHost(t, path, "203.0.113.10:22", newHostKey(t))
			before, _ := os.ReadFile(path)
			f := &fakeRunner{answers: verifyAnswers("api", "console", "site")}
			_, dialed, err := projectRun(t, f, "--file", filepath.Join(dir, fleet.DefaultProjectFile))
			if err == nil || !strings.Contains(err.Error(), "vms[0].hostKey") {
				t.Fatalf("a pinned key that contradicts a recorded one must be refused, got %v", err)
			}
			if len(dialed) != 0 {
				t.Error("nothing may connect")
			}
			if after, _ := os.ReadFile(path); string(after) != string(before) {
				t.Error("the refusal must leave known_hosts unchanged")
			}
		})
	}
}

// seedKnownHost records a key the way ssh would, so a test can set up what the machine already trusts.
func seedKnownHost(t *testing.T, path, hostport, authorized string) {
	t.Helper()
	k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(authorized))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	line := knownhosts.Line([]string{hostport}, k) + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Risk: a key or token is committed in the project file.
func TestProjectReleaseRefusesALiteralKeyBeforeConnecting(t *testing.T) {
	dir, _ := projectDir(t, func(p map[string]any) {
		p["vms"].([]any)[0].(map[string]any)["keyPath"] = "-----BEGIN OPENSSH " + "PRIVATE " + "KEY" + "-----\nAAAA\n-----END OPENSSH " + "PRIVATE " + "KEY" + "-----"
	})
	f := &fakeRunner{answers: verifyAnswers("api", "console", "site")}
	stdout, dialed, err := projectRun(t, f, "--file", filepath.Join(dir, fleet.DefaultProjectFile))
	if err == nil || !strings.Contains(err.Error(), "vms[0].keyPath") {
		t.Fatalf("want a refusal naming vms[0].keyPath, got %v", err)
	}
	if strings.Contains(err.Error(), "AAAA") || strings.Contains(stdout, "AAAA") {
		t.Error("the refusal must not print the key")
	}
	if len(dialed) != 0 {
		t.Error("nothing may connect")
	}
}

func TestProjectReleaseNamesAnUnsetKeyVariable(t *testing.T) {
	dir, _ := projectDir(t, nil)
	t.Setenv("BARYOVM_DEPLOY_KEY_PATH", "")
	f := &fakeRunner{}
	_, dialed, err := projectRun(t, f, "--file", filepath.Join(dir, fleet.DefaultProjectFile))
	if err == nil || !strings.Contains(err.Error(), "BARYOVM_DEPLOY_KEY_PATH") || len(dialed) != 0 {
		t.Fatalf("want the variable named and no connection, got %v (dialed %d)", err, len(dialed))
	}
}

// The merge question is kept out: fleet.json is not read, so a machine that has both releases what
// the project file says.
func TestProjectReleaseIgnoresFleetJSON(t *testing.T) {
	dir, _ := projectDir(t, nil)
	local := &fleet.Store{
		VMs:    []fleet.VM{{Name: "client-vm", Host: "198.51.100.99", User: "me", KeyPath: "/Users/me/.ssh/id"}},
		Stacks: []fleet.Stack{{Name: "api", VM: "client-vm", Dir: "/elsewhere", ReleaseFile: "/nonexistent.json"}},
	}
	if err := local.Save(); err != nil {
		t.Fatal(err)
	}
	f := &fakeRunner{answers: verifyAnswers("api", "console", "site")}
	_, dialed, err := projectRun(t, f, "--file", filepath.Join(dir, fleet.DefaultProjectFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, vm := range dialed {
		if vm.Host != "203.0.113.10" {
			t.Errorf("dialled %s, which is fleet.json's VM and not the project's", vm.Host)
		}
	}
	after, _ := fleet.Load()
	if len(after.Stacks) != 1 || after.Stacks[0].Dir != "/elsewhere" {
		t.Errorf("fleet.json must be left as it was: %+v", after.Stacks)
	}
}

func TestProjectReleaseResolvesManifestsRelativeToTheFile(t *testing.T) {
	dir, _ := projectDir(t, nil)
	// Run from somewhere else: the manifests are found next to the project file, not in the cwd.
	t.Chdir(t.TempDir())
	f := &fakeRunner{answers: verifyAnswers("api", "console", "site")}
	if _, _, err := projectRun(t, f, "--file", filepath.Join(dir, fleet.DefaultProjectFile)); err != nil {
		t.Fatalf("release from another directory: %v", err)
	}
}

// Done when: export from an existing fleet produces a file that project release accepts.
func TestProjectExportProducesAFileProjectReleaseAccepts(t *testing.T) {
	t.Setenv("BARYOVM_HOME", t.TempDir())
	t.Setenv("BARYOVM_DEPLOY_KEY_PATH", "/keys/ci-id")
	t.Setenv("BARYOVM_STRICT_HOST_KEYS", "")
	out := t.TempDir()

	var stacks []fleet.Stack
	for _, n := range []string{"api", "console"} {
		src := filepath.Join(out, "src-"+n)
		if err := os.MkdirAll(filepath.Join(src, "web"), 0o755); err != nil {
			t.Fatal(err)
		}
		m, _ := json.Marshal(map[string]any{"localRoot": src, "remoteRoot": "/srv/" + n, "sync": []string{"web"},
			"noCompose": true, "verify": []string{"verify-" + n}, "verifyAttempts": 1})
		mf := filepath.Join(out, "deploy", n+".release.json")
		if err := os.MkdirAll(filepath.Dir(mf), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(mf, m, 0o600); err != nil {
			t.Fatal(err)
		}
		stacks = append(stacks, fleet.Stack{Name: n, VM: "client-vm", Dir: "/opt/" + n, ReleaseFile: mf, EnvFile: ".env", HealthURL: "http://127.0.0.1:1/h"})
	}
	hostKey := newHostKey(t)
	seedKnownHost(t, filepath.Join(os.Getenv("BARYOVM_HOME"), "known_hosts"), "203.0.113.10:22", hostKey)
	store := &fleet.Store{
		VMs:    []fleet.VM{{Name: "client-vm", Host: "203.0.113.10", User: "deploy", KeyPath: "/Users/me/.ssh/deploy_ed25519", Provider: "ssh"}},
		Stacks: stacks,
	}
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}

	file := filepath.Join(out, fleet.DefaultProjectFile)
	if _, _, err := runCmdE(t, newProjectExportCmd(), nil, true, "--out", file, "--name", "demo"); err != nil {
		t.Fatalf("export: %v", err)
	}
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	for _, leak := range []string{out, "/Users/me", "deploy_ed25519"} {
		if strings.Contains(text, leak) {
			t.Errorf("the exported file holds a machine-local value %q:\n%s", leak, text)
		}
	}
	p, err := fleet.ParseProject(b)
	if err != nil {
		t.Fatalf("export wrote a file its own parser refuses: %v\n%s", err, text)
	}
	if p.Stacks[0].ReleaseFile != "deploy/api.release.json" || p.VMs[0].KeyPath != "env:BARYOVM_DEPLOY_KEY_PATH" || p.VMs[0].HostKey != hostKey {
		t.Errorf("paths, key reference or host key not exported as expected: %+v %+v", p.Stacks[0], p.VMs[0])
	}
	if strings.Join(p.Order, ",") != "api,console" || p.Stacks[0].EnvFile != ".env" || p.Stacks[0].HealthURL == "" {
		t.Errorf("order or stack fields lost: %+v", p)
	}

	// The same file, on a runner with nothing but the file and the key.
	t.Setenv("BARYOVM_HOME", t.TempDir())
	f := &fakeRunner{answers: verifyAnswers("api", "console")}
	f.answers["curl"] = ""
	stdout, _, err := projectRun(t, f, "--file", file)
	if err != nil {
		t.Fatalf("project release refused the exported file: %v\n%s", err, strings.Join(f.seen, "\n"))
	}
	if r := parseReport(t, stdout); !r.OK || len(r.Data.Stacks) != 2 {
		t.Errorf("want both stacks released: %+v", r)
	}
}

func TestProjectExportRefusesWhatCannotBeShared(t *testing.T) {
	t.Setenv("BARYOVM_HOME", t.TempDir())
	out := t.TempDir()
	elsewhere := filepath.Join(t.TempDir(), "r.json")
	inside := filepath.Join(out, "r.json")
	for _, c := range []struct {
		name, release, want string
	}{
		{"manifest outside the output directory", elsewhere, "stacks[0].releaseFile"},
		{"no manifest", "", "stacks[0].releaseFile"},
	} {
		t.Run(c.name, func(t *testing.T) {
			store := &fleet.Store{
				VMs:    []fleet.VM{{Name: "v", Host: "203.0.113.10", User: "u", KeyPath: "/k"}},
				Stacks: []fleet.Stack{{Name: "s", VM: "v", Dir: "/opt/s", ReleaseFile: c.release}},
			}
			_, _, err := exportProject(store, out, "demo", "", nil, nil)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("want an error naming %s, got %v", c.want, err)
			}
		})
	}
	_ = inside

	store := &fleet.Store{
		VMs:    []fleet.VM{{Name: "v", Host: "h", User: "u", KeyPath: "/k"}},
		Stacks: []fleet.Stack{{Name: "s", VM: "v", Dir: "/opt/s", ReleaseFile: inside}},
	}
	if _, _, err := exportProject(store, out, "demo", "bad name", nil, nil); err == nil {
		t.Error("--key-env must be a valid variable name")
	}
	if _, _, err := exportProject(store, out, "demo", "", []string{"missing"}, nil); err == nil {
		t.Error("--stack naming nothing must be refused")
	}
}

func TestProjectExportKeyReferencesAreDistinctPerVM(t *testing.T) {
	out := t.TempDir()
	store := &fleet.Store{
		VMs: []fleet.VM{
			{Name: "web-1", Host: "h1", User: "u", KeyPath: "/k1"},
			{Name: "db.2", Host: "h2", User: "u", KeyPath: "/k2"},
		},
		Stacks: []fleet.Stack{
			{Name: "a", VM: "web-1", Dir: "/opt/a", ReleaseFile: filepath.Join(out, "a.json")},
			{Name: "b", VM: "db.2", Dir: "/opt/b", ReleaseFile: filepath.Join(out, "b.json")},
		},
	}
	p, _, err := exportProject(store, out, "demo", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.VMs[0].KeyPath != "env:BARYOVM_DEPLOY_KEY_PATH_WEB_1" || p.VMs[1].KeyPath != "env:BARYOVM_DEPLOY_KEY_PATH_DB_2" {
		t.Errorf("each VM needs its own variable: %q %q", p.VMs[0].KeyPath, p.VMs[1].KeyPath)
	}
}

func TestProjectExportWillNotOverwriteWithoutForce(t *testing.T) {
	t.Setenv("BARYOVM_HOME", t.TempDir())
	file := filepath.Join(t.TempDir(), fleet.DefaultProjectFile)
	if err := os.WriteFile(file, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCmdE(t, newProjectExportCmd(), nil, true, "--out", file); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("want a refusal that names --force, got %v", err)
	}
}
