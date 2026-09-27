// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra/doc"
)

// GenerateCommandDocs writes the Cobra command tree to dest, or checks that dest
// matches the current command definitions when check is true.
func GenerateCommandDocs(dest string, check bool) error {
	tmp, err := os.MkdirTemp("", "baryovm-command-docs-")
	if err != nil {
		return fmt.Errorf("create temporary command docs directory: %w", err)
	}
	defer os.RemoveAll(tmp)

	root := newRoot()
	root.DisableAutoGenTag = true
	if err := doc.GenMarkdownTree(root, tmp); err != nil {
		return fmt.Errorf("generate command docs: %w", err)
	}
	if check {
		return compareCommandDocs(tmp, dest)
	}
	return writeCommandDocs(tmp, dest)
}

func compareCommandDocs(generatedDir, dest string) error {
	generated, err := os.ReadDir(generatedDir)
	if err != nil {
		return fmt.Errorf("read generated command docs: %w", err)
	}
	current, err := os.ReadDir(dest)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("command docs are missing; run go run ./cmd/gen-docs")
	}
	if err != nil {
		return fmt.Errorf("read command docs: %w", err)
	}
	if len(generated) != len(current) {
		return fmt.Errorf("command docs are stale; run go run ./cmd/gen-docs")
	}
	for i, want := range generated {
		got := current[i]
		if want.IsDir() || got.IsDir() || want.Name() != got.Name() {
			return fmt.Errorf("command docs are stale; run go run ./cmd/gen-docs")
		}
		wantBody, err := os.ReadFile(filepath.Join(generatedDir, want.Name()))
		if err != nil {
			return fmt.Errorf("read generated command doc %s: %w", want.Name(), err)
		}
		wantBody = normalizeCommandDoc(wantBody)
		gotBody, err := os.ReadFile(filepath.Join(dest, got.Name()))
		if err != nil {
			return fmt.Errorf("read command doc %s: %w", got.Name(), err)
		}
		if !bytes.Equal(wantBody, gotBody) {
			return fmt.Errorf("command doc %s is stale; run go run ./cmd/gen-docs", got.Name())
		}
	}
	return nil
}

func writeCommandDocs(generatedDir, dest string) error {
	generated, err := os.ReadDir(generatedDir)
	if err != nil {
		return fmt.Errorf("read generated command docs: %w", err)
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return fmt.Errorf("create command docs directory: %w", err)
	}
	current, err := os.ReadDir(dest)
	if err != nil {
		return fmt.Errorf("read command docs directory: %w", err)
	}
	wanted := make(map[string]struct{}, len(generated))
	for _, entry := range generated {
		if entry.IsDir() {
			return fmt.Errorf("unexpected directory in generated command docs: %s", entry.Name())
		}
		wanted[entry.Name()] = struct{}{}
	}
	for _, entry := range current {
		if entry.IsDir() {
			return fmt.Errorf("unexpected directory in command docs: %s", entry.Name())
		}
		if _, ok := wanted[entry.Name()]; !ok {
			if err := os.Remove(filepath.Join(dest, entry.Name())); err != nil {
				return fmt.Errorf("remove stale command doc %s: %w", entry.Name(), err)
			}
		}
	}
	for _, entry := range generated {
		body, err := os.ReadFile(filepath.Join(generatedDir, entry.Name()))
		if err != nil {
			return fmt.Errorf("read generated command doc %s: %w", entry.Name(), err)
		}
		body = normalizeCommandDoc(body)
		if err := os.WriteFile(filepath.Join(dest, entry.Name()), body, 0o644); err != nil {
			return fmt.Errorf("write command doc %s: %w", entry.Name(), err)
		}
	}
	return nil
}

func normalizeCommandDoc(body []byte) []byte {
	return append(bytes.TrimRight(body, "\n"), '\n')
}
