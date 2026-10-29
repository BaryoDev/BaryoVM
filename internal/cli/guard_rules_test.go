// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BaryoDev/BaryoVM/internal/guard"
)

// Built from pieces so this file does not trip the style scan.
var (
	fxTrailer = "Co-Authored" + "-By: Claude <noreply@" + "anthropic.com>"
	fxFooter  = "Generated " + "with Claude Code"
	fxRobot   = "\U0001F916 " + fxFooter
)

const ghURL = "https://github.com/o/r\n"

func rulesRunner(cfg string, answers map[string]string) *fakeGuardRunner {
	a := map[string]string{"remote get-url": ghURL, "config --get-regexp": cfg, "symbolic-ref --short -q HEAD": "feature\n"}
	for k, v := range answers {
		a[k] = v
	}
	return &fakeGuardRunner{answers: a, files: map[string]string{}}
}

// runRule runs one wrapper command and reports whether the real tool ran and the error.
func runRule(t *testing.T, r *fakeGuardRunner, args ...string) (ran []string, err error) {
	t.Helper()
	t.Setenv("BARYOVM_HOME", t.TempDir())
	t.Setenv("BARYOVM_ACTIVITY", "")
	p := withGuardSeams(t, r, 0)
	err = runGroup(t, args...)
	return *p, err
}

func ruleRefused(t *testing.T, ran []string, err error, contains string) {
	t.Helper()
	if err == nil || len(ran) != 0 {
		t.Fatalf("want a refusal, got err=%v ran=%v", err, ran)
	}
	if contains != "" && !strings.Contains(err.Error(), contains) {
		t.Fatalf("refusal %q does not say %q", err.Error(), contains)
	}
}

func ruleRan(t *testing.T, ran []string, err error) {
	t.Helper()
	if err != nil || len(ran) != 1 {
		t.Fatalf("want it to run, got err=%v ran=%v", err, ran)
	}
}

// Rule 1

func TestProtectedBranchPush(t *testing.T) {
	for _, args := range [][]string{
		{"git", "push", "origin", "main"},
		{"git", "push", "origin", "HEAD:main"},
		{"git", "push", "origin", "+main"},
		{"git", "push", "--force", "origin", "main"},
		{"git", "push", "-f", "origin", "feature:main"},
		{"git", "push", "--force-with-lease", "origin", "main"},
		{"git", "push", "origin", "--delete", "main"},
		{"git", "push", "origin", "release"},
		{"git", "push", "--all"},
		{"git", "push", "--mirror"},
	} {
		ran, err := runRule(t, rulesRunner("baryovm.protectedbranches release", nil), args...)
		ruleRefused(t, ran, err, "")
		if !strings.Contains(err.Error(), "baryovm.rules.off") && !strings.Contains(err.Error(), "forceLeaseBranches") {
			t.Errorf("%v: refusal does not say how to pass: %v", args, err)
		}
	}
	// A bare push from the default branch is a push to it.
	r := rulesRunner("", map[string]string{"symbolic-ref --short -q HEAD": "main\n"})
	ran, err := runRule(t, r, "git", "push")
	ruleRefused(t, ran, err, "main")
}

func TestProtectedBranchAllowsOrdinaryPushes(t *testing.T) {
	for _, args := range [][]string{
		{"git", "push", "origin", "feature"},
		{"git", "push", "-u", "origin", "feature"},
		{"git", "push", "--force", "origin", "feature"},
		{"git", "push", "--force-with-lease", "origin", "feature"},
		{"git", "push"},
	} {
		ran, err := runRule(t, rulesRunner("", nil), args...)
		ruleRan(t, ran, err)
	}
}

func TestProtectedBranchLeaseOnlyWhereThePolicyAllows(t *testing.T) {
	ran, err := runRule(t, rulesRunner("baryovm.forceleasebranches main", nil), "git", "push", "--force-with-lease", "origin", "main")
	ruleRan(t, ran, err)
	// A plain force and a plain push stay refused on the same branch.
	ran, err = runRule(t, rulesRunner("baryovm.forceleasebranches main", nil), "git", "push", "--force", "origin", "main")
	ruleRefused(t, ran, err, "force push")
	ran, err = runRule(t, rulesRunner("baryovm.forceleasebranches main", nil), "git", "push", "origin", "main")
	ruleRefused(t, ran, err, "direct push")
}

