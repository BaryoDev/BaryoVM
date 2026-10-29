// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/BaryoDev/BaryoVM/internal/discover"
	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"github.com/spf13/cobra"
)

const discoverLs = `[{"Name":"barakocms","Status":"running(2)","ConfigFiles":"/opt/barakocms/compose.yaml"},
{"Name":"deploy","Status":"running(3)","ConfigFiles":"/home/opc/app/deploy/docker-compose.yml"},
{"Name":"legacy","Status":"running(1)","ConfigFiles":"/opt/my legacy/prod.yml"}]`

func discoverAnswers() map[string]string {
	return map[string]string{
		"compose ls --format json":                         discoverLs,
		"'/opt/barakocms/compose.yaml' config":             `{"services":{"pg":{"image":"postgres:16","container_name":"barako-postgres"},"app":{"image":"a"}}}`,
		"'/home/opc/app/deploy/docker-compose.yml' config": `{"services":{"a":{"image":"a"},"b":{"image":"b"},"db":{"image":"postgres:15"}}}`,
		"'/opt/my legacy/prod.yml' config":                 `{"services":{"app":{"image":"legacy"}}}`,
	}
}

func discoverFixture(t *testing.T, stacks ...fleet.Stack) {
	t.Helper()
	t.Setenv("BARYOVM_HOME", t.TempDir())
	store := &fleet.Store{
		VMs:    []fleet.VM{{Name: "oracle", Host: "10.0.0.1", User: "opc", KeyPath: "/keys/id"}},
		Stacks: stacks,
	}
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
}

func runDiscover(t *testing.T, f *fakeRunner, jsonMode, tty bool, stdin string, args ...string) (string, string, error) {
	t.Helper()
	prev := stdinIsTerminal
	stdinIsTerminal = func(*cobra.Command) bool { return tty }
	t.Cleanup(func() { stdinIsTerminal = prev })
	cmd := newStackDiscoverCmd()
	cmd.SetIn(strings.NewReader(stdin))
	return runCmdE(t, cmd, f, jsonMode, args...)
}

