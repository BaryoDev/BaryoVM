// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package compose

import (
	"strings"
	"testing"
)

// A podman stack must build podman for every command the package owns, and none may fall back to
// docker. Each call site had its own literal before the runtime existed.
func TestAPodmanStackBuildsPodmanCommands(t *testing.T) {
	s := Stack{Dir: "/opt/app", File: "c.yml", Runtime: "podman"}
	cmds := map[string]string{
		"up":      s.UpCmd(UpOptions{Services: []string{"api"}, ForceRecreate: true}),
		"pull":    s.PullCmd(nil),
		"ps":      s.PsJSONCmd(),
		"psq":     s.PsQuietCmd(nil),
		"config":  s.ConfigCmd(),
		"names":   s.NamesCmd(),
		"mounts":  s.InspectMountsCmd("c"),
		"info":    s.InspectInfoCmd("c"),
		"remove":  s.RemoveCmd("c"),
		"podSudo": Stack{Dir: "/opt/app", Runtime: "podman", Sudo: true}.PsQuietCmd(nil),
	}
	for name, got := range cmds {
		if !strings.Contains(got, "podman") {
			t.Errorf("%s does not use podman: %s", name, got)
		}
		if strings.Contains(got, "docker") && !strings.Contains(got, "com.docker.compose.project") {
			t.Errorf("%s still names docker: %s", name, got)
		}
	}
	if got := cmds["up"]; !strings.Contains(got, "podman compose -f 'c.yml' up -d --force-recreate 'api'") {
		t.Errorf("up: %s", got)
	}
	if got := cmds["podSudo"]; !strings.HasPrefix(got, "sudo -n ") || !strings.Contains(got, "podman compose ps -q") {
		t.Errorf("sudo podman: %s", got)
	}
}

func TestPodmanLogsAndRollbackCommandsGoThroughPodman(t *testing.T) {
	s := Stack{Dir: "/opt/app", Runtime: "podman"}
	r := &seq{}
	if _, err := Logs(r, s, nil, 50); err != nil {
		t.Fatal(err)
	}
	if _, err := Retag(r, s, "sha256:abc", "app:1"); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectImage(r, s, "sha256:abc"); err != nil {
		t.Fatal(err)
	}
	want := []string{"podman compose logs --no-color --tail 50", "podman tag 'sha256:abc' 'app:1'", "podman image inspect --format"}
	for i, w := range want {
		if i >= len(r.seen) || !strings.Contains(r.seen[i], w) {
			t.Errorf("command %d: want %q in %v", i, w, r.seen)
		}
	}
}

func TestTheDefaultAndDockerStacksStayOnDocker(t *testing.T) {
	for _, rt := range []string{"", "docker"} {
		got := Stack{Dir: "/opt/app", Runtime: rt}.UpCmd(UpOptions{})
		if got != "cd '/opt/app' && docker compose up -d" {
			t.Errorf("runtime %q: %s", rt, got)
		}
	}
}

// A runtime that reached a Stack without validation (a hand-edited file) must not become a shell
// fragment, and must not quietly run docker either.
func TestAnUnknownRuntimeNeverReachesTheShell(t *testing.T) {
	got := Stack{Dir: "/opt/app", Runtime: "docker; reboot"}.UpCmd(UpOptions{})
	if strings.Contains(got, "reboot") || strings.Contains(got, "docker compose") {
		t.Errorf("unsafe or silently-docker command: %s", got)
	}
}

func TestNameConflictFixUsesTheStacksRuntime(t *testing.T) {
	e := &NameConflictError{Conflicts: []NameConflict{{Name: "web"}}, Runtime: "podman"}
	if msg := e.Error(); !strings.Contains(msg, "podman rm -f 'web'") {
		t.Errorf("the suggested fix names the wrong runtime: %s", msg)
	}
}

type seq struct{ seen []string }

func (s *seq) Run(cmd string) (string, error) {
	s.seen = append(s.seen, cmd)
	return "", nil
}
