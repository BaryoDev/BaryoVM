// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package guard

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

type exit1 struct{}

func (exit1) Error() string { return "exit 1" }
func (exit1) ExitCode() int { return 1 }

func TestAliasIsReclassified(t *testing.T) {
	staged := map[string]string{"diff --cached": plantedDiff()}
	t.Run("plain alias to commit scans the staged diff", func(t *testing.T) {
		f := &fake{answers: map[string]string{"diff --cached": plantedDiff(), "config --get alias.ci": "commit\n"}}
		v := CheckGit(f, []string{"ci", "-m", "x"}, Policy{})
		if v.Allowed {
			t.Fatal("git ci with alias.ci=commit passed the key scan")
		}
	})
	t.Run("alias with options and a chain", func(t *testing.T) {
		f := &fake{answers: map[string]string{"diff --cached": plantedDiff(), "config --get alias.c2": "ci -a\n", "config --get alias.ci": "commit\n"}}
		if v := CheckGit(f, []string{"c2", "-m", "x"}, Policy{}); v.Allowed {
			t.Fatal("alias chain passed")
		}
	})
	t.Run("alias to push checks the host", func(t *testing.T) {
		f := &fake{answers: map[string]string{"remote get-url": "https://evil.example/o/r\n", "config --get alias.up": "push\n"}}
		if v := CheckGit(f, []string{"up", "origin", "main"}, ParsePolicy([]string{"github.com"})); v.Allowed {
			t.Fatal("push alias bypassed the host policy")
		}
	})
	t.Run("shell alias running commit or push is refused", func(t *testing.T) {
		for _, exp := range []string{"!git commit -a", "!f() { git push \"$@\"; }; f"} {
			f := &fake{answers: map[string]string{"config --get alias.x": exp + "\n"}}
			if v := CheckGit(f, []string{"x"}, Policy{}); v.Allowed {
				t.Fatalf("%q passed", exp)
			}
		}
	})
	t.Run("every shell alias is refused unless the policy lists it", func(t *testing.T) {
		for _, exp := range []string{"!echo hi", `!f(){ git "c""ommit"; }; f`, "!git $1", "!git ${0}mmit"} {
			f := &fake{answers: map[string]string{"config --get alias.x": exp + "\n"}}
			v := CheckGit(f, []string{"x", "-m", "m"}, Policy{})
			if v.Allowed || !strings.Contains(v.Reason, "allowShellAliases") {
				t.Fatalf("%q: %+v", exp, v)
			}
			if v := CheckGit(f, []string{"x"}, Policy{AllowShellAliases: []string{"x"}}); !v.Allowed {
				t.Fatalf("%q listed but refused: %s", exp, v.Reason)
			}
		}
	})
	t.Run("an alias that cannot be resolved is refused", func(t *testing.T) {
		f := &fake{errs: map[string]error{"config --get alias.x": fmt.Errorf("boom")}}
		if v := CheckGit(f, []string{"x"}, Policy{}); v.Allowed {
			t.Fatal("unresolvable alias passed")
		}
	})
	t.Run("a word that is no alias passes", func(t *testing.T) {
		f := &fake{errs: map[string]error{"config --get alias.nosuch": exit1{}}}
		if v := CheckGit(f, []string{"nosuch"}, Policy{}); !v.Allowed {
			t.Fatalf("refused: %s", v.Reason)
		}
	})
	t.Run("a builtin is never looked up", func(t *testing.T) {
		f := &fake{answers: staged}
		CheckGit(f, []string{"status"}, Policy{})
		for _, c := range f.calls {
			if strings.Contains(c, "alias.") {
				t.Fatalf("looked up an alias for status: %v", f.calls)
			}
		}
	})
	t.Run("-c alias and --config-env globals are refused", func(t *testing.T) {
		for _, args := range [][]string{
			{"-c", "alias.ci=commit", "ci"},
			{"-calias.ci=commit", "ci"},
			{"-c", "ALIAS.ci=commit", "status"},
			{"--config-env=alias.ci=X", "ci"},
			{"--config-env", "alias.ci=X", "ci"},
		} {
			if v := CheckGit(&fake{}, args, Policy{}); v.Allowed {
				t.Fatalf("%v passed", args)
			}
		}
		if v := CheckGit(&fake{}, []string{"-c", "user.name=x", "status"}, Policy{}); !v.Allowed {
			t.Fatalf("an ordinary -c was refused: %s", v.Reason)
		}
	})
}

func TestCommitPathspecFromFileIsRefused(t *testing.T) {
	for _, args := range [][]string{
		{"commit", "--pathspec-from-file", "paths.txt", "-m", "x"},
		{"commit", "--pathspec-from-file=paths.txt", "-m", "x"},
		{"commit", "--pathspec-from-file", "-", "-m", "x"},
		{"commit", "--pathspec-from", "paths.txt", "-m", "x"},
	} {
		v := CheckGit(&fake{}, args, Policy{})
		if v.Allowed || !strings.Contains(v.Reason, "as arguments") {
			t.Fatalf("%v: %+v", args, v)
		}
	}
}