func TestProtectedBranchUsesTheRemoteDefault(t *testing.T) {
	r := rulesRunner("", map[string]string{"symbolic-ref --short -q refs/remotes/origin/HEAD": "origin/trunk\n"})
	ran, err := runRule(t, r, "git", "push", "origin", "trunk")
	ruleRefused(t, ran, err, "trunk")
	r = rulesRunner("", map[string]string{"symbolic-ref --short -q refs/remotes/origin/HEAD": "origin/trunk\n"})
	ran, err = runRule(t, r, "git", "push", "origin", "main")
	ruleRan(t, ran, err)
}

func TestProtectedBranchRuleCanBeOff(t *testing.T) {
	ran, err := runRule(t, rulesRunner("baryovm.rules.off protected-branches", nil), "git", "push", "--force", "origin", "main")
	ruleRan(t, ran, err)
	t.Setenv("BARYOVM_RULES_OFF", "protected-branches")
	ran, err = runRule(t, rulesRunner("", nil), "git", "push", "origin", "main")
	ruleRan(t, ran, err)
}

// Rule 2

func TestGhRepoDeleteNeedsTheNameTypedBack(t *testing.T) {
	r := rulesRunner("", nil)
	ran, err := runRule(t, r, "gh", "repo", "delete", "o/r")
	ruleRefused(t, ran, err, "--confirm o/r")

	ran, err = runRule(t, rulesRunner("", nil), "gh", "repo", "delete", "o/r", "--confirm", "o/other")
	ruleRefused(t, ran, err, "does not match")

	ran, err = runRule(t, rulesRunner("", nil), "gh", "repo", "delete")
	ruleRefused(t, ran, err, "OWNER/REPO")

	ran, err = runRule(t, rulesRunner("", nil), "gh", "repo", "delete", "o/r", "--confirm", "o/r")
	ruleRan(t, ran, err)
	if ran[0] != "gh repo delete o/r --yes" {
		t.Fatalf("--confirm must be consumed and --yes added, got %q", ran[0])
	}
}

func TestDestructiveUnderJSONNeverPrompts(t *testing.T) {
	t.Setenv("BARYOVM_OUTPUT", "json")
	old := guardEnv
	t.Cleanup(func() { guardEnv = old })
	prompted := false
	guardEnv = func(confirm string) guard.Env {
		return guard.Env{Interactive: false, Confirm: confirm, Prompt: func(string) (string, error) { prompted = true; return "o/r", nil }}
	}
	ran, err := runRule(t, rulesRunner("", nil), "gh", "repo", "delete", "o/r")
	ruleRefused(t, ran, err, "--confirm")
	if prompted {
		t.Fatal("prompted under json")
	}
}

func TestGhReleaseDelete(t *testing.T) {
	ran, err := runRule(t, rulesRunner("", nil), "gh", "release", "delete", "v1.0")
	ruleRefused(t, ran, err, "--confirm v1.0")
	ran, err = runRule(t, rulesRunner("", nil), "gh", "release", "delete", "-R", "o/r", "v1.0", "--confirm=v1.0")
	ruleRan(t, ran, err)
}

func TestPromptedConfirmation(t *testing.T) {
	old := guardEnv
	t.Cleanup(func() { guardEnv = old })
	run := func(answer string) (ran, shown []string, err error) {
		guardEnv = func(confirm string) guard.Env {
			return guard.Env{Interactive: true, Confirm: confirm,
				Prompt: func(string) (string, error) { return answer, nil },
				Show:   func(l []string) { shown = append(shown, l...) }}
		}
		ran, err = runRule(t, rulesRunner("", nil), "gh", "release", "delete", "v1")
		return
	}
	ran, shown, err := run("v1")
	ruleRan(t, ran, err)
	if len(shown) == 0 || !strings.Contains(shown[0], "v1") {
		t.Fatalf("did not print what goes: %v", shown)
	}
	ran, _, err = run("nope")
	ruleRefused(t, ran, err, "did not match")
}

func TestDestructiveRuleCanBeOff(t *testing.T) {
	ran, err := runRule(t, rulesRunner("baryovm.rules.off destructive", nil), "gh", "repo", "delete", "o/r")
	ruleRan(t, ran, err)
	if ran[0] != "gh repo delete o/r" {
		t.Fatalf("args changed with the rule off: %q", ran[0])
	}
}

