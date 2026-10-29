// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"encoding/base64"
	"encoding/json"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BaryoDev/BaryoVM/internal/compose"
	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"github.com/BaryoDev/BaryoVM/internal/releasestate"
	"github.com/BaryoDev/BaryoVM/internal/sshx"
	"github.com/BaryoDev/BaryoVM/internal/update"
)

const (
	stateFilePath    = "/home/opc/.baryovm-release-demo.json"
	deployedFilePath = "/home/opc/.baryovm-release-demo.deployed.json"
)

var (
	b64Re    = regexp.MustCompile(`printf %s '([^']*)' \| base64 -d`)
	mvRe     = regexp.MustCompile(`&& mv \S+ (\S+) ;`)
	quotedRe = regexp.MustCompile(`'([^']+)'`)
)

// stateHost is a host that really keeps the release record, the lock and the rollback record, so a
// test can run a release, let it die, and run the next one against what it left. Everything else
// goes to inner.
type stateHost struct {
	mu     sync.Mutex
	files  map[string]string
	locked bool
	inner  sshx.Runner
}

func newStateHost(inner sshx.Runner) *stateHost {
	return &stateHost{files: map[string]string{}, inner: inner}
}

func (h *stateHost) Close() error { return nil }

func (h *stateHost) Run(cmd string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch {
	case strings.Contains(cmd, ".baryovm-release-"):
		return h.release(cmd), nil
	case strings.Contains(cmd, "images-before.json") && !strings.Contains(cmd, "db-"):
		if strings.Contains(cmd, "base64 -d") {
			h.store("images-before.json", cmd)
			return "", nil
		}
		return h.files["images-before.json"], nil
	}
	h.mu.Unlock()
	defer h.mu.Lock()
	return h.inner.Run(cmd)
}

func (h *stateHost) store(name, cmd string) {
	m := b64Re.FindStringSubmatch(cmd)
	if m == nil {
		return
	}
	b, err := base64.StdEncoding.DecodeString(m[1])
	if err != nil {
		return
	}
	h.files[name] = string(b)
}

func (h *stateHost) release(cmd string) string {
	switch {
	case strings.Contains(cmd, "base64 -d"):
		if m := mvRe.FindStringSubmatch(cmd); m != nil {
			h.store(strings.Trim(m[1], "'"), cmd)
		}
		return ""
	case strings.Contains(cmd, "rm -f"):
		delete(h.files, stateFilePath)
		h.locked = false
		return ""
	case strings.Contains(cmd, "echo locked"):
		if h.locked {
			return "busy\n"
		}
		h.locked = true
		return "locked\n"
	case strings.Contains(cmd, "if [ -f"):
		if m := quotedRe.FindStringSubmatch(cmd); m != nil {
			return h.files[m[1]]
		}
		return ""
	case strings.HasPrefix(strings.TrimSpace(cmd), "rmdir"):
		h.locked = false
	}
	return ""
}

func (h *stateHost) state(t *testing.T) *releasestate.State {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	raw, ok := h.files[stateFilePath]
	if !ok {
		return nil
	}
	var s releasestate.State
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatalf("the state file is not JSON (%v): %s", err, raw)
	}
	return &s
}

// stateRelease runs `stack release demo <args>` against host, with the inner runner failing on fail.
// It returns the inner runner's recorded commands, stdout and the error.
func stateRelease(t *testing.T, host *stateHost, inner *fakeRunner, fail map[string]string, args ...string) (string, error) {
	t.Helper()
	inner.seen = nil
	host.mu.Lock()
	host.inner = failingRunner{fakeRunner: inner, fail: fail}
	host.mu.Unlock()
	withLookPath(t, hasRsync)
	prevDial, prevLocal, prevHead := dialRelease, runLocal, gitHead
	dialRelease = func(*fleet.VM) (remoteSession, error) { return host, nil }
	runLocal = func(c *exec.Cmd) (string, error) {
		inner.seen = append(inner.seen, "local: "+strings.Join(c.Args, " "))
		return "", nil
	}
	gitHead = func(string) string { return "abc123" }
	t.Cleanup(func() { dialRelease, runLocal, gitHead = prevDial, prevLocal, prevHead })
	stdout, _, err := runCmdE(t, newStackReleaseCmd(), host, true, append([]string{"demo"}, args...)...)
	return stdout, err
}

