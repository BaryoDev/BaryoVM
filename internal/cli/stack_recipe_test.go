// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"github.com/BaryoDev/BaryoVM/internal/managed"
	"github.com/BaryoDev/BaryoVM/internal/verify"
)

// recipeHost is a fake VM for recipe releases. It records every command and every stdin, answers
// the snapshot and service-list probes, and fails a command a set number of times.
type recipeHost struct {
	seen    []string
	stdin   []string
	hadPrev bool
	prevSvc string
	// failOnce fails the first command containing the key, then lets it through.
	failOnce map[string]string
	// busy answers the start-lock command as held by another release.
	busy bool
}

func (h *recipeHost) Close() error { return nil }

func (h *recipeHost) Run(cmd string) (string, error) {
	h.seen = append(h.seen, cmd)
	for k, msg := range h.failOnce {
		if strings.Contains(cmd, k) {
			delete(h.failOnce, k)
			return "", errors.New(msg)
		}
	}
	switch {
	case strings.Contains(cmd, "echo locked"):
		if h.busy {
			return "busy\n", nil
		}
		return "locked\n", nil
	case strings.Contains(cmd, "ps -a --format"):
		return "pg\n", nil
	case strings.Contains(cmd, "echo had"):
		if h.hadPrev {
			return "had\n", nil
		}
		return "none\n", nil
	case strings.Contains(cmd, "config --services"):
		return h.prevSvc, nil
	}
	return "", nil
}

func (h *recipeHost) RunInput(cmd, stdin string) (string, error) {
	h.seen = append(h.seen, cmd)
	h.stdin = append(h.stdin, stdin)
	return "", nil
}

func (h *recipeHost) index(part string, from int) int {
	for i := from; i < len(h.seen); i++ {
		if strings.Contains(h.seen[i], part) {
			return i
		}
	}
	return -1
}

const twoServiceRecipe = `{
  "schema": 1,
  "name": "app",
  "services": [
    {"name": "api", "image": "registry.example/app-api:1.2.0", "ports": ["8080:80"]},
    {"name": "web", "image": "registry.example/app-web:1.2.0", "healthUrl": "http://127.0.0.1:3000/health"}
  ],
  "inputs": [
    {"name": "DB_PASSWORD", "secret": true},
    {"name": "SITE_URL", "default": "http://localhost"}
  ]
}`

// recipeFixture registers stack "app" and writes the recipe into a fixture repo directory.
func recipeFixture(t *testing.T, body string, host *recipeHost) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("BARYOVM_HOME", home)
	repo := filepath.Join(home, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, recipeFileName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &fleet.Store{
		VMs:    []fleet.VM{{Name: "vm1", Host: "10.0.0.1", User: "opc", KeyPath: "/keys/id"}},
		Stacks: []fleet.Stack{{Name: "app", VM: "vm1", Dir: "/opt/app"}},
	}
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	prevDial, prevClock := dialRelease, releaseClock
	dialRelease = func(*fleet.VM) (remoteSession, error) { return host, nil }
	releaseClock = verify.Clock{Now: time.Now, Sleep: func(time.Duration) {}}
	t.Cleanup(func() { dialRelease, releaseClock = prevDial, prevClock })
	t.Setenv("APP_DB_PASSWORD", "s3cret-pw-xyz")
	return repo
}

func releaseRecipeCmd(t *testing.T, repo string, extra ...string) (string, error) {
	t.Helper()
	args := append([]string{"app", "--recipe", repo, "--input", "DB_PASSWORD=env:APP_DB_PASSWORD"}, extra...)
	stdout, _, err := runCmdE(t, newStackReleaseCmd(), nil, true, args...)
	return stdout, err
}

// releaseRecipeBare releases with no --input, for recipes that declare none.
func releaseRecipeBare(t *testing.T, repo string, extra ...string) (string, error) {
	t.Helper()
	args := append([]string{"app", "--recipe", repo}, extra...)
	stdout, _, err := runCmdE(t, newStackReleaseCmd(), nil, true, args...)
	return stdout, err
}

func editStack(t *testing.T, fn func(*fleet.Stack)) {
	t.Helper()
	store, err := fleet.Load()
	if err != nil {
		t.Fatal(err)
	}
	fn(store.FindStack("app"))
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
}

