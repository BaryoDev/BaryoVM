// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"github.com/spf13/cobra"
)

// noRunner fails the test if a dry run runs anything on the VM.
type noRunner struct{ t *testing.T }

func (n noRunner) Run(cmd string) (string, error) {
	n.t.Errorf("dry run ran a remote command: %s", cmd)
	return "", nil
}

// dryGuard makes any dial or local exec fail the test, and returns the fleet.json bytes to compare
// against after the command.
func dryGuard(t *testing.T) []byte {
	t.Helper()
	prevDial, prevLocal, prevKH := dialRelease, runLocal, knownHostsFileFor
	dialRelease = func(*fleet.VM) (remoteSession, error) {
		t.Error("dry run dialed the VM")
		return nil, os.ErrInvalid
	}
	runLocal = func(c *exec.Cmd) (string, error) {
		t.Errorf("dry run ran a local command: %v", c.Args)
		return "", nil
	}
	knownHostsFileFor = func(*fleet.VM) (string, func(), error) {
		t.Error("dry run built a known_hosts file")
		return "", func() {}, os.ErrInvalid
	}
	t.Cleanup(func() { dialRelease, runLocal, knownHostsFileFor = prevDial, prevLocal, prevKH })
	b, err := os.ReadFile(filepath.Join(os.Getenv("BARYOVM_HOME"), "fleet.json"))
	if err != nil {
		t.Fatalf("read fleet.json: %v", err)
	}
	return b
}

