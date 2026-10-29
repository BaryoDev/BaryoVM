// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/BaryoDev/BaryoVM/internal/fleet"
)

// The volume from #7: the hand-run demo keeps its data in a named volume.
const dataVolume = "umbraco-pwa-demo-data"

const namedVolumeMounts = `[{"Type":"volume","Name":"umbraco-pwa-demo-data","Destination":"/data"}]`

const demoInfo = "umbraco-pwa-demo:arm64 2026-10-01T08:00:00Z"

// demoConfigExternal is demoConfig with the volume declared external, as the refusal asks.
const demoConfigExternal = `{"name": "umbraco-pwa", "services": {"demo": {"image": "umbraco-pwa-demo:arm64", "container_name": "umbraco-pwa-demo"}},
 "volumes": {"umbraco-pwa-demo-data": {"name": "umbraco-pwa-demo-data", "external": true}}}`

// demoConfigInternal declares the same volume without external, so compose would create its own.
const demoConfigInternal = `{"name": "umbraco-pwa", "services": {"demo": {"image": "umbraco-pwa-demo:arm64", "container_name": "umbraco-pwa-demo"}},
 "volumes": {"umbraco-pwa-demo-data": {"name": "umbraco-pwa_umbraco-pwa-demo-data"}}}`

func adoptAnswers(config, mounts string) map[string]string {
	a := releaseAnswers(handRunDemo)
	a["config --format json"] = config
	a["json .Mounts"] = mounts
	a["Config.Image"] = demoInfo
	a["rm -f"] = ""
	return a
}

// releaseWith is releaseAgainst with extra arguments, so a test can pass --adopt. It returns the
// stdout of the command as well, for the JSON envelope.
func releaseWith(t *testing.T, f *fakeRunner, args ...string) (string, error) {
	t.Helper()
	withLookPath(t, hasRsync)
	prevDial, prevLocal := dialRelease, runLocal
	dialRelease = func(*fleet.VM) (remoteSession, error) { return fakeSession{f}, nil }
	runLocal = func(c *exec.Cmd) (string, error) {
		f.seen = append(f.seen, "local: "+strings.Join(c.Args, " "))
		return "", nil
	}
	t.Cleanup(func() { dialRelease, runLocal = prevDial, prevLocal })

	stdout, _, err := runCmdE(t, newStackReleaseCmd(), f, true, append([]string{"demo"}, args...)...)
	return stdout, err
}

type adoptFailure struct {
	OK     bool   `json:"ok"`
	Action string `json:"action"`
	Error  string `json:"error"`
}

func parseFailure(t *testing.T, stdout, action string) adoptFailure {
	t.Helper()
	var e adoptFailure
	if err := json.Unmarshal([]byte(stdout), &e); err != nil {
		t.Fatalf("envelope is not JSON (%v): %s", err, stdout)
	}
	if e.OK {
		t.Errorf("a refusal must not report ok: %s", stdout)
	}
	if e.Action != action {
		t.Errorf("action = %q, want %q", e.Action, action)
	}
	return e
}

func neverRemoved(t *testing.T, seen []string) {
	t.Helper()
	for _, cmd := range seen {
		if strings.Contains(cmd, "docker rm -f") {
			t.Errorf("a container was removed although adoption was refused: %s", cmd)
		}
	}
}

func TestReleaseAdoptRefusesANamedVolumeThatIsNotExternal(t *testing.T) {
	releaseFixture(t)
	f := &fakeRunner{answers: adoptAnswers(demoConfigInternal, namedVolumeMounts)}

	stdout, err := releaseWith(t, f, "--adopt")
	if err == nil {
		t.Fatalf("adopt went ahead over a named volume. It ran:\n%s", strings.Join(f.seen, "\n"))
	}
	wantYAML := "volumes:\n  " + dataVolume + ":\n    external: true"
	if !strings.Contains(err.Error(), wantYAML) || !strings.Contains(err.Error(), `"`+dataVolume+`"`) {
		t.Errorf("the refusal should name the volume and print the YAML:\n%v", err)
	}
	neverRemoved(t, f.seen)
	if indexOfCmd(f.seen, "json .Mounts") < 0 {
		t.Errorf("the mounts were never read:\n%s", strings.Join(f.seen, "\n"))
	}
	for _, cmd := range f.seen {
		if strings.Contains(cmd, "docker build") || strings.Contains(cmd, "up -d") {
			t.Errorf("ran although adoption was refused: %s", cmd)
		}
	}
	e := parseFailure(t, stdout, "stack release")
	if !strings.Contains(e.Error, wantYAML) {
		t.Errorf("the JSON envelope should carry the YAML, got: %q", e.Error)
	}
}

