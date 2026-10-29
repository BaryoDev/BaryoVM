// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"github.com/BaryoDev/BaryoVM/internal/sshx"
)

// failingRunner answers like a fakeRunner, except that a command containing a key of fail returns that
// error. It is how a test says "the dump failed" or "the stack dir is not there yet".
type failingRunner struct {
	*fakeRunner
	fail map[string]string
}

func (r failingRunner) Run(cmd string) (string, error) {
	for match, msg := range r.fail {
		if strings.Contains(cmd, match) {
			r.seen = append(r.seen, cmd)
			return "", errors.New(msg)
		}
	}
	return r.fakeRunner.Run(cmd)
}

type closableRunner struct{ sshx.Runner }

func (closableRunner) Close() error { return nil }

// releasePathFixture is releaseFixture with a manifest the caller shapes, and the stack's healthUrl.
func releasePathFixture(t *testing.T, manifest map[string]any, healthURL string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("BARYOVM_HOME", home)
	src := filepath.Join(home, "src")
	if err := os.MkdirAll(filepath.Join(src, "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := map[string]any{
		"localRoot":  src,
		"remoteRoot": "/home/opc/umbraco-pwa-src",
		"sync":       []string{"web"},
	}
	for k, v := range manifest {
		m[k] = v
	}
	body, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(home, "release.json")
	if err := os.WriteFile(manifestPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	store := &fleet.Store{
		VMs: []fleet.VM{{Name: "vm1", Host: "10.0.0.1", User: "opc", KeyPath: "/keys/id"}},
		Stacks: []fleet.Stack{{
			Name: "demo", VM: "vm1", Dir: "/home/opc/umbraco-pwa", HealthURL: healthURL,
			DBContainer: "umbraco-pwa-db", DBName: "umbraco", ReleaseFile: manifestPath,
		}},
	}
	if err := store.Save(); err != nil {
		t.Fatalf("seed fleet: %v", err)
	}
	return src
}

// releaseOn runs `stack release demo` against r and records the local rsync in seen.
func releaseOn(t *testing.T, r sshx.Runner, seen *[]string, jsonMode bool) (string, string, error) {
	t.Helper()
	withLookPath(t, hasRsync)
	prevDial, prevLocal := dialRelease, runLocal
	dialRelease = func(*fleet.VM) (remoteSession, error) { return closableRunner{r}, nil }
	runLocal = func(c *exec.Cmd) (string, error) {
		*seen = append(*seen, "local: "+strings.Join(c.Args, " "))
		return "", nil
	}
	t.Cleanup(func() { dialRelease, runLocal = prevDial, prevLocal })
	return runCmdE(t, newStackReleaseCmd(), r, jsonMode, "demo")
}

func releaseData(t *testing.T, stdout string) map[string]any {
	t.Helper()
	_, data := parseEnvelope(t, stdout)
	return dataField(t, data)
}

func verifyField(t *testing.T, d map[string]any) map[string]any {
	t.Helper()
	v, ok := d["verify"].(map[string]any)
	if !ok {
		t.Fatalf("data.verify missing: %v", d)
	}
	return v
}

// #122: a success that does not say what was checked reads the same whether the site was probed or
// nothing was.
func TestReleaseReportsTheManifestVerifyThatRan(t *testing.T) {
	releasePathFixture(t, map[string]any{"verify": []string{"manifest-check-marker"}, "noCompose": true}, "")
	f := &fakeRunner{answers: map[string]string{"pg_dump": "", "manifest-check-marker": ""}}
	var local []string

	stdout, _, err := releaseOn(t, f, &local, true)
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	v := verifyField(t, releaseData(t, stdout))
	if v["source"] != "manifest" || v["checks"] != float64(1) || v["attempts"] != float64(1) {
		t.Errorf("data.verify = %v", v)
	}

	f2 := &fakeRunner{answers: map[string]string{"pg_dump": "", "manifest-check-marker": ""}}
	_, stderr, err := releaseOn(t, f2, &local, false)
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if !strings.Contains(stderr, "verified by the manifest verify list, 1 check(s) passed on attempt 1") {
		t.Errorf("the success line should name the manifest verify and the attempt:\n%s", stderr)
	}
}

func TestReleaseReportsTheHealthURLThatRan(t *testing.T) {
	releasePathFixture(t, map[string]any{"noCompose": true}, "http://127.0.0.1:8080/health")
	f := &fakeRunner{answers: map[string]string{"pg_dump": "", "curl --fail": ""}}
	var local []string

	stdout, _, err := releaseOn(t, f, &local, true)
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	v := verifyField(t, releaseData(t, stdout))
	if v["source"] != "healthUrl" || v["attempts"] != float64(1) {
		t.Errorf("data.verify = %v", v)
	}

	f2 := &fakeRunner{answers: map[string]string{"pg_dump": "", "curl --fail": ""}}
	_, stderr, err := releaseOn(t, f2, &local, false)
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if !strings.Contains(stderr, "verified by the stack healthUrl") {
		t.Errorf("the success line should name the healthUrl:\n%s", stderr)
	}
}

func TestReleaseWithNothingToVerifyKeepsTheWarningAndSaysNone(t *testing.T) {
	releasePathFixture(t, map[string]any{"noCompose": true}, "")
	f := &fakeRunner{answers: map[string]string{"pg_dump": ""}}
	var local []string

	stdout, _, err := releaseOn(t, f, &local, true)
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	v := verifyField(t, releaseData(t, stdout))
	if v["source"] != "none" || v["checks"] != float64(0) || v["attempts"] != float64(0) {
		t.Errorf("data.verify = %v", v)
	}

	f2 := &fakeRunner{answers: map[string]string{"pg_dump": ""}}
	_, stderr, err := releaseOn(t, f2, &local, false)
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if !strings.Contains(stderr, "nothing to verify") {
		t.Errorf("the warning should stay:\n%s", stderr)
	}
	if strings.Contains(stderr, "verified by") {
		t.Errorf("nothing was verified, but the output says it was:\n%s", stderr)
	}
}

// #121: a first release has no database container, so the backup is skipped with one line, and the
// absent stack dir does not draw a container-name warning.
func TestFirstReleaseWithADatabaseSkipsTheBackup(t *testing.T) {
	releasePathFixture(t, map[string]any{"verify": []string{"manifest-check-marker"}}, "")
	f := failingRunner{
		fakeRunner: &fakeRunner{answers: map[string]string{
			"--format '{{.Names}}'": "some-other-container\n",
			"manifest-check-marker": "",
			"up -d":                 "",
		}},
		fail: map[string]string{
			"config --format json": "remote `cd /home/opc/umbraco-pwa && docker compose config`: exit status 1: no such file: /home/opc/umbraco-pwa: No such file or directory",
		},
	}
	var local []string

	_, stderr, err := releaseOn(t, f, &local, false)
	if err != nil {
		t.Fatalf("a first release should go through: %v\nIt ran:\n%s", err, strings.Join(f.seen, "\n"))
	}
	for _, cmd := range f.seen {
		if strings.Contains(cmd, "pg_dump") {
			t.Errorf("there is no database to dump, but this ran: %s", cmd)
		}
	}
	if !strings.Contains(stderr, "skipping the pre-release backup") {
		t.Errorf("want one line saying the backup was skipped:\n%s", stderr)
	}
	if strings.Contains(stderr, "could not check container names") {
		t.Errorf("an absent stack dir should not warn:\n%s", stderr)
	}
}

// A container that exists and fails to dump is a real problem and still stops the release.
func TestReleaseStopsWhenAnExistingDatabaseContainerFailsToDump(t *testing.T) {
	releasePathFixture(t, nil, "")
	f := failingRunner{
		fakeRunner: &fakeRunner{answers: map[string]string{"--format '{{.Names}}'": "umbraco-pwa-db\n"}},
		fail:       map[string]string{"pg_dump": "remote `docker exec ... pg_dump ...`: exit status 1: pg_dump: connection refused"},
	}
	var local []string

	_, _, err := releaseOn(t, f, &local, true)
	if err == nil || !strings.Contains(err.Error(), "pre-release backup failed") {
		t.Fatalf("want the release stopped by the backup, got %v", err)
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("the error should carry the cause: %v", err)
	}
	if strings.Contains(err.Error(), "docker exec") {
		t.Errorf("the error should not repeat the script: %v", err)
	}
	if len(local) != 0 {
		t.Errorf("nothing should have been synced: %v", local)
	}
}

// When the probe cannot tell and the dump then says the container is missing, the message names the
// container rather than the script.
func TestBackupFailureNamesTheMissingContainer(t *testing.T) {
	releasePathFixture(t, nil, "")
	f := failingRunner{
		fakeRunner: &fakeRunner{answers: map[string]string{}},
		fail: map[string]string{"pg_dump": "remote `bash -c 'docker exec umbraco-pwa-db pg_dump'`: exit status 1: " +
			"Error response from daemon: No such container: umbraco-pwa-db"},
	}
	var local []string

	_, _, err := releaseOn(t, f, &local, true)
	if err == nil || !strings.Contains(err.Error(), "no container named umbraco-pwa-db") {
		t.Fatalf("want the cause named, got %v", err)
	}
	if strings.Contains(err.Error(), "pg_dump") {
		t.Errorf("the message should not name the script: %v", err)
	}
}

// #68: a missing build artifact refuses before the backup, the sync and postDeploy.
func TestReleaseRefusesAMissingRequiredLocalPathBeforeSyncing(t *testing.T) {
	releasePathFixture(t, map[string]any{
		"noCompose":    true,
		"requireLocal": []string{"web/dist/index.html"},
		"postDeploy":   []string{"post-marker"},
	}, "")
	f := &fakeRunner{answers: map[string]string{}}
	var local []string

	_, _, err := releaseOn(t, f, &local, true)
	if err == nil {
		t.Fatal("release went ahead without the required path")
	}
	if !strings.Contains(err.Error(), filepath.Join("web", "dist", "index.html")) {
		t.Errorf("the error should name the missing path: %v", err)
	}
	if len(local) != 0 || len(f.seen) != 0 {
		t.Errorf("nothing should have run, got local %v remote %v", local, f.seen)
	}
}

func TestReleaseGoesAheadWhenTheRequiredLocalPathExists(t *testing.T) {
	src := releasePathFixture(t, map[string]any{
		"noCompose":    true,
		"requireLocal": []string{"web/dist/index.html"},
		"postDeploy":   []string{"post-marker"},
	}, "")
	if err := os.MkdirAll(filepath.Join(src, "web", "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "web", "dist", "index.html"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakeRunner{answers: map[string]string{"pg_dump": "", "post-marker": ""}}
	var local []string

	if _, _, err := releaseOn(t, f, &local, true); err != nil {
		t.Fatalf("release: %v", err)
	}
	if len(local) == 0 {
		t.Error("the sync should have run")
	}
}
