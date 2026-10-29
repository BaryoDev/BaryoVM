// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package secretref

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func confined(t *testing.T) (Resolver, string) {
	t.Helper()
	root := t.TempDir()
	r := Default().Confine(root)
	r.Getenv = func(k string) string { return secret }
	return r, root
}

func TestConfinedEnvNeedsThePrefix(t *testing.T) {
	r, _ := confined(t)
	if _, err := r.Resolve("env:AWS_SECRET_ACCESS_KEY"); err == nil || !strings.Contains(err.Error(), "BARYOVM_") || strings.Contains(err.Error(), secret) {
		t.Fatalf("want a refusal naming the rule and no value, got %v", err)
	}
	if err := r.Check("env:HOME"); err == nil {
		t.Error("Check must refuse env:HOME without reading anything")
	}
	if got, err := r.Resolve("env:BARYOVM_NPM_TOKEN"); err != nil || got != secret {
		t.Errorf("a prefixed name must resolve: %q, %v", got, err)
	}
}

func TestConfinedFileRefusesAbsoluteHomeAndParentPaths(t *testing.T) {
	r, _ := confined(t)
	for _, ref := range []string{"file:/etc/passwd", "file:~/.ssh/id_ed25519", "file:../x", "file:a/../../x", "file:~"} {
		err := r.Check(ref)
		if err == nil || !strings.Contains(err.Error(), ref) {
			t.Errorf("%s: want a refusal naming the reference, got %v", ref, err)
		}
		if _, err := r.Resolve(ref); err == nil {
			t.Errorf("%s: Resolve must refuse too", ref)
		}
	}
}

func TestConfinedFileRefusesASymlinkOutsideTheRoot(t *testing.T) {
	r, root := confined(t)
	outside := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(outside, []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	_, err := r.Resolve("file:link")
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("want a refusal without the value, got %v", err)
	}
}

func TestConfinedFileReadsInsideTheRoot(t *testing.T) {
	r, root := confined(t)
	if err := os.MkdirAll(filepath.Join(root, "keys"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "keys", "tok"), []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "keys", "tok"), filepath.Join(root, "inner")); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"file:keys/tok", "file:./keys/tok", "file:inner"} {
		if got, err := r.Resolve(ref); err != nil || got != secret {
			t.Errorf("%s: got %q, %v", ref, got, err)
		}
	}
}

func TestUnconfinedResolverKeepsTheOldReach(t *testing.T) {
	r := fake()
	if got, err := r.Resolve("file:/run/tok"); err != nil || got != secret {
		t.Errorf("the command line is not confined: %q, %v", got, err)
	}
}
