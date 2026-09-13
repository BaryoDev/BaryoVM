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
// out at compose up, after the backup, the rsync and a full image build had already run.
func TestReleaseRefusesAHandRunContainerNameBeforeDoingAnyWork(t *testing.T) {
	releaseFixture(t)
	f := &fakeRunner{answers: releaseAnswers(handRunDemo)}

	err := releaseAgainst(t, f)
	if err == nil {
		t.Fatalf("release went ahead over a name a hand-run container holds. It ran:\n%s", strings.Join(f.seen, "\n"))
	}
	if !strings.Contains(err.Error(), `"umbraco-pwa-demo"`) || !strings.Contains(err.Error(), "docker rm -f 'umbraco-pwa-demo'") {
		t.Errorf("the error should name the container and the command that frees the name: %v", err)
	}
	if len(f.seen) != 2 {
		t.Errorf("want only the config read and the container list, got:\n%s", strings.Join(f.seen, "\n"))
	}
	for _, cmd := range f.seen {
		for _, work := range []string{"pg_dump", "local: rsync", "docker build", "up -d", "docker rm"} {
			if strings.Contains(cmd, work) {
				t.Errorf("%q ran although the name was taken: %s", work, cmd)
			}
		}
	}
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
