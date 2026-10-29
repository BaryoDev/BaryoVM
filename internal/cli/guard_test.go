// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BaryoDev/BaryoVM/internal/guard"
	"github.com/BaryoDev/BaryoVM/internal/ui"
)

// Built from pieces so this file does not trip the scanner that guards this repo.
var cliFakeKey = "AI" + "za" + strings.Repeat("Q", 35)

type fakeGuardRunner struct {
	answers map[string]string
	files   map[string]string
	calls   []string
}

func (f *fakeGuardRunner) Git(globals []string, args ...string) (string, error) {
	key := strings.Join(args, " ")
	f.calls = append(f.calls, key)
	for k, v := range f.answers {
		if strings.HasPrefix(key, k) {
			return v, nil
		}
	}
	return "", nil
}

func (f *fakeGuardRunner) ReadFile(p string) ([]byte, error) {
	if s, ok := f.files[p]; ok {
		return []byte(s), nil
	}
	return nil, os.ErrNotExist
}

// withGuardSeams swaps both seams and records what the real tool would have been given.
func withGuardSeams(t *testing.T, r guard.Runner, exitCode int) *[]string {
	t.Helper()
	oldR, oldP := guardRunner, passthrough
	var ran []string
	guardRunner = r
	passthrough = func(name string, args []string) (int, error) {
		ran = append(ran, name+" "+strings.Join(args, " "))
		return exitCode, nil
	}
	t.Cleanup(func() { guardRunner, passthrough = oldR, oldP; ui.SetJSON(false) })
	return &ran
}

func plantedCLIDiff() string {
	return "diff --git a/cfg.js b/cfg.js\n--- a/cfg.js\n+++ b/cfg.js\n@@ -0,0 +7 @@\n+k=\"" + cliFakeKey + "\"\n"
}

// guardCaptureStdout runs fn and returns what it wrote to stdout.
func guardCaptureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()
	fn()
	w.Close()
	b, _ := io.ReadAll(r)
	return string(b)
}

func TestGitCommitWithKeyIsRefused(t *testing.T) {
	t.Setenv("BARYOVM_OUTPUT", "json")
	ran := withGuardSeams(t, &fakeGuardRunner{answers: map[string]string{"diff --cached": plantedCLIDiff()}}, 0)
	var err error
	out := guardCaptureStdout(t, func() { err = runGroup(t, "git", "commit", "-m", "add config") })
	code, _ := exitCodeOf(err)
	if err == nil || code == 0 {
		t.Fatalf("want a non-zero exit, got %v", err)
	}
	if len(*ran) != 0 {
		t.Fatalf("git ran after a refusal: %v", *ran)
	}
	var env struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if jerr := json.Unmarshal([]byte(out), &env); jerr != nil {
		t.Fatalf("not JSON: %q", out)
	}
	if env.OK || !strings.Contains(env.Error, "cfg.js:7") || strings.Contains(out, cliFakeKey) {
		t.Fatalf("envelope: %s", out)
	}
}

func TestGitPushWithKeyIsRefused(t *testing.T) {
	ran := withGuardSeams(t, &fakeGuardRunner{answers: map[string]string{
		"remote get-url": "https://github.com/o/r\n",
		"log -p":         plantedCLIDiff(),
	}}, 0)
	err := runGroup(t, "git", "push", "origin", "main")
	if code, _ := exitCodeOf(err); err == nil || code != 1 {
		t.Fatalf("want exit 1, got %v", err)
	}
	if len(*ran) != 0 {
		t.Fatalf("git ran: %v", *ran)
	}
}

func TestGitPushToUnlistedHostFailsBeforeTheNetwork(t *testing.T) {
	t.Setenv("BARYOVM_ALLOWED_HOSTS", "github.com")
	r := &fakeGuardRunner{answers: map[string]string{"remote get-url": "https://evil.example/o/r\n"}}
	ran := withGuardSeams(t, r, 0)
	err := runGroup(t, "git", "push", "origin", "main")
	if err == nil {
		t.Fatal("push to an unlisted host succeeded")
	}
	if len(*ran) != 0 {
		t.Fatalf("git push ran: %v", *ran)
	}
	for _, c := range r.calls {
		if strings.HasPrefix(c, "log") {
			t.Fatalf("scanned history before refusing the host: %v", r.calls)
		}
	}
}