func TestRecipeReleaseDeploysTwoServicesInOrder(t *testing.T) {
	host := &recipeHost{}
	repo := recipeFixture(t, twoServiceRecipe, host)

	stdout, err := releaseRecipeCmd(t, repo)
	if err != nil {
		t.Fatalf("release failed: %v\n%s", err, strings.Join(host.seen, "\n"))
	}
	var env struct {
		OK bool `json:"ok"`
	}
	if jerr := json.Unmarshal([]byte(stdout), &env); jerr != nil || !env.OK {
		t.Fatalf("expected an ok envelope: %s", stdout)
	}
	pull := host.index("pull", 0)
	api := host.index("up -d --no-deps 'api'", 0)
	web := host.index("up -d --no-deps 'web'", 0)
	health := host.index("curl -fsS", 0)
	if pull < 0 || api < 0 || web < 0 || pull >= api || api >= web || web >= health {
		t.Fatalf("want pull, api up, web up, web health in that order, got:\n%s", strings.Join(host.seen, "\n"))
	}
	w := host.index("base64 -d", 0)
	if w < 0 {
		t.Fatal("the compose file was never written")
	}
	b64 := strings.TrimPrefix(host.seen[w], "printf %s '")
	b64, _, _ = strings.Cut(b64, "'")
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || !strings.Contains(string(raw), `"registry.example/app-api:1.2.0"`) || !strings.Contains(string(raw), `"registry.example/app-web:1.2.0"`) {
		t.Fatalf("the written compose file lacks the recipe images: %q, %v", raw, err)
	}
	// The secret reaches the VM on stdin and nowhere else.
	onStdin := 0
	for _, s := range host.stdin {
		if strings.Contains(s, "s3cret-pw-xyz") {
			onStdin++
		}
	}
	if onStdin != 1 {
		t.Fatalf("the password should be sent once on stdin, got %q", host.stdin)
	}
	for _, c := range host.seen {
		if strings.Contains(c, "s3cret-pw-xyz") {
			t.Fatalf("the secret is on a command line: %s", c)
		}
	}
	if strings.Contains(stdout, "s3cret-pw-xyz") {
		t.Fatal("the secret is in the envelope")
	}
}

// The ticket's test: the second service fails, the first is put back.
func TestRecipeReleaseFailingSecondServiceRestoresTheFirst(t *testing.T) {
	host := &recipeHost{hadPrev: true, prevSvc: "api\nweb\n",
		failOnce: map[string]string{"up -d --no-deps 'web'": "web would not start"}}
	repo := recipeFixture(t, twoServiceRecipe, host)

	stdout, err := releaseRecipeCmd(t, repo)
	if err == nil {
		t.Fatal("the release should have failed")
	}
	e := parseFail(t, stdout)
	if e.Data["failed"] != "web" {
		t.Fatalf("the failure should name web: %v", e.Data)
	}
	rolled, _ := e.Data["rolledBack"].([]any)
	if len(rolled) != 2 || rolled[0] != "api" || rolled[1] != "web" {
		t.Fatalf("api and web should be restored: %v", e.Data["rolledBack"])
	}
	failedUp := host.index("up -d --no-deps 'web'", 0)
	restore := host.index("cp -p '/opt/app/docker-compose.recipe.yml.prev' '/opt/app/docker-compose.recipe.yml'", failedUp)
	apiAgain := host.index("up -d --no-deps 'api'", restore)
	if failedUp < 0 || restore < 0 || apiAgain < 0 {
		t.Fatalf("want failed web up, restore of the previous file, then api up again:\n%s", strings.Join(host.seen, "\n"))
	}
}

// A service the previous recipe did not have is removed, not restored.
func TestRecipeReleaseRollbackRemovesAServiceThatWasNew(t *testing.T) {
	host := &recipeHost{hadPrev: true, prevSvc: "api\n",
		failOnce: map[string]string{"up -d --no-deps 'web'": "boom"}}
	repo := recipeFixture(t, twoServiceRecipe, host)

	stdout, err := releaseRecipeCmd(t, repo)
	if err == nil {
		t.Fatal("the release should have failed")
	}
	e := parseFail(t, stdout)
	removed, _ := e.Data["removed"].([]any)
	if len(removed) != 1 || removed[0] != "web" {
		t.Fatalf("web was new and should be removed: %v", e.Data)
	}
	if host.index("rm -sf 'web'", 0) < 0 {
		t.Fatalf("no rm -sf for web:\n%s", strings.Join(host.seen, "\n"))
	}
}