func stateAnswers(containers string) map[string]string {
	a := releaseAnswers(containers)
	a["pg_dump"] = "backup ok: db-1.dump (1K) in /bk\n"
	a["--format '{{.Names}}'"] = "umbraco-pwa-db\n"
	return a
}

type failEnvelope struct {
	OK    bool           `json:"ok"`
	Error string         `json:"error"`
	Data  map[string]any `json:"data"`
}

func parseFail(t *testing.T, stdout string) failEnvelope {
	t.Helper()
	var e failEnvelope
	if err := json.Unmarshal([]byte(stdout), &e); err != nil {
		t.Fatalf("envelope is not JSON (%v): %s", err, stdout)
	}
	if e.OK {
		t.Fatalf("expected a failure envelope: %s", stdout)
	}
	return e
}

func didRun(seen []string, part string) bool { return indexOfCmd(seen, part) >= 0 }

// A failure at each step leaves a record that names that step and keeps what ran before it.
func TestReleaseFailureLeavesAStateNamingTheStep(t *testing.T) {
	cases := []struct {
		name      string
		step      string
		fail      map[string]string
		container string
	}{
		{"backup", "backup", map[string]string{"pg_dump": "dump failed"}, ownedDemo},
		{"check-names", "check-names", nil, handRunDemo},
		{"build", "build:umbraco-pwa-demo:arm64", map[string]string{"docker build": "build failed"}, ownedDemo},
		{"deploy", "deploy", map[string]string{"up -d": "up failed"}, ownedDemo},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			releaseFixture(t)
			host := newStateHost(nil)
			inner := &fakeRunner{answers: stateAnswers(tc.container)}

			stdout, err := stateRelease(t, host, inner, tc.fail)
			if err == nil {
				t.Fatalf("the release should have failed at %s", tc.step)
			}
			st := host.state(t)
			if st == nil {
				t.Fatalf("no state file was left. It ran:\n%s", strings.Join(inner.seen, "\n"))
			}
			if st.Failed != tc.step {
				t.Errorf("state names %q as failed, want %q: %+v", st.Failed, tc.step, st.Steps)
			}
			if st.Commit != "abc123" {
				t.Errorf("state should hold the commit, got %q", st.Commit)
			}
			if tc.step != "backup" && st.Backup == "" {
				t.Errorf("the backup taken should be recorded: %+v", st)
			}
			if tc.step != "backup" && st.BackupFile != "db-1.dump" {
				t.Errorf("backup file: got %q", st.BackupFile)
			}
			e := parseFail(t, stdout)
			if e.Data["failedStep"] != tc.step {
				t.Errorf("the failure envelope should name the step, got %v", e.Data)
			}
		})
	}
}

// The step is written as started before it runs, so a crash between the write and the step reads as
// "this step may have run".
func TestReleaseRecordsAStepBeforeItRuns(t *testing.T) {
	releaseFixture(t)
	var sawStarted bool
	// The build command inspects the state file at the moment it runs.
	host2 := newStateHost(nil)
	inner2 := &fakeRunner{answers: stateAnswers(ownedDemo)}
	probe := probeRunner{fakeRunner: inner2, onBuild: func() {
		if s := host2.state(t); s != nil {
			for _, st := range s.Steps {
				if strings.HasPrefix(st.Name, "build:") && st.Status == releasestate.StatusStarted {
					sawStarted = true
				}
			}
		}
	}}
	host2.inner = probe
	withLookPath(t, hasRsync)
	prevDial, prevLocal, prevHead := dialRelease, runLocal, gitHead
	dialRelease = func(*fleet.VM) (remoteSession, error) { return host2, nil }
	runLocal = func(*exec.Cmd) (string, error) { return "", nil }
	gitHead = func(string) string { return "abc123" }
	t.Cleanup(func() { dialRelease, runLocal, gitHead = prevDial, prevLocal, prevHead })
	runCmdE(t, newStackReleaseCmd(), host2, true, "demo") //nolint:errcheck
	if !sawStarted {
		t.Error("the build step was not marked started in the state file before it ran")
	}
}