func TestGitResetHardWithUncommittedWork(t *testing.T) {
	dirty := map[string]string{"status --porcelain": " M a.go\n M b.go\n"}
	ran, err := runRule(t, rulesRunner("", dirty), "git", "reset", "--hard", "HEAD~1")
	ruleRefused(t, ran, err, "--confirm feature")
	ran, err = runRule(t, rulesRunner("", dirty), "git", "reset", "--hard", "--confirm", "feature")
	ruleRan(t, ran, err)
	if strings.Contains(ran[0], "confirm") {
		t.Fatalf("--confirm reached git: %q", ran[0])
	}
	// Clean tree and a soft reset are not destructive.
	ran, err = runRule(t, rulesRunner("", nil), "git", "reset", "--hard")
	ruleRan(t, ran, err)
	ran, err = runRule(t, rulesRunner("", dirty), "git", "reset", "--soft", "HEAD~1")
	ruleRan(t, ran, err)
	ran, err = runRule(t, rulesRunner("baryovm.rules.off destructive", dirty), "git", "reset", "--hard")
	ruleRan(t, ran, err)
}

func TestGitCleanFdx(t *testing.T) {
	listing := map[string]string{"clean -n": "Would remove build/\nWould remove .env\n", "rev-parse --show-toplevel": "/work/myrepo\n"}
	ran, err := runRule(t, rulesRunner("", listing), "git", "clean", "-fdx")
	ruleRefused(t, ran, err, "--confirm myrepo")
	ran, err = runRule(t, rulesRunner("", listing), "git", "clean", "-fdx", "--confirm", "myrepo")
	ruleRan(t, ran, err)
	// A dry run, or nothing to remove, goes straight through.
	ran, err = runRule(t, rulesRunner("", listing), "git", "clean", "-ndx")
	ruleRan(t, ran, err)
	ran, err = runRule(t, rulesRunner("", nil), "git", "clean", "-fdx")
	ruleRan(t, ran, err)
	ran, err = runRule(t, rulesRunner("baryovm.rules.off destructive", listing), "git", "clean", "-fdx")
	ruleRan(t, ran, err)
}

func TestGitPushDeleteTag(t *testing.T) {
	tag := map[string]string{"rev-parse -q --verify refs/tags/v1": "abc\n"}
	for _, args := range [][]string{
		{"git", "push", "origin", "--delete", "v1"},
		{"git", "push", "origin", ":refs/tags/v1"},
		{"git", "push", "origin", "--delete", "refs/tags/v1"},
	} {
		ran, err := runRule(t, rulesRunner("", tag), args...)
		ruleRefused(t, ran, err, "--confirm v1")
	}
	ran, err := runRule(t, rulesRunner("", tag), "git", "push", "origin", "--delete", "v1", "--confirm", "v1")
	ruleRan(t, ran, err)
	// A branch delete is not a tag delete.
	ran, err = runRule(t, rulesRunner("", nil), "git", "push", "origin", "--delete", "old-branch")
	ruleRan(t, ran, err)
	ran, err = runRule(t, rulesRunner("baryovm.rules.off destructive", tag), "git", "push", "origin", "--delete", "v1")
	ruleRan(t, ran, err)
}

// Rule 3

func TestFilesNeverInGit(t *testing.T) {
	sa := `{"type": "service_account", "project_id": "p"}`
	for name, extra := range map[string]map[string]string{
		".env":             nil,
		"app/.env.local":   nil,
		"certs/server.PEM": nil,
		"a.p12":            nil,
		"a.jks":            nil,
		"release.keystore": nil,
		"creds.json":       {"show :creds.json": sa},
		"big.bin":          {"cat-file -s :big.bin": "99999999"},
	} {
		a := map[string]string{"diff --cached --name-only": name + "\n"}
		for k, v := range extra {
			a[k] = v
		}
		ran, err := runRule(t, rulesRunner("", a), "git", "commit", "-m", "x")
		ruleRefused(t, ran, err, name)
		if !strings.Contains(err.Error(), "baryovm.rules.off") || !strings.Contains(err.Error(), "allowFiles") {
			t.Errorf("%s: refusal does not say how to pass: %v", name, err)
		}
	}
	for name, extra := range map[string]map[string]string{
		".env.example": nil,
		"config.json":  {"show :config.json": `{"type": "user"}`},
		"small.bin":    {"cat-file -s :small.bin": "100"},
		"main.go":      nil,
	} {
		a := map[string]string{"diff --cached --name-only": name + "\n"}
		for k, v := range extra {
			a[k] = v
		}
		ran, err := runRule(t, rulesRunner("", a), "git", "commit", "-m", "x")
		ruleRan(t, ran, err)
	}
}