func TestRecipeReleaseRefusesAnUnknownSchemaThroughTheCommand(t *testing.T) {
	for _, schema := range []string{"2", "0"} {
		host := &recipeHost{}
		body := strings.Replace(twoServiceRecipe, `"schema": 1`, `"schema": `+schema, 1)
		repo := recipeFixture(t, body, host)
		stdout, err := releaseRecipeCmd(t, repo)
		if err == nil || !strings.Contains(err.Error(), "schema") {
			t.Fatalf("schema %s: want a schema refusal, got %v", schema, err)
		}
		parseFail(t, stdout)
		if len(host.seen) != 0 {
			t.Fatalf("schema %s: nothing may run on the VM: %v", schema, host.seen)
		}
	}
	// `stack add --recipe` refuses it as well, and registers nothing.
	host := &recipeHost{}
	body := strings.Replace(twoServiceRecipe, `"schema": 1`, `"schema": 2`, 1)
	repo := recipeFixture(t, body, host)
	_, _, err := runCmdE(t, newStackAddCmd(), nil, true, "other", "--vm", "vm1", "--path", "/opt/other", "--recipe", repo)
	if err == nil || !strings.Contains(err.Error(), "schema 2") {
		t.Fatalf("stack add should refuse schema 2, got %v", err)
	}
	store, _ := fleet.Load()
	if store.FindStack("other") != nil {
		t.Fatal("the stack was registered despite the refusal")
	}
}

func TestStackAddRecipeRegistersTheFileAndItsComposeName(t *testing.T) {
	host := &recipeHost{}
	repo := recipeFixture(t, twoServiceRecipe, host)
	runCmd(t, newStackAddCmd(), nil, true, "other", "--vm", "vm1", "--path", "/opt/other", "--recipe", repo)
	store, _ := fleet.Load()
	st := store.FindStack("other")
	if st == nil || st.RecipeFile != filepath.Join(repo, recipeFileName) || st.File != "docker-compose.recipe.yml" {
		t.Fatalf("unexpected record: %+v", st)
	}
}

// Values from a repository reach the parser as hostile input. Nothing may run on the VM for any of
// them, and none may reach a command.
func TestRecipeReleaseRefusesHostileValues(t *testing.T) {
	hostile := []string{"; rm -rf /", "../"}
	fields := map[string]string{
		"service name": `{"name": "%s", "image": "a:1"}`,
		"image":        `{"name": "ok", "image": "%s"}`,
		"port":         `{"name": "ok", "image": "a:1", "ports": ["%s"]}`,
		"health url":   `{"name": "ok", "image": "a:1", "healthUrl": "%s"}`,
		"build":        `{"name": "ok", "build": "%s"}`,
	}
	for field, tmpl := range fields {
		for _, bad := range hostile {
			host := &recipeHost{}
			body := `{"schema":1,"name":"app","services":[` + strings.Replace(tmpl, "%s", bad, 1) + `]}`
			repo := recipeFixture(t, body, host)
			_, err := releaseRecipeBare(t, repo)
			if err == nil || strings.Contains(err.Error(), "does not declare") {
				t.Fatalf("%s %q was accepted or stopped before validation: %v", field, bad, err)
			}
			if len(host.seen) != 0 {
				t.Fatalf("%s %q reached the VM: %v", field, bad, host.seen)
			}
		}
	}
	for _, bad := range hostile {
		host := &recipeHost{}
		body := `{"schema":1,"name":"app","files":[{"from":"` + bad + `","to":"/srv/x"}]}`
		repo := recipeFixture(t, body, host)
		if _, err := releaseRecipeBare(t, repo); err == nil || len(host.seen) != 0 {
			t.Fatalf("files from %q: err %v, ran %v", bad, err, host.seen)
		}
		body = `{"schema":1,"name":"app","files":[{"from":"dist/","to":"/srv/` + bad + `"}]}`
		repo = recipeFixture(t, body, host)
		if _, err := releaseRecipeBare(t, repo); err == nil || len(host.seen) != 0 {
			t.Fatalf("files to %q: err %v, ran %v", bad, err, host.seen)
		}
	}
}

func TestRecipeReleaseRefusesASecretLiteralAndAMissingInput(t *testing.T) {
	host := &recipeHost{}
	repo := recipeFixture(t, twoServiceRecipe, host)
	_, _, err := runCmdE(t, newStackReleaseCmd(), nil, true, "app", "--recipe", repo, "--input", "DB_PASSWORD=hunter2")
	if err == nil || !strings.Contains(err.Error(), "secret") {
		t.Fatalf("a literal for a secret should be refused: %v", err)
	}
	if err != nil && strings.Contains(err.Error(), "hunter2") {
		t.Fatal("the literal is in the error")
	}
	_, _, err = runCmdE(t, newStackReleaseCmd(), nil, true, "app", "--recipe", repo)
	if err == nil || !strings.Contains(err.Error(), "DB_PASSWORD") {
		t.Fatalf("a missing secret should be named: %v", err)
	}
	_, _, err = runCmdE(t, newStackReleaseCmd(), nil, true, "app", "--recipe", repo,
		"--input", "DB_PASSWORD=cmd:echo hi")
	if err == nil {
		t.Fatal("cmd: must be refused for recipe inputs")
	}
	if len(host.seen) != 0 {
		t.Fatalf("nothing may run on the VM: %v", host.seen)
	}
}

