// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package recipe

import (
	"errors"
	"strings"
	"testing"

	"github.com/BaryoDev/BaryoVM/internal/sshx"
)

func TestValidateRefusesHostileValues(t *testing.T) {
	for _, bad := range []string{"; rm -rf /", "../", "a b", "$(id)", "`id`", "a\nb"} {
		cases := map[string]string{
			"service": `{"schema":1,"name":"x","services":[{"name":"%s","image":"a"}]}`,
			"image":   `{"schema":1,"name":"x","services":[{"name":"a","image":"%s"}]}`,
			"port":    `{"schema":1,"name":"x","services":[{"name":"a","image":"a","ports":["%s"]}]}`,
			"from":    `{"schema":1,"name":"x","files":[{"from":"%s","to":"/b"}]}`,
			"to":      `{"schema":1,"name":"x","files":[{"from":"a","to":"/%s"}]}`,
			"host":    `{"schema":1,"name":"x","files":[{"from":"a","to":"/b"}],"routes":[{"host":"%s","port":80}]}`,
			"name":    `{"schema":1,"name":"%s","files":[{"from":"a","to":"/b"}]}`,
		}
		for field, tmpl := range cases {
			esc := strings.NewReplacer("\n", `\n`, `"`, `\"`, "\\", `\\`).Replace(bad)
			if _, err := Parse([]byte(strings.Replace(tmpl, "%s", esc, 1)), "r.json"); err == nil {
				t.Errorf("%s %q was accepted", field, bad)
			}
		}
	}
}

func TestCommandsQuoteEveryValue(t *testing.T) {
	in := DeployInput{Dir: "/opt/x; rm -rf /tmp", File: "f.yml"}
	dir := sshx.Quote("/opt/x; rm -rf /tmp")
	if !strings.Contains(in.PrepareCmd(), dir) {
		t.Errorf("prepare does not quote the directory: %s", in.PrepareCmd())
	}
	live := sshx.Quote("/opt/x; rm -rf /tmp/f.yml")
	for name, cmd := range map[string]string{
		"snapshot": in.SnapshotCmd(),
		"restore":  in.RestoreCmd(),
		"drop":     in.DropCmd(),
	} {
		if !strings.Contains(cmd, live) {
			t.Errorf("%s: the path is not quoted as one word: %s", name, cmd)
		}
	}
	if w := in.WriteFileCmd("a; rm -rf /", "x"); !strings.Contains(w, sshx.Quote("/opt/x; rm -rf /tmp/a; rm -rf /")) {
		t.Errorf("write: the path is not quoted as one word: %s", w)
	}
}

func TestComposeYAMLQuotesValuesAndNeedsAnImage(t *testing.T) {
	r := &Recipe{Schema: 1, Name: "x", Services: []Service{{Name: "a", Image: "img:1", Ports: []string{"80:80"}}},
		Inputs: []Input{{Name: "A", Default: "1"}}}
	y, err := r.ComposeYAML()
	if err != nil || !strings.Contains(y, `image: "img:1"`) || !strings.Contains(y, `env_file:`) {
		t.Fatalf("unexpected yaml %q, %v", y, err)
	}
	r.Services[0].Image, r.Services[0].Build = "", "."
	if _, err := r.ComposeYAML(); err == nil {
		t.Fatal("a build service is not deployable yet and must say so")
	}
}

// stubRunner records commands and stdin and fails the one containing failOn.
type stubRunner struct {
	seen   []string
	stdin  []string
	failOn string
}

func (s *stubRunner) Run(cmd string) (string, error) {
	s.seen = append(s.seen, cmd)
	if s.failOn != "" && strings.Contains(cmd, s.failOn) {
		return "", errors.New("registry unreachable")
	}
	return "none\n", nil
}

func (s *stubRunner) RunInput(cmd, stdin string) (string, error) {
	s.seen = append(s.seen, cmd)
	s.stdin = append(s.stdin, stdin)
	return "", nil
}

func TestAFailedPullWritesNoEnvValue(t *testing.T) {
	r := &Recipe{Schema: 1, Name: "x", Services: []Service{{Name: "a", Image: "img:1"}},
		Inputs: []Input{{Name: "SITE", Default: "http://x"}}}
	c := &stubRunner{failOn: " pull "}
	_, err := Deploy(c, DeployInput{Recipe: r, Dir: "/opt/x", Env: map[string]string{"SITE": "http://x"}})
	if err == nil || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("want the pull refusal, got %v", err)
	}
	if len(c.stdin) != 0 {
		t.Fatalf("a failed pull must not write .env: %q", c.stdin)
	}
}

func TestPodmanRuntimeReachesTheComposeCalls(t *testing.T) {
	r := &Recipe{Schema: 1, Name: "x", Services: []Service{{Name: "a", Image: "img:1"}}}
	c := &stubRunner{}
	if _, err := Deploy(c, DeployInput{Recipe: r, Dir: "/opt/x", Runtime: "podman"}); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range c.seen {
		if strings.Contains(cmd, "docker compose") {
			t.Fatalf("docker used on a podman stack: %s", cmd)
		}
	}
}