func assertFleetUnchanged(t *testing.T, before []byte) {
	t.Helper()
	after, err := os.ReadFile(filepath.Join(os.Getenv("BARYOVM_HOME"), "fleet.json"))
	if err != nil {
		t.Fatalf("read fleet.json: %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("fleet.json changed during a dry run:\nbefore: %s\nafter:  %s", before, after)
	}
}

// dryData runs cmd in JSON mode and returns the plan it emitted.
func dryData(t *testing.T, cmd *cobra.Command, args ...string) dryRunPlan {
	t.Helper()
	before := dryGuard(t)
	stdout, stderr, err := runCmdE(t, cmd, noRunner{t}, true, args...)
	if err != nil {
		t.Fatalf("%v\nstderr: %s", err, stderr)
	}
	assertFleetUnchanged(t, before)
	env, _ := parseEnvelope(t, stdout)
	var p dryRunPlan
	if err := json.Unmarshal(env.Data, &p); err != nil {
		t.Fatalf("data is not a dry-run plan: %v: %s", err, env.Data)
	}
	if !p.DryRun || len(p.Commands) != len(p.Steps) {
		t.Errorf("malformed plan: %+v", p)
	}
	return p
}

// dryReleaseFixture is releaseFixture with a manifest that has hooks and a trailing-slash sync.
func dryReleaseFixture(t *testing.T) {
	t.Helper()
	releaseFixture(t)
	home := os.Getenv("BARYOVM_HOME")
	manifest, _ := json.Marshal(map[string]any{
		"localRoot":  filepath.Join(home, "src"),
		"remoteRoot": "/home/opc/umbraco-pwa-src",
		"sync":       []string{"web/"},
		"builds":     []map[string]string{{"image": "umbraco-pwa-demo:arm64", "dockerfile": "Dockerfile", "context": "web"}},
		"preDeploy":  []string{"echo pre-hook"},
		"postDeploy": []string{"echo post-hook"},
	})
	if err := os.WriteFile(filepath.Join(home, "release.json"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
}

func joinCommands(p dryRunPlan) string { return strings.Join(p.Commands, "\n") }

func TestStackReleaseDryRunShowsEveryStep(t *testing.T) {
	dryReleaseFixture(t)
	withLookPath(t, hasRsync)
	p := dryData(t, newStackReleaseCmd(), "demo", "--dry-run")
	all := joinCommands(p)

	if p.Transport != "rsync" || len(p.Rsync) != 1 {
		t.Fatalf("want one rsync argv, got transport %q rsync %v", p.Transport, p.Rsync)
	}
	rs := strings.Join(p.Rsync[0], " ")
	if !strings.Contains(rs, "--delete") {
		t.Errorf("rsync should carry --delete: %s", rs)
	}
	if !strings.HasSuffix(p.Rsync[0][len(p.Rsync[0])-2], filepath.Join("src", "web")+"/") {
		t.Errorf("rsync source should keep the trailing slash: %v", p.Rsync[0])
	}
	for _, want := range []string{"pg_dump", "docker build", "up -d", "echo pre-hook", "echo post-hook"} {
		if !strings.Contains(all, want) {
			t.Errorf("plan lacks %q:\n%s", want, all)
		}
	}
	if p.Hooks == nil || len(p.Hooks.PreDeploy) != 1 || len(p.Hooks.PostDeploy) != 1 {
		t.Errorf("hooks not listed: %+v", p.Hooks)
	}
	// Order: backup, sync, build, pre hook, up, post hook.
	order := []string{"pg_dump", "rsync", "docker build", "echo pre-hook", "up -d", "echo post-hook"}
	last := -1
	for _, w := range order {
		i := strings.Index(all, w)
		if i < last {
			t.Errorf("%q is out of order in:\n%s", w, all)
		}
		if i > last {
			last = i
		}
	}
}

func TestStackReleaseDryRunHumanOutputGoesToStderr(t *testing.T) {
	dryReleaseFixture(t)
	withLookPath(t, hasRsync)
	before := dryGuard(t)
	stdout, stderr, err := runCmdE(t, newStackReleaseCmd(), noRunner{t}, false, "demo", "--dry-run", "--no-backup", "--no-build")
	if err != nil {
		t.Fatal(err)
	}
	assertFleetUnchanged(t, before)
	if !strings.Contains(stderr, "--delete") || strings.Contains(stderr, "pg_dump") || strings.Contains(stderr, "docker build") {
		t.Errorf("stderr should list the rsync but not the skipped backup or build:\n%s", stderr)
	}
	if strings.Contains(stdout, "--delete") {
		t.Errorf("human mode should keep the plan off stdout: %s", stdout)
	}
}

func TestStackReleaseDryRunWritesNoReleaseRecord(t *testing.T) {
	dryReleaseFixture(t)
	withLookPath(t, hasRsync)
	home := os.Getenv("BARYOVM_HOME")
	dryData(t, newStackReleaseCmd(), "demo", "--dry-run")
	entries, _ := os.ReadDir(home)
	for _, e := range entries {
		switch e.Name() {
		case "fleet.json", "release.json", "src":
		default:
			t.Errorf("dry run left %s in BARYOVM_HOME", e.Name())
		}
	}
}

func TestStackReleaseDryRunRefusesClear(t *testing.T) {
	dryReleaseFixture(t)
	_, _, err := runCmdE(t, newStackReleaseCmd(), noRunner{t}, true, "demo", "--dry-run", "--clear")
	if err == nil {
		t.Fatal("--dry-run with --clear should be refused")
	}
}

func TestStackReleaseDryRunTarTransport(t *testing.T) {
	dryReleaseFixture(t)
	withLookPath(t, noRsync)
	p := dryData(t, newStackReleaseCmd(), "demo", "--dry-run")
	if p.Transport != "tar" || !strings.Contains(joinCommands(p), "tar") {
		t.Errorf("want the tar transport command, got %q:\n%s", p.Transport, joinCommands(p))
	}
}

func TestStackBackupDryRun(t *testing.T) {
	releaseFixture(t)
	p := dryData(t, newStackBackupCmd(), "demo", "--dry-run")
	if len(p.Commands) != 1 || !strings.Contains(p.Commands[0], "pg_dump") {
		t.Errorf("want the pg_dump script, got %v", p.Commands)
	}
}

func TestStackRestoreDryRunNeedsNoYesAndNamesTheFile(t *testing.T) {
	releaseFixture(t)
	p := dryData(t, newStackRestoreCmd(), "demo", "--dry-run")
	if p.File != "newest" || len(p.Notes) == 0 || !strings.Contains(p.Notes[0], "newest") {
		t.Errorf("should say it restores the newest dump: file %q notes %v", p.File, p.Notes)
	}
	if len(p.Commands) != 1 || !strings.Contains(p.Commands[0], "pg_restore") {
		t.Errorf("want the restore script, got %v", p.Commands)
	}

	p = dryData(t, newStackRestoreCmd(), "demo", "--dry-run", "--file", "db-2026.dump")
	if p.File != "db-2026.dump" || !strings.Contains(p.Commands[0], "db-2026.dump") {
		t.Errorf("should name the given file: %q %v", p.File, p.Commands)
	}
}

func TestStackDeployDryRun(t *testing.T) {
	releaseFixture(t)
	p := dryData(t, newStackDeployCmd(), "demo", "--dry-run", "--pull")
	all := joinCommands(p)
	if len(p.Commands) != 2 || !strings.Contains(all, "pull") || !strings.Contains(all, "up -d") {
		t.Errorf("want pull then up, got:\n%s", all)
	}
}

func TestVMBootstrapDryRun(t *testing.T) {
	releaseFixture(t)
	p := dryData(t, newVMBootstrapCmd(), "vm1", "--dry-run")
	if len(p.Commands) != 1 || !strings.Contains(p.Commands[0], "docker") {
		t.Errorf("want the install script, got %v", p.Commands)
	}
}

func TestDeployDryRunMasksEnv(t *testing.T) {
	releaseFixture(t)
	p := dryData(t, newDeployCmd(), "--vm", "vm1", "--image", "nginx:alpine", "--name", "site", "-p", "80:80", "-e", "TOKEN=hunter2", "--pull", "--dry-run")
	all := joinCommands(p)
	if strings.Contains(all, "hunter2") {
		t.Errorf("env value leaked into the plan:\n%s", all)
	}
	for _, want := range []string{"TOKEN", "nginx:alpine", "pull"} {
		if !strings.Contains(all, want) {
			t.Errorf("plan lacks %q:\n%s", want, all)
		}
	}
}