func TestFilesPolicyKnobs(t *testing.T) {
	a := map[string]string{"diff --cached --name-only": "certs/public.pem\n"}
	ran, err := runRule(t, rulesRunner("baryovm.allowfiles certs/public.pem", a), "git", "commit", "-m", "x")
	ruleRan(t, ran, err)
	ran, err = runRule(t, rulesRunner("baryovm.rules.off files", a), "git", "commit", "-m", "x")
	ruleRan(t, ran, err)

	big := map[string]string{"diff --cached --name-only": "d.bin\n", "cat-file -s :d.bin": "2000000"}
	ran, err = runRule(t, rulesRunner("baryovm.maxfilesize 1M", big), "git", "commit", "-m", "x")
	ruleRefused(t, ran, err, "d.bin")
	ran, err = runRule(t, rulesRunner("baryovm.maxfilesize 5M", big), "git", "commit", "-m", "x")
	ruleRan(t, ran, err)
}

func TestFilesCheckedOnThePushedRange(t *testing.T) {
	a := map[string]string{"log --name-only": "src/a.go\n.env\n"}
	ran, err := runRule(t, rulesRunner("", a), "git", "push", "origin", "feature")
	ruleRefused(t, ran, err, ".env")
	ran, err = runRule(t, rulesRunner("baryovm.rules.off files", a), "git", "push", "origin", "feature")
	ruleRan(t, ran, err)
}

// Rule 4

func TestCommitTextAttribution(t *testing.T) {
	trailer := "add login\n\n" + fxTrailer
	ran, err := runRule(t, rulesRunner("", nil), "git", "commit", "-m", trailer)
	ruleRefused(t, ran, err, "ai-attribution")
	if !strings.Contains(err.Error(), "baryovm.rules.off") {
		t.Fatalf("no way to pass: %v", err)
	}
	ran, err = runRule(t, rulesRunner("", nil), "git", "commit", "-m", "add login", "--trailer", "Co-authored"+"-by: Copilot <c@github.com>")
	ruleRefused(t, ran, err, "ai-attribution")

	r := rulesRunner("", nil)
	r.files["/m.txt"] = trailer
	ran, err = runRule(t, r, "git", "commit", "-F", "/m.txt")
	ruleRefused(t, ran, err, "ai-attribution")

	ran, err = runRule(t, rulesRunner("", nil), "git", "commit", "-m", "add login")
	ruleRan(t, ran, err)
	ran, err = runRule(t, rulesRunner("baryovm.rules.off text", nil), "git", "commit", "-m", trailer)
	ruleRan(t, ran, err)
}

func TestGhTextAttribution(t *testing.T) {
	ran, err := runRule(t, rulesRunner("", nil), "gh", "pr", "create", "--title", "t", "--body", "Fixes it.\n\n"+fxFooter)
	ruleRefused(t, ran, err, "ai-attribution")
	r := rulesRunner("", nil)
	r.files["body.md"] = fxRobot + "\n"
	ran, err = runRule(t, r, "gh", "pr", "create", "--title", "t", "--body-file", "body.md")
	ruleRefused(t, ran, err, "ai-attribution")
	ran, err = runRule(t, rulesRunner("", nil), "gh", "release", "create", "v1", "--notes", fxTrailer)
	ruleRefused(t, ran, err, "ai-attribution")

	ran, err = runRule(t, rulesRunner("", nil), "gh", "pr", "create", "--title", "t", "--body", "plain")
	ruleRan(t, ran, err)
	ran, err = runRule(t, rulesRunner("baryovm.rules.off text", nil), "gh", "pr", "create", "--title", "t", "--body", fxFooter)
	ruleRan(t, ran, err)
}

func TestHouseStyleOnlyWhenTheRepoOptsIn(t *testing.T) {
	msg := "add login \u2014 fast"
	ran, err := runRule(t, rulesRunner("", nil), "git", "commit", "-m", msg)
	ruleRan(t, ran, err)
	ran, err = runRule(t, rulesRunner("baryovm.housestyle true", nil), "git", "commit", "-m", msg)
	ruleRefused(t, ran, err, "house-em-dash")
	ran, err = runRule(t, rulesRunner("baryovm.housestyle true", nil), "git", "commit", "-m", "Add login.")
	ruleRefused(t, ran, err, "house-subject-period")
}

// Rule 5

func TestRepoCreateNeedsExplicitVisibility(t *testing.T) {
	ran, err := runRule(t, rulesRunner("", nil), "gh", "repo", "create", "o/new")
	ruleRefused(t, ran, err, "no default")
	ran, err = runRule(t, rulesRunner("", nil), "gh", "repo", "create", "o/new", "--private")
	ruleRan(t, ran, err)
	ran, err = runRule(t, rulesRunner("", nil), "gh", "repo", "create", "o/new", "--public")
	ruleRan(t, ran, err)
	ran, err = runRule(t, rulesRunner("baryovm.rules.off visibility", nil), "gh", "repo", "create", "o/new")
	ruleRan(t, ran, err)
}

