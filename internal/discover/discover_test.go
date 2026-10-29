// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package discover

import (
	"strings"
	"testing"
)

const lsJSON = `[{"Name":"deploy","Status":"running(3)","ConfigFiles":"/home/opc/app/deploy/docker-compose.yml"},
{"Name":"barakocms","Status":"running(2)","ConfigFiles":"/opt/barakocms/compose.yaml"},
{"Name":"spaced","Status":"running(1)","ConfigFiles":"/srv/my apps/it's/prod.yml"},
{"Name":"split","Status":"running(1)","ConfigFiles":"/opt/split/compose.yaml,/opt/split/override.yaml"}]`

func TestLsCmdIsExact(t *testing.T) {
	if got, want := LsCmd(false), "docker compose ls --format json"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
	if got, want := LsCmd(true), "sudo -n docker compose ls --format json"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestConfigCmdQuotesEveryFile(t *testing.T) {
	got := ConfigCmd([]string{"/srv/my apps/it's/prod.yml", "/srv/o.yml"}, true)
	want := `sudo -n docker compose -f '/srv/my apps/it'\''s/prod.yml' -f '/srv/o.yml' config --format json`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if got := ConfigCmd([]string{"/opt/a/compose.yaml"}, false); got != "docker compose -f '/opt/a/compose.yaml' config --format json" {
		t.Errorf("got %s", got)
	}
}

func TestParseLs(t *testing.T) {
	ps, err := ParseLs(lsJSON)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 4 {
		t.Fatalf("got %d projects: %+v", len(ps), ps)
	}
	// sorted by directory: /home/opc/app/deploy comes before /opt/barakocms
	if ps[0].Name != "deploy" || ps[1].Name != "barakocms" {
		t.Errorf("order: %s, %s", ps[0].Name, ps[1].Name)
	}
	if ps[0].Dir != "/home/opc/app/deploy" || ps[0].File != "" {
		t.Errorf("default file name should not be recorded: %+v", ps[0])
	}
	var spaced, split Project
	for _, p := range ps {
		switch p.Name {
		case "spaced":
			spaced = p
		case "split":
			split = p
		}
	}
	if spaced.Dir != "/srv/my apps/it's" || spaced.File != "prod.yml" {
		t.Errorf("path with spaces: %+v", spaced)
	}
	if len(split.ConfigFiles) != 2 || split.Note == "" {
		t.Errorf("several config files should be noted: %+v", split)
	}
}

func TestParseLsEmpty(t *testing.T) {
	for _, in := range []string{"", "  \n", "[]", "[]\n"} {
		ps, err := ParseLs(in)
		if err != nil || len(ps) != 0 {
			t.Errorf("%q: %v %v", in, ps, err)
		}
	}
}

func TestParseLsMalformedDoesNotEchoOutput(t *testing.T) {
	for _, in := range []string{"not json SECRET=hunter2", `{"Name":"x"}`, `[{"Name":`} {
		_, err := ParseLs(in)
		if err == nil {
			t.Errorf("%q parsed", in)
			continue
		}
		if strings.Contains(err.Error(), "hunter2") {
			t.Errorf("error echoes output: %v", err)
		}
	}
}

func TestParseLsDropsProjectsWithoutConfigFiles(t *testing.T) {
	ps, err := ParseLs(`[{"Name":"gone","Status":"exited(1)","ConfigFiles":""}]`)
	if err != nil || len(ps) != 0 {
		t.Errorf("%v %v", ps, err)
	}
}

func TestInspectFindsPostgresByImage(t *testing.T) {
	p := Project{Name: "deploy"}
	err := Inspect(&p, `{"services":{"api":{"image":"ghcr.io/x/api:1","environment":{"DB_PASSWORD":"s3cret"}},
"db":{"image":"docker.io/library/postgres:16"},"web":{"image":"nginx"}}}`)
	if err != nil {
		t.Fatal(err)
	}
	if p.Services != 3 || p.PostgresGuess != "deploy-db-1" {
		t.Errorf("%+v", p)
	}
}

func TestInspectPrefersContainerName(t *testing.T) {
	p := Project{Name: "barakocms"}
	_ = Inspect(&p, `{"services":{"pg":{"image":"postgres:16","container_name":"barako-postgres"},"app":{"image":"a"}}}`)
	if p.PostgresGuess != "barako-postgres" || p.Services != 2 {
		t.Errorf("%+v", p)
	}
}

func TestInspectNoPostgres(t *testing.T) {
	p := Project{Name: "legacy"}
	if err := Inspect(&p, `{"services":{"app":{"image":"legacy:1"}}}`); err != nil {
		t.Fatal(err)
	}
	if p.Services != 1 || p.PostgresGuess != "" {
		t.Errorf("%+v", p)
	}
}

func TestInspectMalformedDoesNotEchoOutput(t *testing.T) {
	err := Inspect(&Project{}, `DB_PASSWORD=s3cret not json`)
	if err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Errorf("%v", err)
	}
}

func TestAddCommandIsExactAndQuoted(t *testing.T) {
	p := Project{Name: "barako", Dir: "/opt/barakocms", PostgresGuess: "barako-postgres"}
	want := `baryovm stack add 'barako' --vm 'oracle' --path '/opt/barakocms' --db-container 'barako-postgres'`
	if got := AddCommand(p, "oracle", false); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	p = Project{Name: "spaced", Dir: "/srv/my apps/it's", File: "prod.yml"}
	want = `baryovm stack add 'spaced' --vm 'my vm' --path '/srv/my apps/it'\''s' --file 'prod.yml' --sudo`
	if got := AddCommand(p, "my vm", true); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestParseLsSkipsControlCharacters(t *testing.T) {
	cases := map[string]string{
		"dir esc":  `[{"Name":"a","ConfigFiles":"/opt/\u001b[2J/compose.yaml"}]`,
		"name nl":  `[{"Name":"a\nb","ConfigFiles":"/opt/a/compose.yaml"}]`,
		"del":      `[{"Name":"a","ConfigFiles":"/opt/\u007f/compose.yaml"}]`,
		"c1":       `[{"Name":"a","ConfigFiles":"/opt/\u009b/compose.yaml"}]`,
		"file":     `[{"Name":"a","ConfigFiles":"/opt/a/x\u0007.yml"}]`,
		"bad utf8": "[{\"Name\":\"a\",\"ConfigFiles\":\"/opt/\xff/compose.yaml\"}]",
	}
	for name, in := range cases {
		ps, err := ParseLs(in)
		if err != nil || len(ps) != 1 {
			t.Fatalf("%s: %v %v", name, ps, err)
		}
		if ps[0].State != "skipped" || ps[0].AddCommand != "" || strings.ContainsAny(ps[0].Note, "\x1b\x07\n\x7f") {
			t.Errorf("%s: %+v", name, ps[0])
		}
	}
}

func TestInspectSkipsControlCharacterInPostgresGuess(t *testing.T) {
	p := Project{Name: "a"}
	_ = Inspect(&p, `{"services":{"pg":{"image":"postgres","container_name":"x\u001by"}}}`)
	if p.State != "skipped" || !strings.Contains(p.Note, "postgres container") {
		t.Errorf("%+v", p)
	}
}