func TestGitPolicyComesFromGitConfig(t *testing.T) {
	r := &fakeGuardRunner{answers: map[string]string{
		"config --get-all": "github.com\n",
		"remote get-url":   "https://evil.example/o/r\n",
	}}
	withGuardSeams(t, r, 0)
	if err := runGroup(t, "git", "push", "origin", "main"); err == nil {
		t.Fatal("git config policy ignored")
	}
}

func TestGitCleanRunPassesThroughWithGitsExitCode(t *testing.T) {
	ran := withGuardSeams(t, &fakeGuardRunner{}, 0)
	if err := runGroup(t, "git", "commit", "-m", "clean"); err != nil {
		t.Fatalf("clean commit: %v", err)
	}
	if len(*ran) != 1 || (*ran)[0] != "git commit -m clean" {
		t.Fatalf("ran: %v", *ran)
	}

	withGuardSeams(t, &fakeGuardRunner{}, 128)
	err := runGroup(t, "git", "status")
	if code, silent := exitCodeOf(err); code != 128 || !silent {
		t.Fatalf("want git's exit 128 silently, got %d silent=%v err=%v", code, silent, err)
	}
}

func TestGitPassesFlagsToGit(t *testing.T) {
	ran := withGuardSeams(t, &fakeGuardRunner{}, 0)
	if err := runGroup(t, "git", "log", "-o", "--oneline"); err != nil {
		t.Fatal(err)
	}
	if (*ran)[0] != "git log -o --oneline" {
		t.Fatalf("ran: %v", *ran)
	}
}

func TestGhPRCreateBodyFileWithKeyIsRefused(t *testing.T) {
	ran := withGuardSeams(t, &fakeGuardRunner{files: map[string]string{"body.md": "line one\nkey " + cliFakeKey + "\n"}}, 0)
	err := runGroup(t, "gh", "pr", "create", "--title", "t", "--body-file", "body.md")
	if code, _ := exitCodeOf(err); err == nil || code != 1 {
		t.Fatalf("want exit 1, got %v", err)
	}
	if len(*ran) != 0 {
		t.Fatalf("gh ran: %v", *ran)
	}
	if strings.Contains(err.Error(), cliFakeKey) || !strings.Contains(err.Error(), "body.md:2") {
		t.Fatalf("error: %s", err.Error())
	}
}