func TestRepoCreateFollowsThePolicyVisibility(t *testing.T) {
	ran, err := runRule(t, rulesRunner("baryovm.visibility private", nil), "gh", "repo", "create", "o/new", "--public")
	ruleRefused(t, ran, err, "policy names private")
	ran, err = runRule(t, rulesRunner("baryovm.visibility private", nil), "gh", "repo", "create", "o/new")
	ruleRefused(t, ran, err, "pass --private")
	ran, err = runRule(t, rulesRunner("baryovm.visibility private", nil), "gh", "repo", "create", "o/new", "--private")
	ruleRan(t, ran, err)
}

// Rule 6

func gateEnv(t *testing.T, code int) *int {
	t.Helper()
	old := guardEnv
	t.Cleanup(func() { guardEnv = old })
	calls := 0
	guardEnv = func(confirm string) guard.Env {
		return guard.Env{Confirm: confirm, RunGate: func(string) (int, error) { calls++; return code, nil }}
	}
	return &calls
}

func TestGateRunsBeforePush(t *testing.T) {
	head := map[string]string{"rev-parse --verify -q HEAD^{commit}": "abc123def456\n", "rev-parse --verify -q feature^{commit}": "abc123def456\n"}
	cfg := "baryovm.gate scripts/gate.sh"

	calls := gateEnv(t, 0)
	r := rulesRunner(cfg, head)
	ran, err := runRule(t, r, "git", "push", "origin", "feature")
	ruleRan(t, ran, err)
	if *calls != 1 {
		t.Fatalf("gate ran %d times", *calls)
	}
	recorded := false
	for _, c := range r.calls {
		if c == "config --add baryovm.gatepass abc123def456" {
			recorded = true
		}
	}
	if !recorded {
		t.Fatalf("pass not recorded: %v", r.calls)
	}

	calls = gateEnv(t, 1)
	ran, err = runRule(t, rulesRunner(cfg, head), "git", "push", "origin", "feature")
	ruleRefused(t, ran, err, "gate scripts/gate.sh failed")
	_ = calls
}

func TestGateRecordedPassForTheSameCommitSkipsTheRun(t *testing.T) {
	cfg := "baryovm.gate scripts/gate.sh"
	calls := gateEnv(t, 1)
	head := map[string]string{"rev-parse --verify -q HEAD^{commit}": "abc123\n", "rev-parse --verify -q feature^{commit}": "abc123\n", "config --get-all baryovm.gatepass": "abc123\n"}
	ran, err := runRule(t, rulesRunner(cfg, head), "git", "push", "origin", "feature")
	ruleRan(t, ran, err)
	if *calls != 0 {
		t.Fatal("ran the gate despite a recorded pass")
	}
	// A pass recorded for another commit does not count.
	head["config --get-all baryovm.gatepass"] = "old\n"
	ran, err = runRule(t, rulesRunner(cfg, head), "git", "push", "origin", "feature")
	ruleRefused(t, ran, err, "failed")
}

func TestGateRefusesWithoutARunnerOrPass(t *testing.T) {
	old := guardEnv
	t.Cleanup(func() { guardEnv = old })
	guardEnv = func(confirm string) guard.Env { return guard.Env{Confirm: confirm} }
	ran, err := runRule(t, rulesRunner("baryovm.gate g.sh", map[string]string{"rev-parse --verify -q HEAD^{commit}": "abc\n", "rev-parse --verify -q feature^{commit}": "abc\n"}), "git", "push", "origin", "feature")
	ruleRefused(t, ran, err, "no recorded pass")
}

func TestGateRuleCanBeOff(t *testing.T) {
	calls := gateEnv(t, 1)
	ran, err := runRule(t, rulesRunner("baryovm.gate g.sh\nbaryovm.rules.off gate", map[string]string{"rev-parse --verify -q HEAD^{commit}": "abc\n", "rev-parse --verify -q feature^{commit}": "abc\n"}), "git", "push", "origin", "feature")
	ruleRan(t, ran, err)
	if *calls != 0 {
		t.Fatal("gate ran while off")
	}
}

// Rule 7

func readAudit(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(os.Getenv("BARYOVM_HOME"), "activity.jsonl"))
	if err != nil {
		return ""
	}
	return string(b)
}

