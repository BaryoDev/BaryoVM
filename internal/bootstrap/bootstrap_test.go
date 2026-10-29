// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package bootstrap

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Run `go test ./internal/bootstrap -update` to rewrite the golden file, then read the diff.
var update = flag.Bool("update", false, "rewrite golden files")

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with -update to create it): %v", err)
	}
	if string(want) != got {
		t.Errorf("%s differs from %s\n--- want\n%s\n--- got\n%s", name, path, want, got)
	}
}

func mustScript(t *testing.T, runtime string) string {
	t.Helper()
	s, err := Script(runtime)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestDockerScriptGolden(t *testing.T) {
	golden(t, "docker_script", mustScript(t, "docker"))
	if mustScript(t, "") != mustScript(t, "docker") {
		t.Error("the empty runtime must be docker")
	}
}

func TestPodmanScriptGolden(t *testing.T) {
	golden(t, "podman_script", mustScript(t, "podman"))
}

func TestScriptDetectsEveryPackageManager(t *testing.T) {
	for _, rt := range []string{"docker", "podman"} {
		s := mustScript(t, rt)
		for _, pm := range []string{"apt-get", "dnf", "yum", "apk", "pacman"} {
			if !strings.Contains(s, "command -v "+pm) {
				t.Errorf("%s script does not detect %s", rt, pm)
			}
		}
		if !strings.Contains(s, "no supported package manager found") {
			t.Errorf("%s script has no message for an unknown package manager", rt)
		}
	}
}

func TestScriptSkipsSystemctlWithoutSystemdAndSaysSo(t *testing.T) {
	for _, rt := range []string{"docker", "podman"} {
		s := mustScript(t, rt)
		guard := strings.Index(s, "if [ -d /run/systemd/system ]; then")
		ctl := strings.Index(s, "systemctl enable --now")
		skip := strings.Index(s, `echo "NO_SYSTEMD:`)
		if guard < 0 || ctl < guard || skip < ctl {
			t.Errorf("%s: systemctl must sit inside the systemd check, with a NO_SYSTEMD message on the other branch:\n%s", rt, s)
		}
		for _, l := range strings.Split(s, "\n") {
			if strings.Contains(l, "systemctl enable") && (strings.Contains(l, "|| true") || strings.Contains(l, "/dev/null")) {
				t.Errorf("%s: systemctl failure is swallowed: %s", rt, l)
			}
		}
	}
}

func TestDockerKeepsGetDockerOnlyWhereItWorks(t *testing.T) {
	s := mustScript(t, "docker")
	if !strings.Contains(s, "apt-get|dnf|yum)\n      curl -fsSL https://get.docker.com") {
		t.Errorf("get.docker.com must serve apt-get, dnf and yum:\n%s", s)
	}
	if !strings.Contains(s, "apk add --no-cache docker") || !strings.Contains(s, "pacman -Sy --noconfirm --needed docker") {
		t.Errorf("apk and pacman must install the distro docker package:\n%s", s)
	}
}

func TestPodmanInstallsTheDistroPackageAndNotDocker(t *testing.T) {
	s := mustScript(t, "podman")
	for _, want := range []string{"apt-get install -y podman", `"$PM" install -y podman`, "apk add --no-cache podman", "pacman -Sy --noconfirm --needed podman", "podman --version"} {
		if !strings.Contains(s, want) {
			t.Errorf("podman script lacks %q", want)
		}
	}
	if strings.Contains(s, "get.docker.com") || strings.Contains(s, "usermod") {
		t.Errorf("podman script touches docker:\n%s", s)
	}
}

func TestScriptRefusesAnUnknownRuntime(t *testing.T) {
	for _, bad := range []string{"nerdctl", "docker; reboot", "Podman"} {
		if s, err := Script(bad); err == nil || s != "" {
			t.Errorf("Script(%q) = %q, %v", bad, s, err)
		}
	}
}

func TestParseReadsPackageManagerAndMissingSystemd(t *testing.T) {
	r := parse("podman", "PACKAGE_MANAGER=apk\nNO_SYSTEMD: skipped systemctl, x\npodman version 5.0.0\n")
	if r.PackageManager != "apk" || !r.NoSystemd || len(r.Notes) != 1 || r.DockerVersion != "podman version 5.0.0" || r.AlreadyReady {
		t.Errorf("%+v", r)
	}
	r = parse("docker", "ALREADY_INSTALLED\nDocker version 27\n")
	if !r.AlreadyReady || r.NoSystemd || r.DockerVersion != "Docker version 27" {
		t.Errorf("%+v", r)
	}
}

// A systemd host whose podman package ships no podman.socket unit must not stop the script under
// set -e. The enable is guarded by a unit check and a missing unit prints a note.
func TestPodmanSocketEnableIsGuardedByAUnitCheck(t *testing.T) {
	s := mustScript(t, "podman")
	if !strings.Contains(s, "systemctl cat podman.socket") || !strings.Contains(s, "NO_SOCKET:") {
		t.Errorf("the socket enable is not guarded:\n%s", s)
	}
	if strings.Contains(mustScript(t, "docker"), "podman.socket") {
		t.Error("the docker script must not mention the podman socket")
	}
	r := parse("podman", "NO_SOCKET: podman.socket is not installed\npodman version 4.9\n")
	if len(r.Notes) != 1 || r.NoSystemd || r.DockerVersion != "podman version 4.9" {
		t.Errorf("report: %+v", r)
	}
}

// pacman needs a database sync before a package install, and no prompt.
func TestPacmanInstallSyncsAndDoesNotPrompt(t *testing.T) {
	for _, rt := range []string{"docker", "podman"} {
		s := mustScript(t, rt)
		if !strings.Contains(s, "pacman -Sy --noconfirm --needed "+rt) {
			t.Errorf("%s: pacman install lacks -Sy --noconfirm:\n%s", rt, s)
		}
	}
}