func TestGhCleanAndOtherCommandsPassThrough(t *testing.T) {
	ran := withGuardSeams(t, &fakeGuardRunner{files: map[string]string{"b.md": "fine\n"}}, 0)
	for _, args := range [][]string{{"gh", "pr", "create", "--body-file", "b.md"}, {"gh", "pr", "list"}} {
		if err := runGroup(t, args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	if len(*ran) != 2 {
		t.Fatalf("ran: %v", *ran)
	}
}

// run runs git in a real scratch repo (local only, no remote) to prove the seam and the scanner
// agree with git's own diff output.
func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestRealRepoStagedKeyIsFoundWithFileAndLine(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q")
	body := "line1\nline2\nconst k = \"" + cliFakeKey + "\"\n"
	if err := os.WriteFile(filepath.Join(dir, "cfg.js"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", "cfg.js")

	v := guard.CheckGit(osRunner{}, []string{"-C", dir, "commit", "-m", "x"}, guard.Policy{})
	if v.Allowed || len(v.Findings) != 1 || v.Findings[0].File != "cfg.js" || v.Findings[0].Line != 3 {
		t.Fatalf("got %+v", v)
	}
	if strings.Contains(v.Reason, cliFakeKey) {
		t.Fatal("reason prints the key")
	}
}

func TestRealRepoBinaryAttributeDoesNotHideAKey(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, ".gitattributes"), []byte("*.cfg binary\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "x.cfg"), []byte("a\nk=\""+cliFakeKey+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", "x.cfg")
	v := guard.CheckGit(osRunner{}, []string{"-C", dir, "commit", "-m", "x"}, guard.Policy{})
	if v.Allowed || len(v.Findings) != 1 || v.Findings[0].File != "x.cfg" || v.Findings[0].Line != 2 {
		t.Fatalf("got %+v", v)
	}
}

func TestInstallHooks(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q")
	wd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
	oldR := guardRunner
	guardRunner = osRunner{}
	t.Cleanup(func() { guardRunner = oldR })

	if err := runGroup(t, "guard", "install-hooks"); err != nil {
		t.Fatal(err)
	}
	for _, n := range guard.HookNames {
		b, err := os.ReadFile(filepath.Join(dir, ".git", "hooks", n))
		if err != nil || !guard.IsOurHook(string(b)) {
			t.Fatalf("%s: %v %q", n, err, b)
		}
		if fi, _ := os.Stat(filepath.Join(dir, ".git", "hooks", n)); fi.Mode()&0o100 == 0 {
			t.Errorf("%s not executable", n)
		}
	}
	// Running it twice is fine: the hooks are ours.
	if err := runGroup(t, "guard", "install-hooks"); err != nil {
		t.Fatalf("second run: %v", err)
	}

	// A foreign hook is kept unless --force, and --force keeps a copy.
	foreign := filepath.Join(dir, ".git", "hooks", "pre-push")
	if err := os.WriteFile(foreign, []byte("#!/bin/sh\necho mine\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := runGroup(t, "guard", "install-hooks"); err == nil {
		t.Fatal("overwrote a foreign hook without --force")
	}
	if b, _ := os.ReadFile(foreign); !strings.Contains(string(b), "echo mine") {
		t.Fatal("foreign hook changed on refusal")
	}
	if err := runGroup(t, "guard", "install-hooks", "--force"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(foreign + ".baryovm-old"); !strings.Contains(string(b), "echo mine") {
		t.Fatal("foreign hook not kept")
	}
}

func TestGuardCheckPrePushReadsStdin(t *testing.T) {
	sha := strings.Repeat("a", 40)
	zero := strings.Repeat("0", 40)
	withGuardSeams(t, &fakeGuardRunner{answers: map[string]string{"log -p": plantedCLIDiff()}}, 0)
	root := newRoot()
	root.SetIn(bytes.NewBufferString("refs/heads/main " + sha + " refs/heads/main " + zero + "\n"))
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"guard", "check", "pre-push", "origin", "https://github.com/o/r"})
	if _, err := root.ExecuteC(); err == nil {
		t.Fatal("hook let a key through")
	}
}

func TestGuardCheckPreCommitInScratchRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q")
	wd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
	oldR := guardRunner
	guardRunner = osRunner{}
	t.Cleanup(func() { guardRunner = oldR; ui.SetJSON(false) })

	if err := os.WriteFile(filepath.Join(dir, "ok.txt"), []byte("fine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", "ok.txt")
	if err := runGroup(t, "guard", "check", "pre-commit"); err != nil {
		t.Fatalf("clean stage refused: %v", err)
	}

	body := "a\nb\nconst k = \"" + cliFakeKey + "\"\n"
	if err := os.WriteFile(filepath.Join(dir, "cfg.js"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", "cfg.js")
	err := runGroup(t, "guard", "check", "pre-commit")
	if code, _ := exitCodeOf(err); err == nil || code == 0 {
		t.Fatalf("staged key allowed: %v", err)
	}
	if !strings.Contains(err.Error(), "cfg.js:3") || strings.Contains(err.Error(), cliFakeKey) {
		t.Fatalf("error: %s", err.Error())
	}
}

func TestGitAndGuardCommandsLeaveNoActivityLine(t *testing.T) {
	home := activityHome(t)
	withGuardSeams(t, &fakeGuardRunner{}, 0)
	if err := runLogged(t, "git", "status"); err != nil {
		t.Fatal(err)
	}
	if err := runLogged(t, "guard", "check", "pre-commit"); err != nil {
		t.Fatal(err)
	}
	if got := logLines(t, home); len(got) != 0 {
		t.Fatalf("want no activity lines, got %v", got)
	}
}