func TestGuardedCommandsAreAudited(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BARYOVM_HOME", home)
	t.Setenv("BARYOVM_ACTIVITY", "")
	token := "gh" + "p_" + strings.Repeat("a", 36)
	withGuardSeams(t, rulesRunner("", nil), 0)
	// a pass, a refusal, and a remote URL that carries a token
	for _, args := range [][]string{
		{"git", "commit", "-m", "ok"},
		{"git", "push", "origin", "main"},
		{"git", "push", "https://u:" + token + "@github.com/o/r.git", "feature"},
		{"git", "push", "https://u:hunter two%zz@github.com/o/r.git", "feature"},
		{"gh", "repo", "create", "o/new"},
	} {
		_ = runGroup(t, args...)
	}
	log := readAudit(t)
	lines := strings.Split(strings.TrimSpace(log), "\n")
	if len(lines) != 5 {
		t.Fatalf("want 5 audit lines, got %d:\n%s", len(lines), log)
	}
	for _, want := range []string{`"action":"git commit"`, `"action":"git push"`, `"target":"main"`, `"target":"https://github.com/o/r.git feature"`, `"ok":false`, `"ok":true`, `"action":"gh repo create"`, "refused by protected-branches"} {
		if !strings.Contains(log, want) {
			t.Errorf("audit log lacks %s:\n%s", want, log)
		}
	}
	if strings.Contains(log, token) || strings.Contains(log, "u:") || strings.Contains(log, "hunter") || strings.Contains(log, "two%") {
		t.Fatalf("a secret reached the audit log:\n%s", log)
	}
}

func TestAuditCanBeOff(t *testing.T) {
	ran, err := runRule(t, rulesRunner("baryovm.rules.off audit", nil), "git", "commit", "-m", "ok")
	ruleRan(t, ran, err)
	if got := readAudit(t); got != "" {
		t.Fatalf("audited while off: %s", got)
	}
	// and it is on by default
	ran, err = runRule(t, rulesRunner("", nil), "git", "commit", "-m", "ok")
	ruleRan(t, ran, err)
	if readAudit(t) == "" {
		t.Fatal("not audited by default")
	}
}

// Security review fixes

func TestProtectedBranchFailsClosedOnUnclearDestinations(t *testing.T) {
	for _, args := range [][]string{
		{"git", "push", "origin", "refs/heads/*:refs/heads/*"},
		{"git", "push", "origin", "+refs/heads/*:refs/heads/*"},
		{"git", "push", "origin", ":"},
		{"git", "push", "origin", "+:"},
		{"git", "push", "origin", ""},
		{"git", "push", "origin", "feature:"},
	} {
		ran, err := runRule(t, rulesRunner("", nil), args...)
		ruleRefused(t, ran, err, "cannot tell")
		if !strings.Contains(err.Error(), "baryovm.rules.off") {
			t.Errorf("%v: no way to pass: %v", args, err)
		}
	}
	bare := map[string]map[string]string{
		"remote push config": {"config --get-all remote.origin.push": "refs/heads/*:refs/heads/*\n"},
		"push.default":       {"config --get push.default": "matching\n"},
		"detached head":      {"symbolic-ref --short -q HEAD": ""},
		"upstream is main":   {"config --get branch.feature.merge": "refs/heads/main\n"},
	}
	for name, a := range bare {
		ran, err := runRule(t, rulesRunner("", a), "git", "push")
		if name == "upstream is main" {
			ruleRefused(t, ran, err, "main")
		} else {
			ruleRefused(t, ran, err, "cannot tell")
		}
		ran, err = runRule(t, rulesRunner("baryovm.rules.off protected-branches", a), "git", "push")
		ruleRan(t, ran, err)
	}
	// Plain settings still pass.
	ran, err := runRule(t, rulesRunner("", map[string]string{"config --get push.default": "simple\n"}), "git", "push")
	ruleRan(t, ran, err)
}

