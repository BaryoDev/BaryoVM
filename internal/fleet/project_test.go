// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package fleet

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func readGolden(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The point of #91: one vocabulary. A project file and a fleet.json holding the same machines and
// stacks parse into the very same VM and Stack structs, so a field added to either reaches both.
func TestProjectFileAndFleetJSONParseIntoTheSameStructs(t *testing.T) {
	var fl Store
	if err := json.Unmarshal(readGolden(t, "shared.fleet.json"), &fl); err != nil {
		t.Fatalf("fleet.json: %v", err)
	}
	p, err := ParseProject(readGolden(t, "shared.project.json"))
	if err != nil {
		t.Fatalf("project file: %v", err)
	}
	if !reflect.DeepEqual(fl.VMs, p.VMs) {
		t.Errorf("vms differ:\nfleet:   %+v\nproject: %+v", fl.VMs, p.VMs)
	}
	if !reflect.DeepEqual(fl.Stacks, p.Stacks) {
		t.Errorf("stacks differ:\nfleet:   %+v\nproject: %+v", fl.Stacks, p.Stacks)
	}
	if p.Project != "rckoronadal" || !reflect.DeepEqual(p.Order, []string{"api", "site"}) {
		t.Errorf("project and order not read: %+v", p)
	}
	if p.VMs[0].HostKey == "" || p.Stacks[0].ReleaseFile != "api.release.json" || !p.Stacks[0].Sudo {
		t.Errorf("typed fields dropped: %+v", p)
	}
}

// Same promise as the 0.4.0 fleet.json test: a binary that predates a field must not delete it.
func TestProjectUnknownFieldsRoundTrip(t *testing.T) {
	newer := `{
	  "project": "p",
	  "channel": "stable",
	  "vms": [{"name": "v", "host": "h", "user": "u", "keyPath": "env:BARYOVM_K", "region": "ap-southeast-2"}],
	  "stacks": [{"name": "s", "vm": "v", "dir": "/opt/s", "releaseFile": "s.json", "verify": {"http": "/up"}}],
	  "order": ["s"]
	}`
	p, err := ParseProject([]byte(newer))
	if err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"channel":"stable"`, `"region":"ap-southeast-2"`, `"verify":{"http":"/up"}`, `"order":["s"]`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("want %s kept, got:\n%s", want, out)
		}
	}
	// The preserved bytes are compacted by the first write, so compare what a second write produces.
	again, err := ParseProject(out)
	if err != nil {
		t.Fatal(err)
	}
	out2, _ := json.Marshal(again)
	if string(out) != string(out2) {
		t.Errorf("a second round trip changed the file:\n%s\n%s", out, out2)
	}
}

func projectWith(key string) string {
	return `{"project":"p","vms":[{"name":"v","host":"h","user":"u","keyPath":` + key + `}],` +
		`"stacks":[{"name":"s","vm":"v","dir":"/opt/s","releaseFile":"s.json"}]}`
}

func TestProjectRefusesALiteralWhereAReferenceIsExpected(t *testing.T) {
	cases := map[string]struct{ json, field string }{
		"private key": {projectWith(`"-----BEGIN OPENSSH ` + "PRIVATE " + "KEY" + `-----\nabc\n-----END OPENSSH ` + "PRIVATE " + "KEY" + `-----"`), "vms[0].keyPath"},
		"bare token":  {projectWith(`"` + ghToken + `"`), "vms[0].keyPath"},
		"bad env ref": {projectWith(`"env:not valid"`), "vms[0].keyPath"},
		"empty":       {projectWith(`""`), "vms[0].keyPath"},
		"password field": {`{"project":"p","vms":[{"name":"v","host":"h","user":"u","keyPath":"env:BARYOVM_K","password":"hunter2"}],` +
			`"stacks":[]}`, "vms[0].password"},
		"token in a stack field": {`{"project":"p","vms":[{"name":"v","host":"h","user":"u","keyPath":"env:BARYOVM_K"}],` +
			`"stacks":[{"name":"s","vm":"v","dir":"/opt/s","releaseFile":"s.json","healthUrl":"https://x/?k=` + ghToken + `"}]}`, "stacks[0].healthUrl"},
		"private key in a hostKey path":      {`{"project":"p","vms":[],"stacks":[],"notes":{"hostKey":"-----BEGIN OPENSSH ` + "PRIVATE " + `KEY-----\nabc"}}`, "notes.hostKey"},
		"private key behind a host key line": {projectWithHostKey(`"ssh-ed25519 AAAA\n-----BEGIN OPENSSH ` + "PRIVATE " + `KEY-----\nabc"`), "vms[0].hostKey"},
		"newline in a host key":              {projectWithHostKey(`"ssh-ed25519 AAAA\nnot a key"`), "vms[0].hostKey"},
		"too many host key fields":           {projectWithHostKey(`"ssh-ed25519 AAAA comment extra"`), "vms[0].hostKey"},
		"token in a host key comment":        {projectWithHostKey(`"ssh-ed25519 AAAA ` + ghToken + `"`), "vms[0].hostKey"},
		"child of a secret-named container":  {`{"project":"p","vms":[],"stacks":[],"credentials":{"pass":"y"}}`, "credentials.pass"},
		"unknown field":                      {`{"project":"p","deployToken":"abc","vms":[],"stacks":[]}`, "deployToken"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseProject([]byte(c.json))
			if err == nil {
				t.Fatal("a literal secret was accepted")
			}
			if !strings.Contains(err.Error(), c.field) {
				t.Errorf("the error should name %s, got: %v", c.field, err)
			}
			for _, leak := range []string{"hunter2", "abcdefghijklmnop", "OPENSSH"} {
				if strings.Contains(err.Error(), leak) {
					t.Errorf("the error repeats the secret: %v", err)
				}
			}
		})
	}
}

func TestProjectAcceptsReferences(t *testing.T) {
	for _, key := range []string{`"env:BARYOVM_DEPLOY_KEY_PATH"`, `"~/.ssh/deploy"`, `"/home/runner/.ssh/id"`, `".keys/deploy"`} {
		if _, err := ParseProject([]byte(projectWith(key))); err != nil {
			t.Errorf("%s: %v", key, err)
		}
	}
	// A secret-named field may hold a reference.
	in := `{"project":"p","vms":[],"stacks":[],"registryToken":"env:REG"}`
	if _, err := ParseProject([]byte(in)); err != nil {
		t.Errorf("an env reference in a secret-named field is fine: %v", err)
	}
}

func TestProjectValidation(t *testing.T) {
	base := func(mut func(m map[string]any)) string {
		m := map[string]any{
			"project": "p",
			"vms":     []any{map[string]any{"name": "v", "host": "h", "user": "u", "keyPath": "env:BARYOVM_K"}},
			"stacks":  []any{map[string]any{"name": "s", "vm": "v", "dir": "/opt/s", "releaseFile": "s.json"}},
		}
		mut(m)
		b, _ := json.Marshal(m)
		return string(b)
	}
	cases := map[string]struct {
		mut  func(m map[string]any)
		want string
	}{
		"no project name":  {func(m map[string]any) { m["project"] = "" }, "project"},
		"unknown vm":       {func(m map[string]any) { m["stacks"].([]any)[0].(map[string]any)["vm"] = "x" }, "stacks[0].vm"},
		"relative dir":     {func(m map[string]any) { m["stacks"].([]any)[0].(map[string]any)["dir"] = "opt" }, "stacks[0].dir"},
		"absolute release": {func(m map[string]any) { m["stacks"].([]any)[0].(map[string]any)["releaseFile"] = "/Users/me/r.json" }, "stacks[0].releaseFile"},
		"order unknown":    {func(m map[string]any) { m["order"] = []any{"nope"} }, "order[0]"},
		"order twice":      {func(m map[string]any) { m["order"] = []any{"s", "s"} }, "order[1]"},
		"no release file":  {func(m map[string]any) { delete(m["stacks"].([]any)[0].(map[string]any), "releaseFile") }, "releaseFile"},
		"bad runtime":      {func(m map[string]any) { m["stacks"].([]any)[0].(map[string]any)["runtime"] = "rkt" }, "stacks[0].runtime"},
		"bad managed": {func(m map[string]any) {
			m["stacks"].([]any)[0].(map[string]any)["managed"] = map[string]any{"provider": "aws", "region": "us-east-1", "database": "db;ls"}
		}, "stacks[0].managed"},
		"bad host key": {func(m map[string]any) { m["vms"].([]any)[0].(map[string]any)["hostKey"] = "nope" }, "vms[0].hostKey"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseProject([]byte(base(c.mut)))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("want an error naming %q, got %v", c.want, err)
			}
		})
	}
	if _, err := ParseProject([]byte(base(func(map[string]any) {}))); err != nil {
		t.Errorf("the base file should be valid: %v", err)
	}
	good := base(func(m map[string]any) {
		st := m["stacks"].([]any)[0].(map[string]any)
		st["runtime"] = "podman"
		st["managed"] = map[string]any{"provider": "aws", "region": "us-east-1", "database": "shop-db"}
	})
	if _, err := ParseProject([]byte(good)); err != nil {
		t.Errorf("podman with a managed database should be valid: %v", err)
	}
}

func TestProjectStoreResolvesPathsAgainstTheFileDirectory(t *testing.T) {
	p, err := ParseProject(readGolden(t, "shared.project.json"))
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"BARYOVM_DEPLOY_KEY_PATH": "/run/secrets/key"}
	s, err := p.Store("/repo/deploy", func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Stacks[0].ReleaseFile; got != filepath.Join("/repo/deploy", "api.release.json") {
		t.Errorf("releaseFile = %q", got)
	}
	if s.VMs[0].KeyPath != "/run/secrets/key" {
		t.Errorf("keyPath = %q", s.VMs[0].KeyPath)
	}
	if _, err := p.Store("/repo/deploy", func(string) string { return "" }); err == nil || !strings.Contains(err.Error(), "BARYOVM_DEPLOY_KEY_PATH") {
		t.Errorf("an unset key variable should be named, got %v", err)
	}
}

var ghToken = "gh" + "p_" + strings.Repeat("a", 36)

func projectWithHostKey(hk string) string {
	return `{"project":"p","vms":[{"name":"v","host":"h","user":"u","keyPath":"env:BARYOVM_K","hostKey":` + hk + `}],` +
		`"stacks":[{"name":"s","vm":"v","dir":"/opt/s","releaseFile":"s.json"}]}`
}

func TestProjectAcceptsAPublicHostKeyThatLooksLikeAToken(t *testing.T) {
	in := projectWithHostKey(`"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGhwX0FBQUFBQUFBQUFBQUFBQUFBQUFBQUFB ci@runner"`)
	if _, err := ParseProject([]byte(in)); err != nil {
		t.Errorf("a normal host key line must pass: %v", err)
	}
}