func TestDeployAdoptRefusesANamedVolumeThatIsNotExternal(t *testing.T) {
	stackFixture(t)
	f := &fakeRunner{answers: map[string]string{
		"config --format json":       demoConfigInternal,
		"com.docker.compose.project": handRunDemo,
		"json .Mounts":               namedVolumeMounts,
		"Config.Image":               demoInfo,
		"rm -f":                      "",
		"up -d":                      "",
	}}

	stdout, _, err := runCmdE(t, newStackDeployCmd(), f, true, "app", "--adopt")
	if err == nil {
		t.Fatalf("deploy --adopt went ahead over a named volume. It ran:\n%s", strings.Join(f.seen, "\n"))
	}
	neverRemoved(t, f.seen)
	for _, cmd := range f.seen {
		if strings.Contains(cmd, "up -d") {
			t.Errorf("compose up ran although adoption was refused: %s", cmd)
		}
	}
	e := parseFailure(t, stdout, "stack deploy")
	if !strings.Contains(e.Error, "volumes:\n  "+dataVolume+":\n    external: true") {
		t.Errorf("the JSON envelope should carry the YAML, got: %q", e.Error)
	}
}

func TestReleaseAdoptRemovesTheContainerWhenTheVolumeIsExternal(t *testing.T) {
	releaseFixture(t)
	f := &fakeRunner{answers: adoptAnswers(demoConfigExternal, namedVolumeMounts)}

	if _, err := releaseWith(t, f, "--adopt"); err != nil {
		t.Fatalf("adopt refused an external volume: %v\nIt ran:\n%s", err, strings.Join(f.seen, "\n"))
	}
	assertAdoptOrder(t, f.seen)
}

func TestReleaseAdoptRemovesTheContainerWhenItHasNoVolumes(t *testing.T) {
	releaseFixture(t)
	f := &fakeRunner{answers: adoptAnswers(demoConfig, "[]")}

	if _, err := releaseWith(t, f, "--adopt"); err != nil {
		t.Fatalf("adopt refused a container with no volumes: %v\nIt ran:\n%s", err, strings.Join(f.seen, "\n"))
	}
	assertAdoptOrder(t, f.seen)
}

func TestReleaseAdoptIgnoresBindMountsAndAnonymousVolumes(t *testing.T) {
	releaseFixture(t)
	anon := strings.Repeat("ab", 32)
	mounts := `[{"Type":"bind","Source":"/srv/data","Destination":"/data"},{"Type":"volume","Name":"` + anon + `","Destination":"/tmp"}]`
	f := &fakeRunner{answers: adoptAnswers(demoConfig, mounts)}

	if _, err := releaseWith(t, f, "--adopt"); err != nil {
		t.Fatalf("adopt refused mounts that have no declarable name: %v", err)
	}
	assertAdoptOrder(t, f.seen)
}

// assertAdoptOrder checks the whole adoption sequence: the mounts are read before anything is
// removed, the removal does not use -v, and the build and compose up come after it.
func assertAdoptOrder(t *testing.T, seen []string) {
	t.Helper()
	dump := strings.Join(seen, "\n")
	mounts := indexOfCmd(seen, "inspect --format '{{json .Mounts}}' 'umbraco-pwa-demo'")
	info := indexOfCmd(seen, "inspect --format '{{.Config.Image}} {{.Created}}' 'umbraco-pwa-demo'")
	rm := indexOfCmd(seen, "docker rm -f 'umbraco-pwa-demo'")
	build := indexOfCmd(seen, "docker build")
	up := indexOfCmd(seen, "up -d")
	sync := indexOfCmd(seen, "local: rsync")
	if mounts < 0 || info < 0 || rm < 0 || build < 0 || up < 0 || sync < 0 {
		t.Fatalf("a step is missing (mounts %d, info %d, rm %d, build %d, up %d, sync %d):\n%s", mounts, info, rm, build, up, sync, dump)
	}
	if sync >= mounts || mounts >= rm || info >= rm || rm >= build || build >= up {
		t.Errorf("wrong order, want sync, inspect, rm, build, up:\n%s", dump)
	}
	if strings.Contains(seen[rm], "rm -f -v") || strings.Contains(seen[rm], " -v ") {
		t.Errorf("removal must keep the volumes: %s", seen[rm])
	}
}