func TestGateChecksTheCommitBeingPushed(t *testing.T) {
	cfg := "baryovm.gate scripts/gate.sh"
	a := map[string]string{
		"rev-parse --verify -q HEAD^{commit}":  "aaaa1111\n",
		"rev-parse --verify -q other^{commit}": "bbbb2222\n",
		"config --get-all baryovm.gatepass":    "aaaa1111\n",
	}
	calls := gateEnv(t, 0)
	// The pass is for HEAD; pushing another branch sends a commit with no pass, and the gate
	// (which checks the work tree) is not run for it.
	ran, err := runRule(t, rulesRunner(cfg, a), "git", "push", "origin", "other")
	ruleRefused(t, ran, err, "bbbb2222")
	ran, err = runRule(t, rulesRunner(cfg, a), "git", "push", "origin", "other:feature")
	ruleRefused(t, ran, err, "bbbb2222")
	if *calls != 0 {
		t.Fatal("ran the gate for a commit that is not checked out")
	}
	// Two refspecs: one with a pass, one without.
	ran, err = runRule(t, rulesRunner(cfg, a), "git", "push", "origin", "HEAD:feature", "other:x")
	ruleRefused(t, ran, err, "bbbb2222")
	// A name that does not resolve is refused.
	ran, err = runRule(t, rulesRunner(cfg, a), "git", "push", "origin", "nosuch")
	ruleRefused(t, ran, err, "cannot resolve nosuch")
	// With a recorded pass for each, it goes.
	a["config --get-all baryovm.gatepass"] = "aaaa1111\nbbbb2222\n"
	ran, err = runRule(t, rulesRunner(cfg, a), "git", "push", "origin", "other", "HEAD:feature")
	ruleRan(t, ran, err)
}

func TestProtectedBranchResolvesRefNamesLikeGit(t *testing.T) {
	onMain := map[string]string{"symbolic-ref --short -q HEAD": "main\n"}
	detached := map[string]string{"symbolic-ref --short -q HEAD": ""}
	cases := []struct {
		name    string
		answers map[string]string
		args    []string
	}{
		{"full ref", nil, []string{"git", "push", "origin", "HEAD:refs/heads/main"}},
		{"heads/ spelling", nil, []string{"git", "push", "origin", "HEAD:heads/main"}},
		{"+heads/ spelling", nil, []string{"git", "push", "origin", "+HEAD:heads/main"}},
		{"@ source", nil, []string{"git", "push", "origin", "@:main"}},
		{"@ destination", onMain, []string{"git", "push", "origin", "feature:@"}},
		{"bare @ on main", onMain, []string{"git", "push", "origin", "@"}},
		{"bare HEAD on main", onMain, []string{"git", "push", "origin", "HEAD"}},
		{"symbolic name resolves to main", map[string]string{"rev-parse --symbolic-full-name hotfix": "refs/heads/main\n"}, []string{"git", "push", "origin", "hotfix"}},
		{"bare @ detached", detached, []string{"git", "push", "origin", "@"}},
	}
	for _, c := range cases {
		ran, err := runRule(t, rulesRunner("", c.answers), c.args...)
		if err == nil || len(ran) != 0 {
			t.Errorf("%s: want a refusal, got err=%v ran=%v", c.name, err, ran)
			continue
		}
		if !strings.Contains(err.Error(), "baryovm.rules.off") {
			t.Errorf("%s: refusal does not say how to pass: %v", c.name, err)
		}
		ran, err = runRule(t, rulesRunner("baryovm.rules.off protected-branches", c.answers), c.args...)
		ruleRan(t, ran, err)
	}
}

func TestPushTakesTheFirstPositionalAsTheRemote(t *testing.T) {
	// git reads "main" as the remote here and "feature" as the refspec.
	ran, err := runRule(t, rulesRunner("", nil), "git", "push", "--repo=other", "main", "feature")
	ruleRan(t, ran, err)
	// With --repo and a refspec for main, the refspec is still checked.
	ran, err = runRule(t, rulesRunner("", nil), "git", "push", "--repo=other", "origin", "HEAD:main")
	ruleRefused(t, ran, err, "main")
}

func TestRenamedFileIsCheckedByTheFilesRule(t *testing.T) {
	// Only the filter that includes renames reports this file.
	a := map[string]string{"diff --cached --name-only --diff-filter=ACMR": ".env\n"}
	ran, err := runRule(t, rulesRunner("", a), "git", "commit", "-m", "rn")
	ruleRefused(t, ran, err, ".env")
	ran, err = runRule(t, rulesRunner("baryovm.rules.off files", a), "git", "commit", "-m", "rn")
	ruleRan(t, ran, err)
}

func TestGhBypassesAreRefused(t *testing.T) {
	for _, args := range [][]string{
		{"gh", "alias", "set", "rd", "repo delete"},
		{"gh", "alias", "import", "aliases.yml"},
		{"gh", "api", "-X", "DELETE", "repos/o/r"},
		{"gh", "api", "--method=DELETE", "repos/o/r"},
		{"gh", "api", "-XPATCH", "repos/o/r"},
		{"gh", "api", "repos/o/r/issues", "-f", "title=x"},
		{"gh", "api", "repos/o/r/issues", "--field", "title=x"},
		{"gh", "api", "graphql", "-F", "query=@q.graphql"},
		{"gh", "api", "repos/o/r", "--input", "body.json"},
	} {
		ran, err := runRule(t, rulesRunner("", nil), args...)
		ruleRefused(t, ran, err, "baryovm.rules.off")
		ran, err = runRule(t, rulesRunner("baryovm.rules.off destructive", nil), args...)
		ruleRan(t, ran, err)
	}
	for _, args := range [][]string{
		{"gh", "api", "repos/o/r"},
		{"gh", "api", "-X", "GET", "search/issues", "-f", "q=x"},
		{"gh", "alias", "list"},
	} {
		ran, err := runRule(t, rulesRunner("", nil), args...)
		ruleRan(t, ran, err)
	}
}