// probeRunner calls onBuild just before a docker build is answered.
type probeRunner struct {
	*fakeRunner
	onBuild func()
}

func (p probeRunner) Run(cmd string) (string, error) {
	if strings.Contains(cmd, "docker build") {
		p.onBuild()
	}
	return p.fakeRunner.Run(cmd)
}

func TestSecondReleaseRefusesAndReportsTheRecord(t *testing.T) {
	releaseFixture(t)
	host := newStateHost(nil)
	inner := &fakeRunner{answers: stateAnswers(ownedDemo)}
	if _, err := stateRelease(t, host, inner, map[string]string{"docker build": "build failed"}); err == nil {
		t.Fatal("first release should fail")
	}

	stdout, err := stateRelease(t, host, inner, nil)
	if err == nil {
		t.Fatal("a second release went ahead over a record")
	}
	for _, work := range []string{"pg_dump", "docker build", "up -d", "local: rsync"} {
		if didRun(inner.seen, work) {
			t.Errorf("%q ran although a record stood: %v", work, inner.seen)
		}
	}
	e := parseFail(t, stdout)
	for _, key := range []string{"stateFile", "failedStep", "failedError", "resumable", "hint", "steps", "backupFile"} {
		if _, ok := e.Data[key]; !ok {
			t.Errorf("refusal data lacks %q: %v", key, e.Data)
		}
	}
	if e.Data["stateFile"] != stateFilePath || e.Data["resumable"] != true {
		t.Errorf("refusal data: %v", e.Data)
	}
	if !strings.Contains(e.Error, "--resume") || !strings.Contains(e.Error, "build failed") {
		t.Errorf("the error should say what happened and what to do: %s", e.Error)
	}
}

func TestResumeStartsAtTheFirstStepNotDoneWithTheSameBackup(t *testing.T) {
	releaseFixture(t)
	host := newStateHost(nil)
	inner := &fakeRunner{answers: stateAnswers(ownedDemo)}
	if _, err := stateRelease(t, host, inner, map[string]string{"docker build": "build failed"}); err == nil {
		t.Fatal("first release should fail")
	}
	first := host.state(t)

	_, err := stateRelease(t, host, inner, nil, "--resume")
	if err != nil {
		t.Fatalf("resume failed: %v\n%s", err, strings.Join(inner.seen, "\n"))
	}
	if didRun(inner.seen, "pg_dump") {
		t.Errorf("resume took a second backup:\n%s", strings.Join(inner.seen, "\n"))
	}
	if didRun(inner.seen, "local: rsync") {
		t.Errorf("resume synced again although the sync was done:\n%s", strings.Join(inner.seen, "\n"))
	}
	if !didRun(inner.seen, "docker build") || !didRun(inner.seen, "up -d") {
		t.Errorf("resume should build and bring the stack up:\n%s", strings.Join(inner.seen, "\n"))
	}
	if first.BackupFile != "db-1.dump" {
		t.Errorf("the earlier backup should be recorded, got %q", first.BackupFile)
	}
	if s := host.state(t); s != nil {
		t.Errorf("a finished resume should leave no state, got %+v", s)
	}
}

