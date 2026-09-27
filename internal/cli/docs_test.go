// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateCommandDocsWritesAndChecks(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "commands")
	if err := GenerateCommandDocs(dest, false); err != nil {
		t.Fatalf("generate command docs: %v", err)
	}
	if err := GenerateCommandDocs(dest, true); err != nil {
		t.Fatalf("check freshly generated command docs: %v", err)
	}

	root, err := os.ReadFile(filepath.Join(dest, "baryovm.md"))
	if err != nil {
		t.Fatalf("read root command doc: %v", err)
	}
	if !strings.Contains(string(root), "baryovm stack") {
		t.Fatalf("root command doc does not list stack: %s", root)
	}
	stackAdd, err := os.ReadFile(filepath.Join(dest, "baryovm_stack_add.md"))
	if err != nil {
		t.Fatalf("read stack add doc: %v", err)
	}
	if !strings.Contains(string(stackAdd), "--path") {
		t.Fatalf("stack add doc does not include its flags: %s", stackAdd)
	}

	if err := os.WriteFile(filepath.Join(dest, "stale.md"), []byte("stale"), 0o644); err != nil {
		t.Fatalf("write stale doc: %v", err)
	}
	if err := GenerateCommandDocs(dest, true); err == nil {
		t.Fatal("checking command docs passed with a stale page")
	}
	if err := GenerateCommandDocs(dest, false); err != nil {
		t.Fatalf("regenerate command docs: %v", err)
	}
	if err := GenerateCommandDocs(dest, true); err != nil {
		t.Fatalf("check regenerated command docs: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "stale.md")); !os.IsNotExist(err) {
		t.Fatalf("stale command doc was not removed: %v", err)
	}
}
