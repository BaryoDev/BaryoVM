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
	errs    map[string]error
	calls   []string
}

// codedErr is an error with an exit code, like the one osRunner returns for a failed git.
type codedErr struct{ code int }

func (e codedErr) Error() string { return "git failed" }
func (e codedErr) ExitCode() int { return e.code }

func (f *fakeGuardRunner) Git(globals []string, args ...string) (string, error) {
	key := strings.Join(args, " ")
	f.calls = append(f.calls, key)
	for k, e := range f.errs {
		if strings.HasPrefix(key, k) {
			return "", e
		}
	}
	// The longest matching prefix wins, so a test can answer "get-url --push --all" differently
	// from a plain "get-url --push".
	best, found := "", false
	for k := range f.answers {
		if strings.HasPrefix(key, k) && (!found || len(k) > len(best)) {
			best, found = k, true
		}
	}
	if found {
		return f.answers[best], nil
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

func TestGhGroupWordFoundPastRepoFlag(t *testing.T) {
	ran := withGuardSeams(t, &fakeGuardRunner{files: map[string]string{"f": "k " + cliFakeKey + "\n"}}, 0)
	err := runGroup(t, "gh", "pr", "-R", "o/r", "create", "--body-file", "f")
	if code, _ := exitCodeOf(err); err == nil || code != 1 {
		t.Fatalf("want exit 1, got %v", err)
	}
	if len(*ran) != 0 {
		t.Fatalf("gh ran: %v", *ran)
	}
}

func TestGhEditCommentReviewAreScanned(t *testing.T) {
	cases := [][]string{
		{"gh", "pr", "edit", "1", "--body-file", "f"},
		{"gh", "pr", "comment", "1", "-F", "f"},
		{"gh", "pr", "review", "1", "--body-file", "f"},
		{"gh", "issue", "edit", "1", "--body-file", "f"},
		{"gh", "issue", "comment", "1", "--body-file", "f"},
		{"gh", "release", "edit", "v1", "--notes-file", "f"},
		{"gh", "release", "create", "v1", "-n", cliFakeKey},
		{"gh", "issue", "comment", "1", "--body", cliFakeKey},
	}
	for _, args := range cases {
		ran := withGuardSeams(t, &fakeGuardRunner{files: map[string]string{"f": "k " + cliFakeKey + "\n"}}, 0)
		err := runGroup(t, args...)
		if code, _ := exitCodeOf(err); err == nil || code != 1 || len(*ran) != 0 {
			t.Fatalf("%v: err=%v ran=%v", args, err, *ran)
		}
	}
}

func TestGitPushDeleteSkipsScanButKeepsHostPolicy(t *testing.T) {
	for _, args := range [][]string{
		{"git", "push", "origin", "--delete", "feat"},
		{"git", "push", "origin", "-d", "feat"},
		{"git", "push", "origin", ":feat"},
	} {
		r := &fakeGuardRunner{answers: map[string]string{"remote get-url": "https://github.com/o/r\n", "log -p": plantedCLIDiff()}}
		ran := withGuardSeams(t, r, 0)
		if err := runGroup(t, args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if len(*ran) != 1 {
			t.Fatalf("%v: ran %v", args, *ran)
		}
		for _, c := range r.calls {
			if strings.HasPrefix(c, "log") {
				t.Fatalf("%v: scanned history for a delete", args)
			}
		}
	}
	t.Setenv("BARYOVM_ALLOWED_HOSTS", "github.com")
	r := &fakeGuardRunner{answers: map[string]string{"remote get-url": "https://evil.example/o/r\n"}}
	ran := withGuardSeams(t, r, 0)
	if err := runGroup(t, "git", "push", "origin", "--delete", "feat"); err == nil || len(*ran) != 0 {
		t.Fatalf("delete to an unlisted host: err=%v ran=%v", err, *ran)
	}
}

func TestGitCommitMessageFileIsScanned(t *testing.T) {
	for _, args := range [][]string{
		{"git", "commit", "-F", "m.txt"},
		{"git", "commit", "--file=m.txt"},
		{"git", "commit", "--file", "m.txt"},
	} {
		ran := withGuardSeams(t, &fakeGuardRunner{files: map[string]string{"m.txt": "subject\n\n" + cliFakeKey + "\n"}}, 0)
		err := runGroup(t, args...)
		if code, _ := exitCodeOf(err); err == nil || code != 1 || len(*ran) != 0 {
			t.Fatalf("%v: err=%v ran=%v", args, err, *ran)
		}
		if !strings.Contains(err.Error(), "m.txt:3") || strings.Contains(err.Error(), cliFakeKey) {
			t.Fatalf("%v: error %s", args, err.Error())
		}
	}
	ran := withGuardSeams(t, &fakeGuardRunner{}, 0)
	err := runGroup(t, "git", "commit", "-F", "-")
	if err == nil || len(*ran) != 0 || !strings.Contains(err.Error(), "stdin") {
		t.Fatalf("-F - was not refused for stdin: err=%v ran=%v", err, *ran)
	}
}

func TestGitCommitAbbreviatedLongOptionsAreResolved(t *testing.T) {
	files := map[string]string{"m.txt": "subject\n" + cliFakeKey + "\n"}
	for _, args := range [][]string{
		{"git", "commit", "--fi", "m.txt"},
		{"git", "commit", "--fil=m.txt"},
		{"git", "commit", "--mess", cliFakeKey},
		{"git", "commit", "--messa=" + cliFakeKey},
		{"git", "commit", "--tem", "m.txt"},
		{"git", "commit", "--trail", "Co: " + cliFakeKey},
	} {
		ran := withGuardSeams(t, &fakeGuardRunner{files: files}, 0)
		err := runGroup(t, args...)
		if code, _ := exitCodeOf(err); err == nil || code != 1 || len(*ran) != 0 {
			t.Fatalf("%v: err=%v ran=%v", args, err, *ran)
		}
	}
}

func TestGitCommitRelativeFileIsRefusedWhenGitIsRedirected(t *testing.T) {
	files := map[string]string{"m.txt": "clean\n"}
	for _, args := range [][]string{
		{"git", "--work-tree=/x", "commit", "-F", "m.txt"},
		{"git", "--git-dir", "/x/.git", "commit", "-F", "m.txt"},
	} {
		ran := withGuardSeams(t, &fakeGuardRunner{files: files}, 0)
		err := runGroup(t, args...)
		if err == nil || len(*ran) != 0 || !strings.Contains(err.Error(), "absolute") {
			t.Fatalf("%v: err=%v ran=%v", args, err, *ran)
		}
	}
	t.Setenv("GIT_DIR", "/x/.git")
	ran := withGuardSeams(t, &fakeGuardRunner{files: files}, 0)
	if err := runGroup(t, "git", "commit", "-F", "m.txt"); err == nil || len(*ran) != 0 {
		t.Fatalf("GIT_DIR: err=%v ran=%v", err, *ran)
	}
	// An absolute path is read as given.
	ran = withGuardSeams(t, &fakeGuardRunner{files: map[string]string{"/abs/m.txt": "clean\n"}}, 0)
	if err := runGroup(t, "git", "commit", "-F", "/abs/m.txt"); err != nil || len(*ran) != 1 {
		t.Fatalf("absolute: err=%v ran=%v", err, *ran)
	}
}

func TestGhFlagsBeforeTheVerbAreRefused(t *testing.T) {
	// The files hold a clean body, so the only reason to refuse is the flag before the verb.
	for _, args := range [][]string{
		{"gh", "pr", "-XR", "o/r", "create", "--body-file", "f"},
		{"gh", "pr", "--foo", "create", "--body-file", "f"},
		{"gh", "issue", "-wR", "o/r", "comment", "1", "--body-file", "f"},
		{"gh", "release", "-v", "create", "v1", "--notes-file", "f"},
	} {
		ran := withGuardSeams(t, &fakeGuardRunner{files: map[string]string{"f": "clean\n"}}, 0)
		err := runGroup(t, args...)
		if code, _ := exitCodeOf(err); err == nil || code != 1 || len(*ran) != 0 || !strings.Contains(err.Error(), "before the command") {
			t.Fatalf("%v: err=%v ran=%v", args, err, *ran)
		}
	}
	// The three accepted forms still work.
	for _, args := range [][]string{
		{"gh", "pr", "-R", "o/r", "create", "--body-file", "f"},
		{"gh", "pr", "--repo", "o/r", "create", "--body-file", "f"},
		{"gh", "pr", "--repo=o/r", "create", "--body-file", "f"},
		{"gh", "--version"},
	} {
		ran := withGuardSeams(t, &fakeGuardRunner{files: map[string]string{"f": "clean\n"}}, 0)
		if err := runGroup(t, args...); err != nil || len(*ran) != 1 {
			t.Fatalf("%v: err=%v ran=%v", args, err, *ran)
		}
	}
}

func TestGitCommitRelativeFileRefusedForEveryRedirect(t *testing.T) {
	files := map[string]string{"m.txt": "clean\n"}
	for _, args := range [][]string{
		{"git", "-c", "core.worktree=/x", "commit", "-F", "m.txt"},
		{"git", "-c", "core.bare=true", "commit", "-F", "m.txt"},
		{"git", "-c", "Core.WorkTree=/x", "commit", "-F", "m.txt"},
		{"git", "--bare", "commit", "-F", "m.txt"},
		{"git", "--config-env=core.worktree=X", "commit", "-F", "m.txt"},
		{"git", "-c", "user.name=x", "commit", "-F", "m.txt"},
		{"git", "-cinclude.path=/x", "commit", "-F", "m.txt"},
		{"git", "--no-pager", "commit", "-F", "m.txt"},
	} {
		ran := withGuardSeams(t, &fakeGuardRunner{files: files}, 0)
		err := runGroup(t, args...)
		if err == nil || len(*ran) != 0 || !strings.Contains(err.Error(), "absolute path") {
			t.Fatalf("%v: err=%v ran=%v", args, err, *ran)
		}
	}
	for _, env := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT", "GIT_CONFIG_GLOBAL"} {
		t.Setenv(env, "1")
		ran := withGuardSeams(t, &fakeGuardRunner{files: files}, 0)
		err := runGroup(t, "git", "commit", "-F", "m.txt")
		if err == nil || len(*ran) != 0 || !strings.Contains(err.Error(), "absolute path") {
			t.Fatalf("%s: err=%v ran=%v", env, err, *ran)
		}
		if err := os.Unsetenv(env); err != nil {
			t.Fatal(err)
		}
	}
	// With no global flag and no GIT_* redirect, the relative file is read and the commit runs.
	ran := withGuardSeams(t, &fakeGuardRunner{files: files}, 0)
	if err := runGroup(t, "git", "commit", "-F", "m.txt"); err != nil || len(*ran) != 1 {
		t.Fatalf("plain commit: err=%v ran=%v", err, *ran)
	}
}

func TestGitPushShortClusterDoesNotHideAScan(t *testing.T) {
	for _, args := range [][]string{
		{"git", "push", "-qod", "origin", "main"},
		{"git", "push", "-o", "d", "origin", "main"},
		{"git", "push", "-od", "origin", "main"},
	} {
		ran := withGuardSeams(t, &fakeGuardRunner{answers: map[string]string{
			"remote get-url": "https://github.com/o/r\n", "log -p": plantedCLIDiff(),
		}}, 0)
		err := runGroup(t, args...)
		if code, _ := exitCodeOf(err); err == nil || code != 1 || len(*ran) != 0 {
			t.Fatalf("%v: err=%v ran=%v", args, err, *ran)
		}
	}
	// Real deletes still pass, including an abbreviated long option.
	for _, args := range [][]string{
		{"git", "push", "-fd", "origin", "feat"},
		{"git", "push", "--del", "origin", "feat"},
	} {
		ran := withGuardSeams(t, &fakeGuardRunner{answers: map[string]string{
			"remote get-url": "https://github.com/o/r\n", "log -p": plantedCLIDiff(),
		}}, 0)
		if err := runGroup(t, args...); err != nil || len(*ran) != 1 {
			t.Fatalf("%v: err=%v ran=%v", args, err, *ran)
		}
	}
}

func TestGhGroupedShortFlagsAreWalked(t *testing.T) {
	refused := [][]string{
		{"gh", "pr", "create", "-wb", cliFakeKey},
		{"gh", "pr", "create", "-dbtext" + cliFakeKey},
		{"gh", "pr", "create", "-wF", "f"},
		{"gh", "pr", "--repo=o/r", "create", "-b", cliFakeKey},
		{"gh", "pr", "review", "1", "-ab", cliFakeKey},
		{"gh", "release", "create", "v1", "-pn", cliFakeKey},
		{"gh", "release", "create", "v1", "-dpt", cliFakeKey},
	}
	for _, args := range refused {
		ran := withGuardSeams(t, &fakeGuardRunner{files: map[string]string{"f": cliFakeKey}}, 0)
		err := runGroup(t, args...)
		if code, _ := exitCodeOf(err); err == nil || code != 1 || len(*ran) != 0 {
			t.Fatalf("%v: err=%v ran=%v", args, err, *ran)
		}
	}
	// A group with a letter we do not know is refused, not skipped.
	ran := withGuardSeams(t, &fakeGuardRunner{}, 0)
	if err := runGroup(t, "gh", "pr", "create", "-zb", cliFakeKey); err == nil || len(*ran) != 0 {
		t.Fatalf("unknown letter: err=%v ran=%v", err, *ran)
	}
	// The same letter differs by command: -p is a bool on release create but takes a value on
	// pr create, so "-pn" there makes n the project title and the key is not text.
	ran = withGuardSeams(t, &fakeGuardRunner{}, 0)
	if err := runGroup(t, "gh", "pr", "create", "-pn", cliFakeKey); err != nil || len(*ran) != 1 {
		t.Fatalf("pr create -p takes a value: err=%v ran=%v", err, *ran)
	}
	// A letter that the subcommand does not list, in a cluster, is refused.
	ran = withGuardSeams(t, &fakeGuardRunner{}, 0)
	if err := runGroup(t, "gh", "release", "create", "v1", "-wb", cliFakeKey); err == nil || len(*ran) != 0 {
		t.Fatalf("w is not a release create flag: err=%v ran=%v", err, *ran)
	}
	// The refusal is for the letter itself: the value here is clean.
	for _, args := range [][]string{
		{"gh", "pr", "create", "-zb", "fine"},
		{"gh", "release", "create", "v1", "-wb", "fine"},
	} {
		ran = withGuardSeams(t, &fakeGuardRunner{}, 0)
		err := runGroup(t, args...)
		if err == nil || len(*ran) != 0 || !strings.Contains(err.Error(), "flag group") {
			t.Fatalf("%v: err=%v ran=%v", args, err, *ran)
		}
	}
	// A clean group passes.
	ran = withGuardSeams(t, &fakeGuardRunner{}, 0)
	if err := runGroup(t, "gh", "pr", "create", "-wb", "fine"); err != nil || len(*ran) != 1 {
		t.Fatalf("clean group: err=%v ran=%v", err, *ran)
	}
}

func TestGuardCheckCommitMsg(t *testing.T) {
	withGuardSeams(t, &fakeGuardRunner{files: map[string]string{"bad": "x " + cliFakeKey + "\n", "ok": "fine\n"}}, 0)
	if err := runGroup(t, "guard", "check", "commit-msg", "ok"); err != nil {
		t.Fatalf("clean message: %v", err)
	}
	err := runGroup(t, "guard", "check", "commit-msg", "bad")
	if code, _ := exitCodeOf(err); err == nil || code != 1 {
		t.Fatalf("want exit 1, got %v", err)
	}
}

func TestGitPushChecksEveryPushURL(t *testing.T) {
	t.Setenv("BARYOVM_ALLOWED_HOSTS", "github.com")
	// Without --all git prints only the first push URL, as the fake does for the plain form.
	r := &fakeGuardRunner{answers: map[string]string{
		"remote get-url --push":       "https://github.com/o/r\n",
		"remote get-url --push --all": "https://github.com/o/r\nhttps://evil.example/o/r\n",
	}}
	ran := withGuardSeams(t, r, 0)
	if err := runGroup(t, "git", "push", "origin", "main"); err == nil || len(*ran) != 0 {
		t.Fatalf("second push url outside the policy: err=%v ran=%v", err, *ran)
	}
}

func TestPolicyConfigErrors(t *testing.T) {
	// Exit 1 means the key is unset: no restriction.
	ran := withGuardSeams(t, &fakeGuardRunner{errs: map[string]error{"config --get-all": codedErr{1}}}, 0)
	if err := runGroup(t, "git", "status"); err != nil {
		t.Fatalf("unset policy: %v", err)
	}
	if len(*ran) != 1 {
		t.Fatalf("ran: %v", *ran)
	}
	// Any other failure refuses.
	ran = withGuardSeams(t, &fakeGuardRunner{errs: map[string]error{"config --get-all": codedErr{128}}}, 0)
	err := runGroup(t, "git", "status")
	if code, _ := exitCodeOf(err); err == nil || code != 1 || len(*ran) != 0 {
		t.Fatalf("broken config: err=%v ran=%v", err, *ran)
	}
	ran = withGuardSeams(t, &fakeGuardRunner{errs: map[string]error{"config --get-all": os.ErrPermission}}, 0)
	if err := runGroup(t, "git", "status"); err == nil || len(*ran) != 0 {
		t.Fatalf("uncoded error: err=%v ran=%v", err, *ran)
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
	// The set is spelled out here so dropping a hook from HookNames fails this test.
	for _, n := range []string{"pre-commit", "commit-msg", "pre-push"} {
		b, err := os.ReadFile(filepath.Join(dir, ".git", "hooks", n))
		if err != nil || !guard.IsOurHook(string(b)) || !strings.Contains(string(b), "guard check "+n) {
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
