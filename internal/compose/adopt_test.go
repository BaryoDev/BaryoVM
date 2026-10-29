// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package compose

import (
	"errors"
	"strings"
	"testing"
)

func TestAdoptCommandsQuoteTheNameAndHonourSudo(t *testing.T) {
	plain := Stack{Dir: "/opt/app"}
	root := Stack{Dir: "/opt/app", Sudo: true}
	name := "web; rm -rf /"

	cases := []struct {
		got, want string
	}{
		{plain.InspectMountsCmd("demo"), "docker inspect --format '{{json .Mounts}}' 'demo'"},
		{plain.InspectInfoCmd("demo"), "docker inspect --format '{{.Config.Image}} {{.Created}}' 'demo'"},
		{plain.RemoveCmd("demo"), "docker rm -f 'demo'"},
		{root.RemoveCmd("demo"), "sudo -n docker rm -f 'demo'"},
		{root.InspectMountsCmd("demo"), "sudo -n docker inspect --format '{{json .Mounts}}' 'demo'"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
	if strings.Contains(plain.RemoveCmd(name), "-v") {
		t.Errorf("removal must not pass -v: %s", plain.RemoveCmd(name))
	}
	if !strings.HasSuffix(plain.RemoveCmd(name), "'web; rm -rf /'") {
		t.Errorf("the name must be single quoted: %s", plain.RemoveCmd(name))
	}
}

type adoptRunner struct {
	answers map[string]string
	seen    []string
}

func (r *adoptRunner) Run(cmd string) (string, error) {
	r.seen = append(r.seen, cmd)
	for k, v := range r.answers {
		if strings.Contains(cmd, k) {
			return v, nil
		}
	}
	return "", nil
}

func TestPlanAdoptionRefusesWithoutRemovingAnything(t *testing.T) {
	r := &adoptRunner{answers: map[string]string{
		"config --format json": `{"volumes":{"data":{"name":"proj_data"}}}`,
		"json .Mounts":         `[{"Type":"volume","Name":"proj_data"}]`,
		"Config.Image":         "img:1 2026-10-01T00:00:00Z",
	}}
	_, err := PlanAdoption(r, Stack{Dir: "/opt/app"}, []string{"demo"})
	var refused *AdoptRefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("want *AdoptRefusedError, got %v", err)
	}
	if len(refused.Refusals) != 1 || refused.Refusals[0].Volume != "proj_data" {
		t.Errorf("refusals = %+v", refused.Refusals)
	}
	if !strings.Contains(err.Error(), "volumes:\n  proj_data:\n    external: true") {
		t.Errorf("the YAML snippet is missing: %v", err)
	}
	for _, c := range r.seen {
		if strings.Contains(c, "rm -f") {
			t.Errorf("planning must not remove anything: %s", c)
		}
	}
}

func TestPlanAdoptionReadsImageAndCreated(t *testing.T) {
	r := &adoptRunner{answers: map[string]string{
		"config --format json": `{}`,
		"json .Mounts":         `[]`,
		"Config.Image":         "img:1 2026-10-01T00:00:00Z\n",
	}}
	plan, err := PlanAdoption(r, Stack{Dir: "/opt/app"}, []string{"demo"})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 1 || plan[0].Name != "demo" || plan[0].Image != "img:1" || plan[0].Created != "2026-10-01T00:00:00Z" {
		t.Errorf("plan = %+v", plan)
	}
}

func TestExternalFalseDoesNotCountAsExternal(t *testing.T) {
	ext, err := externalVolumes(`{"volumes":{"a":{"external":false},"b":{"external":true}}}`)
	if err != nil {
		t.Fatal(err)
	}
	if ext["a"] || !ext["b"] {
		t.Errorf("ext = %v", ext)
	}
}
