// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package recipe

import (
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