// A stack with no recipe goes down the manifest path exactly as before.
func TestStackWithoutARecipeStillNeedsAManifest(t *testing.T) {
	host := &recipeHost{}
	recipeFixture(t, twoServiceRecipe, host)
	_, _, err := runCmdE(t, newStackReleaseCmd(), nil, true, "app")
	if err == nil || !strings.Contains(err.Error(), "no release manifest") {
		t.Fatalf("want the manifest error, got %v", err)
	}
	if len(host.seen) != 0 {
		t.Fatalf("nothing may run: %v", host.seen)
	}
}

// withDatabase gives the fixture stack a postgres container, so a release has something to back up.
func withDatabase(t *testing.T) {
	t.Helper()
	store, err := fleet.Load()
	if err != nil {
		t.Fatal(err)
	}
	st := store.FindStack("app")
	st.DBContainer, st.DBName, st.DBUser = "pg", "appdb", "app"
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
}

func TestRecipeReleaseBacksUpBeforeAnyComposeCommand(t *testing.T) {
	host := &recipeHost{}
	repo := recipeFixture(t, twoServiceRecipe, host)
	withDatabase(t)

	if _, err := releaseRecipeCmd(t, repo); err != nil {
		t.Fatalf("release failed: %v\n%s", err, strings.Join(host.seen, "\n"))
	}
	dump := host.index("pg_dump", 0)
	if dump < 0 {
		t.Fatalf("no backup ran:\n%s", strings.Join(host.seen, "\n"))
	}
	for i, c := range host.seen[:dump] {
		if strings.Contains(c, "pull") || strings.Contains(c, "base64 -d") || strings.Contains(c, "up -d") {
			t.Fatalf("command %d changed the host before the backup: %s", i, c)
		}
	}
	if host.index("pull", 0) < dump {
		t.Fatal("the pull ran before the backup")
	}
}

func TestRecipeReleaseFailingBackupSendsNoComposeCommand(t *testing.T) {
	host := &recipeHost{failOnce: map[string]string{"pg_dump": "pg_dump: connection refused"}}
	repo := recipeFixture(t, twoServiceRecipe, host)
	withDatabase(t)

	stdout, err := releaseRecipeCmd(t, repo)
	if err == nil || !strings.Contains(err.Error(), "pre-release backup failed") {
		t.Fatalf("want a pre-release backup failure, got %v", err)
	}
	parseFail(t, stdout)
	for _, c := range host.seen {
		if strings.Contains(c, "docker compose") || strings.Contains(c, "base64 -d") {
			t.Fatalf("a deploy command ran after the backup failed: %s", c)
		}
	}
	if len(host.stdin) != 0 {
		t.Fatalf("inputs were written after the backup failed: %q", host.stdin)
	}
}

func TestRecipeReleaseNoBackupSkipsIt(t *testing.T) {
	host := &recipeHost{}
	repo := recipeFixture(t, twoServiceRecipe, host)
	withDatabase(t)

	if _, err := releaseRecipeCmd(t, repo, "--no-backup"); err != nil {
		t.Fatalf("release failed: %v", err)
	}
	if host.index("pg_dump", 0) >= 0 {
		t.Fatal("--no-backup still ran a dump")
	}
	if host.index("up -d --no-deps 'web'", 0) < 0 {
		t.Fatal("the release did not run")
	}
}

