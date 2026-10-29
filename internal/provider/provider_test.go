// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package provider

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

// Run `go test ./internal/provider -update` to rewrite the golden files, then read the diff.
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

// These types are emitted in the JSON envelope, so their field names are a public contract.
func TestInstanceJSONGolden(t *testing.T) {
	b, err := json.Marshal(Instance{ID: "i-123", Name: "web", PublicIP: "203.0.113.7", User: "ubuntu", Provider: "lightsail"})
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "instance", string(b)+"\n")
}

func TestSpecJSONGolden(t *testing.T) {
	b, err := json.Marshal(Spec{Name: "web", Region: "us-east-1", Size: "nano_3_0", PublicKey: "ssh-ed25519 AAAA test"})
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "spec", string(b)+"\n")
}