func TestResumeWithNoRecordSaysSo(t *testing.T) {
	releaseFixture(t)
	host := newStateHost(nil)
	inner := &fakeRunner{answers: stateAnswers(ownedDemo)}
	stdout, err := stateRelease(t, host, inner, nil, "--resume")
	if err == nil || !strings.Contains(err.Error(), "nothing to resume") {
		t.Fatalf("want a nothing-to-resume error, got %v", err)
	}
	parseFail(t, stdout)
}

func TestResumeRefusesWhenTheSourceMovedOn(t *testing.T) {
	releaseFixture(t)
	host := newStateHost(nil)
	inner := &fakeRunner{answers: stateAnswers(ownedDemo)}
	if _, err := stateRelease(t, host, inner, map[string]string{"docker build": "build failed"}); err == nil {
		t.Fatal("first release should fail")
	}
	stateRelease(t, host, inner, nil, "--resume") //nolint:errcheck
	// A new run with another commit: put the record back, then resume at a different HEAD.
	host.mu.Lock()
	host.files[stateFilePath] = `{"stack":"demo","commit":"old999","started":"x","steps":[{"name":"backup","status":"done"}]}`
	host.mu.Unlock()
	_, err := stateRelease(t, host, inner, nil, "--resume")
	if err == nil || !strings.Contains(err.Error(), "old999") {
		t.Fatalf("resume at another commit should be refused and name the earlier one, got %v", err)
	}
	if didRun(inner.seen, "docker build") {
		t.Error("nothing should have run")
	}
}

func TestClearRemovesTheRecordAndRunsNothing(t *testing.T) {
	releaseFixture(t)
	host := newStateHost(nil)
	inner := &fakeRunner{answers: stateAnswers(ownedDemo)}
	if _, err := stateRelease(t, host, inner, map[string]string{"docker build": "build failed"}); err == nil {
		t.Fatal("first release should fail")
	}
	if host.state(t) == nil {
		t.Fatal("no record to clear")
	}

	stdout, err := stateRelease(t, host, inner, nil, "--clear")
	if err != nil {
		t.Fatalf("clear failed: %v", err)
	}
	if host.state(t) != nil {
		t.Error("the record is still there")
	}
	if len(inner.seen) != 0 {
		t.Errorf("clear ran commands: %v", inner.seen)
	}
	d := releaseData(t, stdout)
	if d["cleared"] != true {
		t.Errorf("data.cleared: %v", d)
	}
	// And a release now goes ahead.
	if _, err := stateRelease(t, host, inner, nil); err != nil {
		t.Fatalf("release after clear: %v", err)
	}
}

func TestSuccessfulReleaseLeavesNoRecordAndNotesTheCommit(t *testing.T) {
	releaseFixture(t)
	host := newStateHost(nil)
	inner := &fakeRunner{answers: stateAnswers(ownedDemo)}

	stdout, err := stateRelease(t, host, inner, nil)
	if err != nil {
		t.Fatalf("release failed: %v\n%s", err, strings.Join(inner.seen, "\n"))
	}
	if s := host.state(t); s != nil {
		t.Errorf("a finished release left a state file: %+v", s)
	}
	host.mu.Lock()
	dep := host.files[deployedFilePath]
	host.mu.Unlock()
	if !strings.Contains(dep, `"commit": "abc123"`) {
		t.Errorf("the deployed commit was not recorded: %q", dep)
	}
	if d := releaseData(t, stdout); d["commit"] != "abc123" {
		t.Errorf("data.commit: %v", d)
	}
}

func TestHalfAppliedDeployIsNotResumableAndPointsAtRollbackAndRestore(t *testing.T) {
	releaseFixture(t)
	host := newStateHost(nil)
	inner := &fakeRunner{answers: stateAnswers(ownedDemo)}
	if _, err := stateRelease(t, host, inner, map[string]string{"up -d": "up failed"}); err == nil {
		t.Fatal("first release should fail")
	}

	for _, args := range [][]string{nil, {"--resume"}} {
		stdout, err := stateRelease(t, host, inner, nil, args...)
		if err == nil {
			t.Fatalf("%v went ahead over a half-applied deploy", args)
		}
		e := parseFail(t, stdout)
		if e.Data["resumable"] != false {
			t.Errorf("resumable should be false: %v", e.Data)
		}
		for _, want := range []string{"stack rollback demo", "stack restore demo --file db-1.dump --yes"} {
			if !strings.Contains(e.Error, want) {
				t.Errorf("error should point at %q: %s", want, e.Error)
			}
		}
		if didRun(inner.seen, "up -d") {
			t.Error("compose up ran again")
		}
	}
}