func TestParseRulesAndConfirm(t *testing.T) {
	ru := ParseRules([]string{
		"baryovm.rules.off files, text",
		"baryovm.protectedbranches release",
		"baryovm.forceleasebranches wip",
		"baryovm.maxfilesize 5M",
		"baryovm.visibility Private",
		"baryovm.gate scripts/gate.sh",
		"baryovm.housestyle true",
	}, "gate")
	if ru.On(RuleFiles) || ru.On(RuleText) || ru.On(RuleGate) || !ru.On(RuleProtected) {
		t.Fatalf("off: %v", ru.Off)
	}
	if ru.MaxFileSize != 5<<20 || ru.Visibility != "private" || ru.Gate != "scripts/gate.sh" || !ru.HouseStyle ||
		!reflect.DeepEqual(ru.ProtectedBranches, []string{"release"}) {
		t.Fatalf("%+v", ru)
	}
	if ParseRules(nil, "").MaxFileSize != DefaultMaxFileSize {
		t.Fatal("default size")
	}
	rest, c := ExtractConfirm([]string{"repo", "delete", "--confirm", "o/r", "o/r", "--", "--confirm"})
	if c != "o/r" || !reflect.DeepEqual(rest, []string{"repo", "delete", "o/r", "--", "--confirm"}) {
		t.Fatalf("%v %q", rest, c)
	}
	if _, c := ExtractConfirm([]string{"--confirm=x"}); c != "x" {
		t.Fatal("= form")
	}
}

func TestCheckText(t *testing.T) {
	bad := []string{
		"Co-Authored" + "-By: Claude <noreply@" + "anthropic.com>",
		"co-authored" + "-by: ChatGPT <x@openai.com>",
		"Generated " + "with Claude Code",
		"\U0001F916 Generated " + "with [Claude Code](https://claude.com/claude-code)",
		"Generated " + "by AI",
	}
	for _, b := range bad {
		if len(CheckText(Rules{}, "m", "subject\n\n"+b, true)) == 0 {
			t.Errorf("not caught: %q", b)
		}
	}
	for _, ok := range []string{"Co-Authored" + "-By: Alice Smith <alice@example.com>", "the generated file is checked in", "Add a claude client"} {
		if fs := CheckText(Rules{}, "m", "subject\n\n"+ok, true); len(fs) != 0 {
			t.Errorf("false positive: %q %v", ok, fs)
		}
	}
	long := strings.Repeat("a", 80)
	if len(CheckText(Rules{}, "m", long+".", true)) != 0 {
		t.Error("house style applied without opt-in")
	}
	if got := CheckText(Rules{HouseStyle: true}, "m", long+".", true); len(got) != 2 {
		t.Errorf("house style: %v", got)
	}
}

func TestSafeTargetDropsCredentialsEvenWhenTheURLDoesNotParse(t *testing.T) {
	cases := map[string]string{
		"https://u:p%zz@host/o/r":     "https://host/o/r",
		"https://u:p@ss@host/o/r":     "https://host/o/r",
		"https://u:pa/ss@host/x":      "[redacted]",
		"https://user:tok en@host/x":  "https://host/x",
		"user:pa ss@host:o/r":         "host:o/r",
		"origin https://u:p%zz w@h/x": "origin https://h/x",
		"user:pass@host:o/r.git":      "host:o/r.git",
		"git@github.com:o/r.git":      "github.com:o/r.git",
		"https://github.com/o/r.git":  "https://github.com/o/r.git",
		"origin feature":              "origin feature",
		"ssh://git:secretpw@h:22/o/r": "ssh://h:22/o/r",
	}
	for in, want := range cases {
		got := SafeTarget(in)
		if got != want {
			t.Errorf("SafeTarget(%q) = %q, want %q", in, got, want)
		}
		for _, leak := range []string{"secretpw", "pass@", "p%zz", "u:p"} {
			if strings.Contains(got, leak) {
				t.Errorf("SafeTarget(%q) leaked %q: %q", in, leak, got)
			}
		}
	}
}

func TestAliasWordsFollowGitQuoting(t *testing.T) {
	cases := map[string][]string{
		`commit -m "feat: x"`: {"commit", "-m", "feat: x"},
		`commit -m 'a b' -s`:  {"commit", "-m", "a b", "-s"},
		`commit -m a\ b`:      {"commit", "-m", "a b"},
		`  push   origin  `:   {"push", "origin"},
		`commit -m ""`:        {"commit", "-m", ""},
	}
	for in, want := range cases {
		got := aliasWords(in)
		if strings.Join(got, "\x00") != strings.Join(want, "\x00") || len(got) != len(want) {
			t.Errorf("aliasWords(%q) = %q, want %q", in, got, want)
		}
	}
}
