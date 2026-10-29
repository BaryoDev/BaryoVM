// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package repomove

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseRefusesAScpFormCredentialAndOtherSmugglingForms(t *testing.T) {
	for _, raw := range []string{
		"user:tok-scp-1@github.com:o/r",
		"https://github.com/o/r?token=tok-scp-1",
		"https://github.com/o/r#tok-scp-1",
		"git@github.com:o/r?x=tok-scp-1",
		"ext::sh -c tok-scp-1",
		"file:///tmp/tok-scp-1",
		"file:tok-scp-1",
	} {
		_, err := Parse(raw)
		if err == nil {
			t.Errorf("%q was accepted", raw)
			continue
		}
		if strings.Contains(err.Error(), "tok-scp-1") {
			t.Errorf("%q: the error echoes the token: %v", raw, err)
		}
	}
	if _, err := Parse("user:tok-scp-1@github.com:o/r"); !errors.Is(err, ErrCredential) {
		t.Errorf("a scp credential is not ErrCredential: %v", err)
	}
	if r, err := Parse("git@github.com:o/r.git"); err != nil || r.Host != "github.com" {
		t.Errorf("a plain scp url was refused: %v %+v", err, r)
	}
}

func TestParseLenientReadsHostAndPathOfACredentialOriginOnly(t *testing.T) {
	r, err := ParseLenient("https://deploy:tok-len-1@github.com/acme/app.git")
	if err != nil || r.Host != "github.com" || r.Path != "acme/app" {
		t.Fatalf("%v %+v", err, r)
	}
	if _, err := ParseLenient("ext::sh -c x"); err == nil {
		t.Error("lenient parse accepted ext::")
	}
}

func TestApproveCommandResetsTheHelperListBeforeNamingTheStore(t *testing.T) {
	cmd, err := CredApproveCmd(true, "opc", "/opt/deploy/app")
	if err != nil {
		t.Fatal(err)
	}
	reset := strings.Index(cmd, "-c credential.helper= ")
	store := strings.Index(cmd, "store --file=")
	if reset < 0 || store < 0 || reset > store {
		t.Errorf("approve would reach the owner's global helpers: %s", cmd)
	}
}

func TestCredUndoRemovesTheFileAndTheHelper(t *testing.T) {
	cmd, err := CredUndoCmd(true, "opc", "/opt/deploy/app")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"rm -f -- ", "baryovm-credentials", "--unset-all credential.helper", "sudo -n -u"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("missing %q in %s", want, cmd)
		}
	}
	if _, err := CredUndoCmd(true, "opc", "/opt/a b"); err == nil {
		t.Error("an unsafe directory was accepted")
	}
}

func TestCredInputRefusesLineBreaksAndNUL(t *testing.T) {
	for _, tok := range []string{"a\nb", "a\rb", "a\x00b"} {
		if _, err := CredInput("dev.azure.com", "u", tok); err == nil {
			t.Errorf("token %q was accepted", tok)
		}
	}
	if _, err := CredInput("dev.azure.com\nhost=evil", "u", "t"); err == nil {
		t.Error("a host with a line break was accepted")
	}
	if got, err := CredInput("dev.azure.com", "u", "t"); err != nil || !strings.Contains(got, "password=t\n") {
		t.Errorf("%q %v", got, err)
	}
}

func TestOnlyMissingIsAStrictSubset(t *testing.T) {
	src := Refs{"refs/heads/a": "1", "refs/heads/b": "2"}
	if d := Diff(src, Refs{"refs/heads/a": "1"}); !d.OnlyMissing() {
		t.Error("a strict subset is not only-missing")
	}
	if d := Diff(src, Refs{"refs/heads/a": "9"}); d.OnlyMissing() {
		t.Error("a changed ref counted as only-missing")
	}
	if d := Diff(src, Refs{"refs/heads/a": "1", "refs/heads/z": "3"}); d.OnlyMissing() {
		t.Error("an extra ref counted as only-missing")
	}
	if d := Diff(src, src); d.OnlyMissing() {
		t.Error("identical lists counted as only-missing")
	}
}

func TestTrackedBranchesPicksOnlyTheNamedRemote(t *testing.T) {
	out := "branch.main.remote origin\nbranch.feat/x.remote origin\nbranch.old.remote other\n"
	got := TrackedBranches(out, "origin")
	if len(got) != 2 || got[0] != "main" || got[1] != "feat/x" {
		t.Errorf("%v", got)
	}
}

func TestExportWriteReplacesASymlinkInsteadOfFollowingIt(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "precious")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "issues.json")); err != nil {
		t.Fatal(err)
	}
	e := &Export{Issues: []map[string]any{{"number": 1}}, PullRequests: []map[string]any{}}
	if err := e.Write(dir); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(target); string(b) != "keep" {
		t.Errorf("the symlink target was overwritten: %q", b)
	}
	st, err := os.Lstat(filepath.Join(dir, "issues.json"))
	if err != nil || !st.Mode().IsRegular() {
		t.Errorf("issues.json is not a regular file: %v %v", st, err)
	}
	if !ExportPresent(dir) {
		t.Error("a finished export is not recognised")
	}
	left, _ := filepath.Glob(filepath.Join(dir, ".*.tmp"))
	if len(left) != 0 {
		t.Errorf("temp files left behind: %v", left)
	}
}

func TestExportPresentRejectsAHalfWrittenOrLinkedFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "issues.json"), []byte(`[{"number":1},`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pulls.json"), []byte(`[]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if ExportPresent(dir) {
		t.Error("a truncated issues.json counted as finished")
	}
	if err := os.WriteFile(filepath.Join(dir, "issues.json"), []byte(`[]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !ExportPresent(dir) {
		t.Fatal("two valid files should count")
	}
	_ = os.Remove(filepath.Join(dir, "pulls.json"))
	if err := os.Symlink(filepath.Join(dir, "issues.json"), filepath.Join(dir, "pulls.json")); err != nil {
		t.Fatal(err)
	}
	if ExportPresent(dir) {
		t.Error("a symlinked pulls.json counted as finished")
	}
}
