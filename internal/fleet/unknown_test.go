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

// The scenario this file exists for: a newer BaryoVM writes a field, an older one reads the file and
// saves it for an unrelated reason, and the field is gone. Not on the field's own command, which is
// why nobody would connect the two.
func TestAFieldThisBinaryDoesNotKnowSurvivesASave(t *testing.T) {
	newer := `{
	  "stacks": [{
	    "name": "app",
	    "vm": "prod",
	    "dir": "/opt/app",
	    "recipe": "./barako.stack.json",
	    "inputs": {"DB_PASSWORD": {"from": "env", "name": "PW"}}
	  }]
	}`

	var s Store
	if err := json.Unmarshal([]byte(newer), &s); err != nil {
		t.Fatalf("load: %v", err)
	}
	out, err := json.Marshal(&s)
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	got := string(out)
	for _, want := range []string{`"recipe":"./barako.stack.json"`, `"DB_PASSWORD"`, `"from":"env"`} {
		if !strings.Contains(got, want) {
			t.Errorf("want %s preserved, got:\n%s", want, got)
		}
	}
	// The known fields are still there too.
	if !strings.Contains(got, `"name":"app"`) || !strings.Contains(got, `"dir":"/opt/app"`) {
		t.Errorf("the typed fields should survive unchanged:\n%s", got)
	}
}

func TestUnknownVMFieldSurvives(t *testing.T) {
	newer := `{"vms":[{"name":"prod","host":"10.0.0.1","user":"ubuntu","keyPath":"~/.ssh/id","provider":"ssh","region":"ap-southeast-2"}]}`
	var s Store
	if err := json.Unmarshal([]byte(newer), &s); err != nil {
		t.Fatalf("load: %v", err)
	}
	out, _ := json.Marshal(&s)
	if !strings.Contains(string(out), `"region":"ap-southeast-2"`) {
		t.Errorf("an unknown VM field should survive:\n%s", out)
	}
}

// The trap that made the first version of this wrong: deriving the known-field list from a
// marshalled VALUE omits every empty omitempty field, so `port` on a VM with no port would be
// captured as unknown and merged back on save. An omitted-because-empty field becoming a preserved
// stale one is worse than the bug being fixed.
func TestAnEmptyOmitemptyFieldIsNotTreatedAsUnknown(t *testing.T) {
	// port is absent from the input and is an omitempty field on VM.
	in := `{"vms":[{"name":"prod","host":"10.0.0.1","user":"ubuntu","keyPath":"~/.ssh/id","provider":"ssh"}]}`
	var s Store
	if err := json.Unmarshal([]byte(in), &s); err != nil {
		t.Fatalf("load: %v", err)
	}
	if s.VMs[0].rest != nil {
		t.Fatalf("nothing here is unknown, got %v", s.VMs[0].rest)
	}

	// Set the port the normal way and save: it must appear once, from the typed field.
	s.VMs[0].Port = 2222
	out, _ := json.Marshal(&s)
	if n := strings.Count(string(out), `"port"`); n != 1 {
		t.Errorf("want one port key, got %d:\n%s", n, out)
	}
	if !strings.Contains(string(out), `"port":2222`) {
		t.Errorf("want the typed value:\n%s", out)
	}
}

// A preserved copy of a key the struct now owns is a leftover from an older file. Letting it win
// would resurrect a stale value on every save.
func TestATypedFieldBeatsAPreservedOne(t *testing.T) {
	var st Stack
	if err := json.Unmarshal([]byte(`{"name":"app","vm":"prod"}`), &st); err != nil {
		t.Fatalf("load: %v", err)
	}
	// Force the situation: a preserved key that the struct also has a field for.
	st.rest = unknownFields{"name": json.RawMessage(`"stale"`)}
	st.Name = "current"

	out, err := json.Marshal(st)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if !strings.Contains(string(out), `"name":"current"`) {
		t.Errorf("the typed field must win:\n%s", out)
	}
	if strings.Contains(string(out), "stale") {
		t.Errorf("a stale preserved value must not resurrect:\n%s", out)
	}
}

func TestNothingUnknownAddsNothing(t *testing.T) {
	in := `{"name":"app","vm":"prod","dir":"/opt/app"}`
	var st Stack
	if err := json.Unmarshal([]byte(in), &st); err != nil {
		t.Fatalf("load: %v", err)
	}
	if st.rest != nil {
		t.Errorf("no unknown fields here, got %v", st.rest)
	}
}

// Round tripping twice must not accumulate or lose anything, since Save runs on every command that
// changes the fleet.
func TestRepeatedSavesAreStable(t *testing.T) {
	in := `{"stacks":[{"name":"app","vm":"prod","recipe":"./r.json"}]}`
	cur := in
	for i := 0; i < 3; i++ {
		var s Store
		if err := json.Unmarshal([]byte(cur), &s); err != nil {
			t.Fatalf("round %d load: %v", i, err)
		}
		b, err := json.Marshal(&s)
		if err != nil {
			t.Fatalf("round %d save: %v", i, err)
		}
		cur = string(b)
		if !strings.Contains(cur, `"recipe":"./r.json"`) {
			t.Fatalf("round %d lost the field:\n%s", i, cur)
		}
	}
}

func TestKnownKeysReadsEveryTaggedField(t *testing.T) {
	got := knownKeys(reflect.TypeOf(struct {
		A string `json:"a"`
		B int    `json:"b,omitempty"`
		C bool   `json:"-"`
		D string
	}{}))
	for _, want := range []string{"a", "b", "D"} {
		if !got[want] {
			t.Errorf("want %q in the known set, got %v", want, got)
		}
	}
	if got["C"] || got["-"] {
		t.Errorf(`a json:"-" field is not a key: %v`, got)
	}
}

// Save writes the whole file, so a malformed entry must not silently become an empty one.
func TestMalformedEntryIsAnError(t *testing.T) {
	var st Stack
	if err := json.Unmarshal([]byte(`{"name":["not","a","string"]}`), &st); err == nil {
		t.Fatal("a type mismatch must still be an error")
	}
}

// Through the real Load and Save, not just the marshallers, because Save is what writes the file
// and a helper passing in isolation proves less than the path a command takes.
func TestLoadAndSavePreserveThroughTheRealFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BARYOVM_HOME", dir)

	newer := `{
	  "vms": [{"name":"prod","host":"10.0.0.1","user":"ubuntu","keyPath":"~/.ssh/id","provider":"ssh"}],
	  "stacks": [{"name":"app","vm":"prod","dir":"/opt/app","recipe":"./barako.stack.json"}]
	}`
	if err := os.WriteFile(filepath.Join(dir, "fleet.json"), []byte(newer), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	// Change something unrelated, the way `stack add` would.
	s.Stacks = append(s.Stacks, Stack{Name: "other", VM: "prod", Dir: "/opt/other"})
	if err := s.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(dir, "fleet.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"recipe": "./barako.stack.json"`) {
		t.Errorf("registering an unrelated stack deleted a field it did not know:\n%s", b)
	}
	if !strings.Contains(string(b), `"name": "other"`) {
		t.Errorf("the new stack should be there too:\n%s", b)
	}
}