func TestGhMergeAndCloseTextIsScanned(t *testing.T) {
	for _, args := range [][]string{
		{"gh", "pr", "merge", "1", "--body", fxTrailer},
		{"gh", "pr", "merge", "1", "-b", fxTrailer},
		{"gh", "pr", "merge", "1", "--subject", fxTrailer},
		{"gh", "issue", "close", "1", "-c", fxTrailer},
		{"gh", "issue", "close", "1", "--comment", fxTrailer},
	} {
		ran, err := runRule(t, rulesRunner("", nil), args...)
		ruleRefused(t, ran, err, "baryovm.rules.off")
		ran, err = runRule(t, rulesRunner("baryovm.rules.off text", nil), args...)
		ruleRan(t, ran, err)
	}
	ran, err := runRule(t, rulesRunner("", nil), "gh", "pr", "merge", "1", "--squash", "-b", "plain text")
	ruleRan(t, ran, err)
}

func TestQuotedAliasRunsWithTheOriginalWords(t *testing.T) {
	a := map[string]string{"config --get alias.c": `commit -m "feat: x"` + "\n"}
	ran, err := runRule(t, rulesRunner("", a), "git", "c")
	ruleRan(t, ran, err)
	if len(ran) != 1 || ran[0] != "git c" {
		t.Fatalf("the alias was rewritten: %v", ran)
	}
	// The guard still reads through the alias: a trailer in its quoted message is refused.
	a = map[string]string{"config --get alias.c": `commit -m "x` + "\n\n" + fxTrailer + `"` + "\n"}
	ran, err = runRule(t, rulesRunner("", a), "git", "c")
	ruleRefused(t, ran, err, "")
}

// globalsRecorder remembers the globals each git read was given.
type globalsRecorder struct {
	*fakeGuardRunner
	globals [][]string
}

func (r *globalsRecorder) Git(globals []string, args ...string) (string, error) {
	r.globals = append(r.globals, globals)
	return r.fakeGuardRunner.Git(globals, args...)
}

func TestRulesAndPolicyAreReadWithoutDashC(t *testing.T) {
	rr := &globalsRecorder{fakeGuardRunner: rulesRunner("", nil)}
	old := guardRunner
	guardRunner = rr
	t.Cleanup(func() { guardRunner = old })
	for _, load := range []func() error{
		func() error { _, err := loadRules([]string{"-C", "/r", "-c", "user.name=x"}); return err },
		func() error { _, err := loadPolicy([]string{"-C", "/r", "-c", "user.name=x"}); return err },
		func() error { _, err := loadRegistryPolicy([]string{"-C", "/r", "-cuser.name=x"}); return err },
	} {
		rr.globals = nil
		if err := load(); err != nil {
			t.Fatal(err)
		}
		if len(rr.globals) == 0 {
			t.Fatal("no git read happened")
		}
		for _, g := range rr.globals {
			if strings.Join(g, " ") != "-C /r" {
				t.Errorf("the read was given %q", g)
			}
		}
	}
}

func TestCleanGitEnvDropsInjectedConfig(t *testing.T) {
	got := cleanGitEnv([]string{"PATH=/bin", "GIT_CONFIG_PARAMETERS='x'", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=a", "GIT_CONFIG_VALUE_0=b", "GIT_CONFIG=/tmp/x.cfg", "GIT_CONFIG_GLOBAL=/dev/null"})
	if strings.Join(got, " ") != "PATH=/bin GIT_CONFIG_GLOBAL=/dev/null" {
		t.Fatalf("got %q", got)
	}
}

func TestInjectedConfigEnvIsRefused(t *testing.T) {
	for _, env := range []string{"GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0", "GIT_CONFIG"} {
		t.Setenv(env, "x")
		ran, err := runRule(t, rulesRunner("", nil), "git", "push", "origin", "feature")
		ruleRefused(t, ran, err, env)
		t.Setenv(env, "")
		os.Unsetenv(env)
	}
}