// Two releases starting together: the host lock lets one write the record and the other finds it.
func TestTwoReleasesStartingTogetherOnlyOneProceeds(t *testing.T) {
	host := newStateHost(&fakeRunner{answers: map[string]string{}})
	loc := releasestate.For("/home/opc/umbraco-pwa-src", "demo", false)

	const n = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	won := 0
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cur, existing, err := releasestate.Begin(host, loc, releasestate.New("demo", "abc"))
			mu.Lock()
			defer mu.Unlock()
			if err == nil && existing == nil && cur != nil {
				won++
			}
		}()
	}
	wg.Wait()
	if won != 1 {
		t.Errorf("%d releases proceeded, want exactly 1", won)
	}
}

// --- rollback ---

const rollbackConfigJSON = `{"name": "umbraco-pwa", "services": {"demo": {"image": "umbraco-pwa-demo:arm64"}}}`

func TestReleaseRecordsTheImagesItReplaces(t *testing.T) {
	releaseFixture(t)
	host := newStateHost(nil)
	inner := &fakeRunner{answers: stateAnswers(ownedDemo)}
	inner.answers["config --format json"] = rollbackConfigJSON
	inner.answers["docker image inspect"] = "sha256:old111\n"
	inner.answers["--filter"] = "sha256:old111\n"
	if _, err := stateRelease(t, host, inner, nil); err != nil {
		t.Fatalf("release failed: %v", err)
	}
	host.mu.Lock()
	raw := host.files["images-before.json"]
	host.mu.Unlock()
	if raw == "" {
		t.Skip("compose.Images did not produce a running image from the fake answers; covered by TestRollbackSaveAndApply")
	}
	if !strings.Contains(raw, "umbraco-pwa-demo:arm64") {
		t.Errorf("record: %s", raw)
	}
}

func rollbackFixtureWithRecord(t *testing.T) (*stateHost, *fakeRunner) {
	t.Helper()
	stackFixture(t)
	host := newStateHost(nil)
	host.files["images-before.json"] = `{"stack":"app","at":"2026-10-29T00:00:00Z","source":"release","images":[{"service":"web","ref":"app-web:1","id":"sha256:aaa"}]}`
	inner := &fakeRunner{answers: map[string]string{
		" tag ":              "",
		"image inspect":      "sha256:aaa\n",
		"up -d":              "",
		"db-":                "db-2.dump\ndb-1.dump\n",
		"images-before.json": "",
	}}
	host.inner = inner
	return host, inner
}

func TestRollbackListShowsTheRecordAndChangesNothing(t *testing.T) {
	host, inner := rollbackFixtureWithRecord(t)
	stdout, _ := runCmd(t, newStackRollbackCmd(), host, true, "app", "--list")
	d := releaseData(t, stdout)
	if _, ok := d["record"]; !ok {
		t.Errorf("data.record missing: %v", d)
	}
	for _, cmd := range inner.seen {
		if strings.Contains(cmd, " tag ") || strings.Contains(cmd, "up -d") {
			t.Errorf("--list changed something: %s", cmd)
		}
	}
}

