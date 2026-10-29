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
)

// fakeSession is a fakeRunner a release can hang up on.
type fakeSession struct{ *fakeRunner }

func (fakeSession) Close() error { return nil }

// The compose file from #1: its container_name matches the container that had been started by hand.
const demoConfig = `{"name": "umbraco-pwa", "services": {"demo": {"image": "umbraco-pwa-demo:arm64", "container_name": "umbraco-pwa-demo"}}}`

// What `docker ps -a` said on that VM: the demo holds the name and carries no compose project label.
const handRunDemo = "umbraco-pwa-demo\t\n"

// The same name held by this project's own container, beside a renamed leftover of the kind #57
// removes. Neither is somebody else's container.
const ownedDemo = "umbraco-pwa-demo\tumbraco-pwa\nb92d86916493_umbraco-pwa-demo\tumbraco-pwa\n"

// releaseFixture registers the stack from #1 with a database, so a release that does not stop early
// runs every step: backup, rsync, build, compose up.
func releaseFixture(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("BARYOVM_HOME", home)

	src := filepath.Join(home, "src")
	if err := os.MkdirAll(filepath.Join(src, "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest, err := json.Marshal(map[string]any{
		"localRoot":  src,
		"remoteRoot": "/home/opc/umbraco-pwa-src",
		"sync":       []string{"web"},
		"builds": []map[string]string{
			{"image": "umbraco-pwa-demo:arm64", "dockerfile": "Dockerfile", "context": "web"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(home, "release.json")
	if err := os.WriteFile(manifestPath, manifest, 0o600); err != nil {
		t.Fatal(err)
	}

	store := &fleet.Store{
		VMs: []fleet.VM{{Name: "vm1", Host: "10.0.0.1", User: "opc", KeyPath: "/keys/id"}},
		Stacks: []fleet.Stack{{
			Name: "demo", VM: "vm1", Dir: "/home/opc/umbraco-pwa",
			DBContainer: "umbraco-pwa-db", DBName: "umbraco", ReleaseFile: manifestPath,
		}},
	}
	if err := store.Save(); err != nil {
		t.Fatalf("seed fleet: %v", err)
	}
}

// releaseAgainst runs `stack release demo` with every remote command and the rsync recorded, in
// order, in f.seen.
func releaseAgainst(t *testing.T, f *fakeRunner) error {
	t.Helper()
	withLookPath(t, hasRsync)
	prevDial, prevLocal := dialRelease, runLocal
	dialRelease = func(*fleet.VM) (remoteSession, error) { return fakeSession{f}, nil }
	runLocal = func(c *exec.Cmd) (string, error) {
		f.seen = append(f.seen, "local: "+strings.Join(c.Args, " "))
		return "", nil
	}
	t.Cleanup(func() { dialRelease, runLocal = prevDial, prevLocal })

	_, _, err := runCmdE(t, newStackReleaseCmd(), f, true, "demo")
	return err
}

func releaseAnswers(containers string) map[string]string {
	return map[string]string{
		"config --format json":       demoConfig,
		"com.docker.compose.project": containers,
		"pg_dump":                    "",
		"docker build":               "",
		"up -d":                      "",
	}
}

// The reported failure: a container started with `docker run` held the name, and the release found
// out at compose up, after the image build had already run. The check now sits after the sync (the
// compose file is on the VM by then, #150) and must still come before the build and compose up.
func TestReleaseRefusesAHandRunContainerNameBeforeBuildAndUp(t *testing.T) {
	releaseFixture(t)
	f := &fakeRunner{answers: releaseAnswers(handRunDemo)}

	err := releaseAgainst(t, f)
	if err == nil {
		t.Fatalf("release went ahead over a name a hand-run container holds. It ran:\n%s", strings.Join(f.seen, "\n"))
	}
	if !strings.Contains(err.Error(), `"umbraco-pwa-demo"`) || !strings.Contains(err.Error(), "docker rm -f 'umbraco-pwa-demo'") {
		t.Errorf("the error should name the container and the command that frees the name: %v", err)
	}
	rsync := indexOfCmd(f.seen, "local: rsync")
	config := indexOfCmd(f.seen, "config --format json")
	names := indexOfCmd(f.seen, "com.docker.compose.project")
	if rsync < 0 || config < 0 || names < 0 {
		t.Fatalf("want the sync, the config read and the container list to run, got:\n%s", strings.Join(f.seen, "\n"))
	}
	if rsync >= config || config >= names {
		t.Errorf("the name check must come after the sync, got:\n%s", strings.Join(f.seen, "\n"))
	}
	for _, cmd := range f.seen {
		for _, work := range []string{"docker build", "up -d", "docker rm"} {
			if strings.Contains(cmd, work) {
				t.Errorf("%q ran although the name was taken: %s", work, cmd)
			}
		}
	}
}

// A first release has no compose file on the VM until the sync lands it. The name check reads that
// file, so it has to run after the sync and still refuse a colliding name before anything is built.
func TestFirstReleaseWithACollidingNameIsRefusedAfterTheSync(t *testing.T) {
	releaseFixture(t)
	f := &fakeRunner{answers: releaseAnswers(handRunDemo)}
	f.answers["--format '{{.Names}}'"] = "" // the database container does not exist yet

	err := releaseAgainst(t, f)
	if err == nil {
		t.Fatalf("a first release went ahead over a colliding name. It ran:\n%s", strings.Join(f.seen, "\n"))
	}
	if indexOfCmd(f.seen, "local: rsync") < 0 {
		t.Errorf("the sync should have run before the check, got:\n%s", strings.Join(f.seen, "\n"))
	}
	if indexOfCmd(f.seen, "pg_dump") >= 0 {
		t.Errorf("there is no database to dump on a first release:\n%s", strings.Join(f.seen, "\n"))
	}
	for _, cmd := range f.seen {
		if strings.Contains(cmd, "docker build") || strings.Contains(cmd, "up -d") {
			t.Errorf("ran although the name was taken: %s", cmd)
		}
	}
}

// indexOfCmd is the position of the first recorded command containing sub, or -1.
func indexOfCmd(seen []string, sub string) int {
	for i, c := range seen {
		if strings.Contains(c, sub) {
			return i
		}
	}
	return -1
}

// The other half: a name held by this project's own container is what a second release looks like,
// and it must go all the way through.
func TestReleaseGoesAheadWhenTheNameBelongsToThisProject(t *testing.T) {
	releaseFixture(t)
	f := &fakeRunner{answers: releaseAnswers(ownedDemo)}

	if err := releaseAgainst(t, f); err != nil {
		t.Fatalf("release refused a name its own project holds: %v\nIt ran:\n%s", err, strings.Join(f.seen, "\n"))
	}
	if len(f.seen) == 0 {
		t.Fatal("no commands ran, so this proves nothing")
	}
	if last := f.seen[len(f.seen)-1]; !strings.Contains(last, "up -d") {
		t.Errorf("the release should end in compose up, ended in: %s", last)
	}
	for _, cmd := range f.seen {
		if strings.Contains(cmd, "docker rm") {
			t.Errorf("nothing needed removing, but the release ran: %s", cmd)
		}
	}
}

func TestDeployRefusesAHandRunContainerNameBeforePulling(t *testing.T) {
	stackFixture(t)
	f := &fakeRunner{answers: map[string]string{
		"config --format json":       demoConfig,
		"com.docker.compose.project": handRunDemo,
		" pull":                      "",
		"up -d":                      "",
	}}

	_, _, err := runCmdE(t, newStackDeployCmd(), f, true, "app", "--pull")
	if err == nil {
		t.Fatalf("deploy went ahead over a name a hand-run container holds. It ran:\n%s", strings.Join(f.seen, "\n"))
	}
	if !strings.Contains(err.Error(), "docker rm -f 'umbraco-pwa-demo'") {
		t.Errorf("the error should give the command that frees the name: %v", err)
	}
	for _, cmd := range f.seen {
		if strings.Contains(cmd, " pull") || strings.Contains(cmd, "up -d") || strings.Contains(cmd, "docker rm") {
			t.Errorf("ran although the name was taken: %s", cmd)
		}
	}
}

func TestDeployGoesAheadWhenTheNameBelongsToThisProject(t *testing.T) {
	stackFixture(t)
	f := &fakeRunner{answers: map[string]string{
		"config --format json":       demoConfig,
		"com.docker.compose.project": ownedDemo,
		"up -d":                      "recreated\n",
	}}

	runCmd(t, newStackDeployCmd(), f, true, "app")

	if len(f.seen) == 0 {
		t.Fatal("no commands ran, so this proves nothing")
	}
	if last := f.seen[len(f.seen)-1]; !strings.Contains(last, "up -d") {
		t.Errorf("deploy should end in compose up, ended in: %s", last)
	}
}