func TestRecipeReleaseTakesTheManagedSnapshotFirst(t *testing.T) {
	host := &recipeHost{}
	repo := recipeFixture(t, twoServiceRecipe, host)
	editStack(t, func(st *fleet.Stack) {
		st.Managed = &managed.Spec{Provider: managed.AWS, Profile: "p", Region: "us-east-1", Database: "shop-db"}
	})
	f := &fakeCloud{}
	withFakeCloud(t, f)

	stdout, err := releaseRecipeCmd(t, repo)
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if len(f.seen) != 3 || !strings.Contains(f.seen[2], "wait db-snapshot-available") {
		t.Fatalf("cloud calls: %v", f.seen)
	}
	var env struct {
		Data struct {
			Snapshot string         `json:"managedSnapshot"`
			Cloud    []managed.Call `json:"cloud"`
			Services []string       `json:"services"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatal(err)
	}
	if env.Data.Snapshot != "baryovm-20261030-010203" || len(env.Data.Cloud) != 3 || len(env.Data.Services) != 2 {
		t.Errorf("envelope data: %+v", env.Data)
	}
}

func TestRecipeReleaseStopsOnAFailedSnapshotAndSendsNothing(t *testing.T) {
	host := &recipeHost{}
	repo := recipeFixture(t, twoServiceRecipe, host)
	editStack(t, func(st *fleet.Stack) {
		st.Managed = &managed.Spec{Provider: managed.AWS, Profile: "p", Region: "us-east-1", Database: "shop-db"}
	})
	withFakeCloud(t, &fakeCloud{fail: map[string]int{"wait db-snapshot-available": 255}})

	stdout, err := releaseRecipeCmd(t, repo)
	if err == nil {
		t.Fatal("a snapshot that did not finish must block the release")
	}
	for _, c := range host.seen {
		if strings.Contains(c, "compose") || strings.Contains(c, "mkdir -p '/opt/app'") {
			t.Fatalf("nothing may be sent to the VM after a failed snapshot: %s", c)
		}
	}
	e := parseFail(t, stdout)
	if cloud, _ := e.Data["cloud"].([]any); len(cloud) != 3 {
		t.Errorf("failure envelope should carry the cloud calls: %v", e.Data)
	}
}

func TestRecipeReleaseNoBackupSkipsTheManagedSnapshot(t *testing.T) {
	host := &recipeHost{}
	repo := recipeFixture(t, twoServiceRecipe, host)
	editStack(t, func(st *fleet.Stack) {
		st.Managed = &managed.Spec{Provider: managed.AWS, Profile: "p", Region: "us-east-1", Database: "shop-db"}
	})
	f := &fakeCloud{}
	withFakeCloud(t, f)
	if _, err := releaseRecipeCmd(t, repo, "--no-backup"); err != nil {
		t.Fatal(err)
	}
	if len(f.seen) != 0 {
		t.Fatalf("--no-backup must skip the snapshot: %v", f.seen)
	}
}

func TestRecipeReleaseUsesThePodmanRuntime(t *testing.T) {
	host := &recipeHost{}
	repo := recipeFixture(t, twoServiceRecipe, host)
	editStack(t, func(st *fleet.Stack) { st.Runtime = "podman" })
	if _, err := releaseRecipeCmd(t, repo); err != nil {
		t.Fatal(err)
	}
	for _, c := range host.seen {
		if strings.Contains(c, "docker compose") || strings.Contains(c, "docker ") {
			t.Fatalf("a podman stack ran docker: %s", c)
		}
	}
	if host.index("podman compose", 0) < 0 {
		t.Fatalf("no podman compose call:\n%s", strings.Join(host.seen, "\n"))
	}
}

func TestRecipeReleaseRefusesWhileTheHostLockIsHeld(t *testing.T) {
	host := &recipeHost{busy: true}
	repo := recipeFixture(t, twoServiceRecipe, host)
	_, err := releaseRecipeCmd(t, repo)
	if err == nil || !strings.Contains(err.Error(), "lock") {
		t.Fatalf("want a lock refusal, got %v", err)
	}
	if host.index("compose", 0) >= 0 || host.index("mkdir -p '/opt/app'", 0) >= 0 {
		t.Fatalf("nothing may run while the lock is held:\n%s", strings.Join(host.seen, "\n"))
	}
}

func TestRecipeReleaseDropsTheLockWhenDone(t *testing.T) {
	host := &recipeHost{}
	repo := recipeFixture(t, twoServiceRecipe, host)
	if _, err := releaseRecipeCmd(t, repo); err != nil {
		t.Fatal(err)
	}
	if host.index("rmdir", 0) < 0 {
		t.Fatal("the lock was not released")
	}
}

func TestRecipeReleaseRefusesAReleaseFileAndARecipeTogether(t *testing.T) {
	host := &recipeHost{}
	recipeFixture(t, twoServiceRecipe, host)
	editStack(t, func(st *fleet.Stack) { st.ReleaseFile = "/x/release.json"; st.RecipeFile = "/x/baryovm.recipe.json" })
	_, _, err := runCmdE(t, newStackReleaseCmd(), nil, true, "app")
	if err == nil || !strings.Contains(err.Error(), "both a release file and a recipe") {
		t.Fatalf("want the both-files refusal, got %v", err)
	}
	if len(host.seen) != 0 {
		t.Fatalf("nothing may run: %v", host.seen)
	}
}