func parseDiscover(t *testing.T, stdout string) (bool, []discover.Project) {
	t.Helper()
	var env struct {
		OK   bool               `json:"ok"`
		Data []discover.Project `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("not one JSON envelope (%v): %s", err, stdout)
	}
	return env.OK, env.Data
}

func TestDiscoverIsReadOnlyAndEmitsTypedList(t *testing.T) {
	discoverFixture(t)
	f := &fakeRunner{answers: discoverAnswers()}

	stdout, _, err := runDiscover(t, f, true, false, "", "oracle")
	if err != nil {
		t.Fatal(err)
	}
	ok, ps := parseDiscover(t, stdout)
	if !ok || len(ps) != 3 {
		t.Fatalf("ok=%v projects=%+v", ok, ps)
	}
	by := map[string]discover.Project{}
	for _, p := range ps {
		by[p.Name] = p
	}
	b := by["barakocms"]
	if b.Services != 2 || b.PostgresGuess != "barako-postgres" || b.State != "found" {
		t.Errorf("barakocms: %+v", b)
	}
	wantAdd := `baryovm stack add 'barakocms' --vm 'oracle' --path '/opt/barakocms' --db-container 'barako-postgres'`
	if b.AddCommand != wantAdd {
		t.Errorf("got  %s\nwant %s", b.AddCommand, wantAdd)
	}
	if d := by["deploy"]; d.PostgresGuess != "deploy-db-1" || d.Services != 3 {
		t.Errorf("deploy: %+v", d)
	}
	l := by["legacy"]
	if l.PostgresGuess != "" || l.File != "prod.yml" || l.Dir != "/opt/my legacy" {
		t.Errorf("legacy: %+v", l)
	}
	if !strings.Contains(l.AddCommand, `--path '/opt/my legacy' --file 'prod.yml'`) {
		t.Errorf("legacy add command: %s", l.AddCommand)
	}
	for _, c := range f.seen {
		if !strings.Contains(c, "compose ls") && !strings.Contains(c, " config --format json") {
			t.Errorf("a read-only discover ran: %s", c)
		}
		if strings.Contains(c, "sudo") {
			t.Errorf("sudo without --sudo: %s", c)
		}
	}
	store, _ := fleet.Load()
	if len(store.Stacks) != 0 {
		t.Errorf("discover registered something: %+v", store.Stacks)
	}
}

func TestDiscoverSudoWrapsEveryCommand(t *testing.T) {
	discoverFixture(t)
	f := &fakeRunner{answers: discoverAnswers()}
	if _, _, err := runDiscover(t, f, true, false, "", "oracle", "--sudo"); err != nil {
		t.Fatal(err)
	}
	if len(f.seen) != 4 {
		t.Fatalf("seen %v", f.seen)
	}
	for _, c := range f.seen {
		if !strings.HasPrefix(c, "sudo -n docker compose ") {
			t.Errorf("not elevated: %s", c)
		}
	}
}

func TestDiscoverEmptyList(t *testing.T) {
	discoverFixture(t)
	f := &fakeRunner{answers: map[string]string{"compose ls": "[]\n"}}
	stdout, _, err := runDiscover(t, f, true, false, "", "oracle")
	if err != nil {
		t.Fatal(err)
	}
	ok, ps := parseDiscover(t, stdout)
	if !ok || ps == nil || len(ps) != 0 {
		t.Errorf("ok=%v data=%v", ok, ps)
	}
	if !strings.Contains(stdout, `"data": []`) {
		t.Errorf("an empty list should be [], not missing: %s", stdout)
	}
}

func TestDiscoverMalformedLsFailsWithOneEnvelope(t *testing.T) {
	discoverFixture(t)
	f := &fakeRunner{answers: map[string]string{"compose ls": "this is not json"}}
	stdout, _, err := runDiscover(t, f, true, false, "", "oracle")
	if err == nil {
		t.Fatal("malformed output should fail")
	}
	e := parseFailure(t, stdout, "stack discover")
	if e.Error == "" {
		t.Error("no error text")
	}
	if code, silent := exitCodeOf(err); code != 1 || !silent {
		t.Errorf("exit %d silent %v", code, silent)
	}
}

func TestDiscoverKeepsAProjectWhoseConfigIsUnreadable(t *testing.T) {
	discoverFixture(t)
	a := discoverAnswers()
	a["'/opt/barakocms/compose.yaml' config"] = "garbage"
	stdout, _, err := runDiscover(t, &fakeRunner{answers: a}, true, false, "", "oracle")
	if err != nil {
		t.Fatal(err)
	}
	_, ps := parseDiscover(t, stdout)
	for _, p := range ps {
		if p.Name == "barakocms" && (!strings.Contains(p.Note, "config not read") || p.PostgresGuess != "") {
			t.Errorf("%+v", p)
		}
	}
	if len(ps) != 3 {
		t.Errorf("the other projects were lost: %+v", ps)
	}
}

func TestDiscoverUnknownVM(t *testing.T) {
	discoverFixture(t)
	if _, _, err := runDiscover(t, &fakeRunner{}, true, false, "", "nope"); err == nil {
		t.Fatal("unknown VM accepted")
	}
}

func TestAdoptNeedsYesWithoutATerminal(t *testing.T) {
	for name, jsonMode := range map[string]bool{"json": true, "no tty": false} {
		discoverFixture(t)
		f := &fakeRunner{answers: discoverAnswers()}
		_, _, err := runDiscover(t, f, jsonMode, false, "", "oracle", "--adopt")
		if err == nil || !strings.Contains(err.Error(), "--yes") {
			t.Errorf("%s: %v", name, err)
		}
		if len(f.seen) != 0 {
			t.Errorf("%s: connected before refusing: %v", name, f.seen)
		}
	}
}

func TestAdoptYesRegistersThroughStackAdd(t *testing.T) {
	discoverFixture(t)
	f := &fakeRunner{answers: discoverAnswers()}
	stdout, _, err := runDiscover(t, f, true, false, "", "oracle", "--adopt", "--yes", "--sudo")
	if err != nil {
		t.Fatal(err)
	}
	_, ps := parseDiscover(t, stdout)
	for _, p := range ps {
		if p.State != "registered" {
			t.Errorf("%s: %s", p.Name, p.State)
		}
	}
	store, _ := fleet.Load()
	if len(store.Stacks) != 3 {
		t.Fatalf("stacks: %+v", store.Stacks)
	}
	st := store.FindStack("barakocms")
	if st == nil || st.VM != "oracle" || st.Dir != "/opt/barakocms" || st.DBContainer != "barako-postgres" || !st.Sudo {
		t.Errorf("barakocms: %+v", st)
	}
	if l := store.FindStack("legacy"); l == nil || l.File != "prod.yml" || l.Dir != "/opt/my legacy" || l.DBContainer != "" {
		t.Errorf("legacy: %+v", l)
	}
}

func TestAdoptSkipsRegisteredProjectsAndSaysSo(t *testing.T) {
	discoverFixture(t,
		fleet.Stack{Name: "mine", VM: "oracle", Dir: "/opt/barakocms/", DBContainer: "keep-me"},
		fleet.Stack{Name: "deploy", VM: "oracle", Dir: "/somewhere/else"})
	f := &fakeRunner{answers: discoverAnswers()}
	stdout, _, err := runDiscover(t, f, true, false, "", "oracle", "--adopt", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	_, ps := parseDiscover(t, stdout)
	state := map[string]discover.Project{}
	for _, p := range ps {
		state[p.Name] = p
	}
	if p := state["barakocms"]; p.State != "already-registered" || !strings.Contains(p.Note, "mine") {
		t.Errorf("barakocms: %+v", p)
	}
	if p := state["deploy"]; p.State != "skipped" || !strings.Contains(p.Note, "/somewhere/else") {
		t.Errorf("deploy: %+v", p)
	}
	if state["legacy"].State != "registered" {
		t.Errorf("legacy: %+v", state["legacy"])
	}
	store, _ := fleet.Load()
	if st := store.FindStack("mine"); st == nil || st.DBContainer != "keep-me" {
		t.Errorf("an existing registration was changed: %+v", st)
	}
	if st := store.FindStack("deploy"); st.Dir != "/somewhere/else" {
		t.Errorf("a stack with the same name was overwritten: %+v", st)
	}
}

func TestAdoptPromptsPerProjectOnATerminal(t *testing.T) {
	discoverFixture(t)
	f := &fakeRunner{answers: discoverAnswers()}
	// projects come in directory order: /home/opc/app/deploy, /opt/barakocms, /opt/my legacy
	_, _, err := runDiscover(t, f, false, true, "y\nn\nyes\n", "oracle", "--adopt")
	if err != nil {
		t.Fatal(err)
	}
	store, _ := fleet.Load()
	if store.FindStack("deploy") == nil || store.FindStack("legacy") == nil {
		t.Errorf("accepted projects missing: %+v", store.Stacks)
	}
	if store.FindStack("barakocms") != nil {
		t.Errorf("a declined project was registered: %+v", store.Stacks)
	}
}

func TestAdoptSplitProjectIsNotRegistered(t *testing.T) {
	discoverFixture(t)
	a := map[string]string{
		"compose ls": `[{"Name":"split","Status":"running(1)","ConfigFiles":"/opt/split/compose.yaml,/opt/split/o.yaml"}]`,
		"-f '/opt/split/compose.yaml' -f '/opt/split/o.yaml' config": `{"services":{"a":{"image":"a"}}}`,
	}
	stdout, _, err := runDiscover(t, &fakeRunner{answers: a}, true, false, "", "oracle", "--adopt", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	_, ps := parseDiscover(t, stdout)
	if len(ps) != 1 || ps[0].State != "skipped" || ps[0].AddCommand != "" {
		t.Errorf("%+v", ps)
	}
	if store, _ := fleet.Load(); len(store.Stacks) != 0 {
		t.Errorf("registered a project stack add cannot express: %+v", store.Stacks)
	}
}

func TestYesWithoutAdoptIsRefused(t *testing.T) {
	discoverFixture(t)
	if _, _, err := runDiscover(t, &fakeRunner{}, true, false, "", "oracle", "--yes"); err == nil {
		t.Fatal("--yes alone accepted")
	}
}