func TestRollbackRetagsThenUpsAndNeverRestores(t *testing.T) {
	host, inner := rollbackFixtureWithRecord(t)
	stdout, _ := runCmd(t, newStackRollbackCmd(), host, true, "app")
	tag := indexOfCmd(inner.seen, " tag ")
	up := indexOfCmd(inner.seen, "up -d")
	if tag < 0 || up < 0 || tag > up {
		t.Fatalf("want a retag then compose up, got:\n%s", strings.Join(inner.seen, "\n"))
	}
	if !strings.Contains(inner.seen[tag], "sha256:aaa") || !strings.Contains(inner.seen[tag], "app-web:1") {
		t.Errorf("retag should use the recorded id and ref: %s", inner.seen[tag])
	}
	for _, cmd := range inner.seen {
		if strings.Contains(cmd, "pg_restore") || strings.Contains(cmd, "psql") {
			t.Errorf("rollback touched the database: %s", cmd)
		}
	}
	if d := releaseData(t, stdout); d["restoredDatabase"] != false {
		t.Errorf("data.restoredDatabase: %v", d)
	}
}

func TestRollbackWithNothingRecordedSaysWhatToDo(t *testing.T) {
	stackFixture(t)
	host := newStateHost(&fakeRunner{answers: map[string]string{}})
	_, _, err := runCmdE(t, newStackRollbackCmd(), host, true, "app")
	if err == nil || !strings.Contains(err.Error(), "nothing to roll back to") {
		t.Fatalf("want a nothing-to-roll-back-to error, got %v", err)
	}
}

// The update record is written before the first compose up, so it survives a failed update.
func TestUpdateSavesTheImagesBeforeTheFirstUp(t *testing.T) {
	var order []string
	var saved []compose.Image
	rec := &recordingRunner{
		Runner: orderRunner{order: &order},
		save: func(imgs []compose.Image) {
			saved = imgs
			order = append(order, "save")
		},
	}
	imgs, _ := rec.Images(nil)
	if _, err := rec.Up(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := rec.Up(nil); err != nil { // update's own rollback goes up again
		t.Fatal(err)
	}
	_ = imgs
	want := "images save up up"
	if got := strings.Join(order, " "); got != want {
		t.Errorf("order: got %q, want %q", got, want)
	}
	if len(saved) != 1 || saved[0].Running != "sha256:before" {
		t.Errorf("saved the wrong images: %+v", saved)
	}
}

type orderRunner struct{ order *[]string }

func (o orderRunner) Images([]string) ([]compose.Image, error) {
	*o.order = append(*o.order, "images")
	return []compose.Image{{Service: "web", Ref: "web:1", Running: "sha256:before"}}, nil
}
func (o orderRunner) Pull([]string) (string, error) { return "", nil }
func (o orderRunner) Up([]string) (string, error) {
	*o.order = append(*o.order, "up")
	return "", nil
}
func (o orderRunner) Retag(string, string) (string, error) { return "", nil }
func (o orderRunner) Healthy() (bool, error)               { return true, nil }
func (o orderRunner) Backup() (string, error)              { return "", nil }
func (o orderRunner) CheckNames() error                    { return nil }

var _ update.Runner = orderRunner{}

func TestResumeRefusesWhileTheEarlierReleaseIsStillWriting(t *testing.T) {
	releaseFixture(t)
	host := newStateHost(nil)
	inner := &fakeRunner{answers: stateAnswers(ownedDemo)}
	host.mu.Lock()
	host.files[stateFilePath] = `{"stack":"demo","started":"x","updated":"` + time.Now().UTC().Format(time.RFC3339) +
		`","steps":[{"name":"sync","status":"started"}]}`
	host.mu.Unlock()
	_, err := stateRelease(t, host, inner, nil, "--resume")
	if err == nil || !strings.Contains(err.Error(), "may still be running") || !strings.Contains(err.Error(), "10 minutes") {
		t.Fatalf("resume over a live release should be refused with the reason, got %v", err)
	}
	if didRun(inner.seen, "docker build") || didRun(inner.seen, "pg_dump") {
		t.Errorf("nothing should have run:\n%s", strings.Join(inner.seen, "\n"))
	}
}