func TestReleaseWithoutAdoptIsUnchanged(t *testing.T) {
	releaseFixture(t)
	f := &fakeRunner{answers: adoptAnswers(demoConfigExternal, namedVolumeMounts)}

	err := releaseAgainst(t, f)
	if err == nil {
		t.Fatalf("release went ahead over a hand-run container without --adopt:\n%s", strings.Join(f.seen, "\n"))
	}
	if !strings.Contains(err.Error(), "docker rm -f 'umbraco-pwa-demo'") {
		t.Errorf("the original error should still give the command that frees the name: %v", err)
	}
	if strings.Contains(err.Error(), "refusing to adopt") {
		t.Errorf("the adopt refusal showed up without --adopt: %v", err)
	}
	for _, cmd := range f.seen {
		if strings.Contains(cmd, "inspect") || strings.Contains(cmd, "docker rm -f") {
			t.Errorf("nothing should be inspected or removed without --adopt: %s", cmd)
		}
	}
}

func TestDeployWithoutAdoptIsUnchanged(t *testing.T) {
	stackFixture(t)
	f := &fakeRunner{answers: map[string]string{
		"config --format json":       demoConfigExternal,
		"com.docker.compose.project": handRunDemo,
		"json .Mounts":               namedVolumeMounts,
		"up -d":                      "",
	}}

	if _, _, err := runCmdE(t, newStackDeployCmd(), f, true, "app"); err == nil {
		t.Fatalf("deploy went ahead without --adopt:\n%s", strings.Join(f.seen, "\n"))
	}
	for _, cmd := range f.seen {
		if strings.Contains(cmd, "inspect") || strings.Contains(cmd, "docker rm -f") || strings.Contains(cmd, "up -d") {
			t.Errorf("ran although the name was taken: %s", cmd)
		}
	}
}

func TestDeployAdoptRemovesAndBringsTheStackUp(t *testing.T) {
	stackFixture(t)
	f := &fakeRunner{answers: map[string]string{
		"config --format json":       demoConfigExternal,
		"com.docker.compose.project": handRunDemo,
		"json .Mounts":               namedVolumeMounts,
		"Config.Image":               demoInfo,
		"rm -f":                      "",
		"up -d":                      "",
	}}

	if _, _, err := runCmdE(t, newStackDeployCmd(), f, true, "app", "--adopt"); err != nil {
		t.Fatalf("deploy --adopt failed: %v\nIt ran:\n%s", err, strings.Join(f.seen, "\n"))
	}
	rm := indexOfCmd(f.seen, "docker rm -f 'umbraco-pwa-demo'")
	up := indexOfCmd(f.seen, "up -d")
	mounts := indexOfCmd(f.seen, "json .Mounts")
	if mounts < 0 || rm < 0 || up < 0 || mounts >= rm || rm >= up {
		t.Errorf("want inspect, rm, up in that order:\n%s", strings.Join(f.seen, "\n"))
	}
}

// A container that belongs to another compose project is not a hand-run container, and --adopt is
// only for those.
func TestAdoptNeverRemovesAnotherProjectsContainer(t *testing.T) {
	releaseFixture(t)
	other := "umbraco-pwa-demo\tsome-other-project\n"
	a := adoptAnswers(demoConfig, "[]")
	a["com.docker.compose.project"] = other
	f := &fakeRunner{answers: a}

	if _, err := releaseWith(t, f, "--adopt"); err == nil {
		t.Fatalf("adopt removed a container another compose project owns:\n%s", strings.Join(f.seen, "\n"))
	}
	neverRemoved(t, f.seen)
}
