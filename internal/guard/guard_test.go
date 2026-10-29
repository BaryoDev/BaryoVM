// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package guard

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Fake keys are built from pieces so this file does not trip the scanner that guards this repo.
var (
	fakeGoogle = "AI" + "za" + strings.Repeat("A", 35)
	fakeGitHub = "gh" + "p_" + strings.Repeat("a", 36)
	fakePAT    = "github" + "_pat_" + strings.Repeat("a", 22)
	fakeAWS    = "AK" + "IA" + strings.Repeat("A", 16)
	fakeSlack  = "xo" + "xb-" + "1234567890abc"
	fakeStripe = "sk" + "_live_" + strings.Repeat("a", 24)
	fakeNPM    = "np" + "m_" + strings.Repeat("a", 36)
	fakePEM    = "-----BEGIN RSA " + "PRIVATE KEY-----"
)

func TestScanLine(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string // rule name, or "" for no finding
	}{
		{"google key", `const k = "` + fakeGoogle + `"`, "google-api-key"},
		{"google key too short", "AI" + "za" + strings.Repeat("A", 20), ""},
		{"github token", "token=" + fakeGitHub, "github-token"},
		{"github token too short", "gh" + "p_" + strings.Repeat("a", 10), ""},
		{"github pat", fakePAT, "github-pat"},
		{"github pat too short", "github" + "_pat_abc", ""},
		{"aws key", "id = " + fakeAWS, "aws-access-key"},
		{"aws session key", "id = AS" + "IA" + strings.Repeat("B", 16), "aws-access-key"},
		{"aws doc example", "id = AK" + "IAIOSFODNN7EXAMPLE", ""},
		{"aws lowercase", "akia" + strings.Repeat("a", 16), ""},
		{"aws inside a word", "xAK" + "IA" + strings.Repeat("A", 16) + "x", ""},
		{"slack token", fakeSlack, "slack-token"},
		{"slack too short", "xo" + "xb-123", ""},
		{"stripe live", fakeStripe, "stripe-live-key"},
		{"stripe restricted live", "rk" + "_live_" + strings.Repeat("a", 24), "stripe-live-key"},
		{"stripe test key", "sk" + "_test_" + strings.Repeat("a", 24), ""},
		{"npm token", fakeNPM, "npm-token"},
		{"npm token too short", "np" + "m_" + strings.Repeat("a", 10), ""},
		{"private key", fakePEM, "private-key"},
		{"openssh private key", "-----BEGIN OPENSSH " + "PRIVATE KEY-----", "private-key"},
		{"public key", "-----BEGIN PUBLIC " + "KEY-----", ""},
		{"pgp private key", "-----BEGIN PGP " + "PRIVATE KEY BLOCK-----", "pgp-private-key"},
		{"pgp public key", "-----BEGIN PGP " + "PUBLIC KEY BLOCK-----", ""},
		{"anthropic key", "k=sk" + "-ant-" + strings.Repeat("a", 24), "anthropic-key"},
		{"anthropic key too short", "sk" + "-ant-short", ""},
		{"openai project key", "k=sk" + "-proj-" + strings.Repeat("a", 24), "openai-key"},
		{"openai project key too short", "sk" + "-proj-abc", ""},
		{"openai classic key", "k=sk" + "-" + strings.Repeat("a", 24), "openai-key"},
		{"sk word too short", "sk" + "-abc", ""},
		{"task word not a key", "task" + "-" + strings.Repeat("a", 24), ""},
		{"allow marker", fakeGoogle + " // allow-secret", ""},
		{"plain text", "nothing to see here", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ScanLine(tc.line)
			if tc.want == "" {
				if len(got) != 0 {
					t.Fatalf("want no finding, got %v", got)
				}
				return
			}
			if len(got) != 1 || got[0] != tc.want {
				t.Fatalf("want [%s], got %v", tc.want, got)
			}
		})
	}
}

func TestScanTextReportsLineNotValue(t *testing.T) {
	fs := ScanText("notes.md", "one\ntwo\nkey: "+fakeGoogle+"\n")
	if len(fs) != 1 || fs[0].File != "notes.md" || fs[0].Line != 3 {
		t.Fatalf("got %v", fs)
	}
	if strings.Contains(fs[0].String(), fakeGoogle) {
		t.Fatal("finding prints the key")
	}
}

const sampleDiff = `diff --git a/src/app.js b/src/app.js
index 111..222 100644
--- a/src/app.js
+++ b/src/app.js
@@ -10,0 +11,3 @@ func
+const a = 1
+const k = "KEY"
+// done
@@ -40,2 +43,2 @@
 context
-old KEY
+++ looks like a header but is content
diff --git a/old.txt b/old.txt
deleted file mode 100644
--- a/old.txt
+++ /dev/null
@@ -1 +0,0 @@
-KEY
diff --git a/b.env b/b.env
--- /dev/null
+++ b/b.env
@@ -0,0 +1 @@
+TOKEN=KEY
`

func TestScanDiffNamesFileAndLine(t *testing.T) {
	diff := strings.ReplaceAll(sampleDiff, "KEY", fakeGoogle)
	fs := ScanDiff(diff)
	want := []Finding{
		{File: "src/app.js", Line: 12, Rule: "google-api-key"},
		{File: "b.env", Line: 1, Rule: "google-api-key"},
	}
	if len(fs) != len(want) {
		t.Fatalf("got %v, want %v", fs, want)
	}
	for i := range want {
		if fs[i] != want[i] {
			t.Errorf("finding %d: got %v, want %v", i, fs[i], want[i])
		}
	}
}

func TestScanDiffAddedLineStartingWithPlusPlus(t *testing.T) {
	// The added line "++x KEY" shows up in the diff as "+++x KEY", which must not be read as a
	// file header inside a hunk.
	diff := "diff --git a/x.txt b/x.txt\n--- a/x.txt\n+++ b/x.txt\n@@ -1 +1,2 @@\n ctx\n+++x " + fakeGoogle + "\n"
	fs := ScanDiff(diff)
	if len(fs) != 1 || fs[0].File != "x.txt" || fs[0].Line != 2 {
		t.Fatalf("got %v", fs)
	}
}

func TestHookScriptFailsWithoutBaryovmOnPath(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "hook")
	if err := os.WriteFile(script, []byte(HookScript("pre-commit")), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", script)
	cmd.Env = []string{"PATH="}
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() == 0 {
		t.Fatalf("want a non-zero exit, got %v", err)
	}
	if !strings.Contains(string(out), "not on PATH") {
		t.Fatalf("output: %q", out)
	}
}

func TestScanDiffIgnoresRemovedKey(t *testing.T) {
	diff := "diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-k=" + fakeGoogle + "\n+k=\n"
	if fs := ScanDiff(diff); len(fs) != 0 {
		t.Fatalf("removed key flagged: %v", fs)
	}
}

func TestParseRemoteHost(t *testing.T) {
	tests := []struct {
		raw, want string
		err       bool
	}{
		{"https://github.com/o/r.git", "github.com", false},
		{"https://user:tok@GitHub.com:8443/o/r", "github.com", false},
		{"ssh://git@git.example.org:2222/o/r", "git.example.org", false},
		{"git@github.com:o/r.git", "github.com", false},
		{"github.com:o/r.git", "github.com", false},
		{"/srv/git/r.git", "", false},
		{"../r.git", "", false},
		{"file:///srv/git/r.git", "", false},
		{"", "", true},
		{"https:///nohost", "", true},
	}
	// A single letter before the colon is a drive only on Windows; elsewhere it is host "c".
	if runtime.GOOS == "windows" {
		tests = append(tests, struct {
			raw, want string
			err       bool
		}{"C:\\repos\\r", "", false})
	} else {
		tests = append(tests, struct {
			raw, want string
			err       bool
		}{"C:\\repos\\r", "c", false})
	}
	for _, tc := range tests {
		got, err := ParseRemoteHost(tc.raw)
		if (err != nil) != tc.err || got != tc.want {
			t.Errorf("%q: got %q, %v; want %q, err=%v", tc.raw, got, err, tc.want, tc.err)
		}
	}
}

func TestParseRemoteHostErrorHasNoURLText(t *testing.T) {
	token := "TOK" + "EN" + "SECRET123"
	_, err := ParseRemoteHost("https://user:" + token + "@bad host/x.git")
	if err == nil {
		t.Fatal("want an error for a malformed url")
	}
	if strings.Contains(err.Error(), token) || strings.Contains(err.Error(), "bad host") {
		t.Fatalf("error echoes the url: %v", err)
	}
}

func TestGitDiffAndLogFlagsDefeatGitattributes(t *testing.T) {
	f := &fake{}
	CheckGit(f, []string{"commit", "-m", "x"}, Policy{})
	if len(f.calls) == 0 || !strings.Contains(f.calls[0], "--text") || !strings.Contains(f.calls[0], "--no-textconv") {
		t.Fatalf("commit diff flags: %v", f.calls)
	}
	f = &fake{answers: map[string]string{"remote get-url": "https://github.com/o/r\n"}}
	CheckGit(f, []string{"push", "origin", "main"}, Policy{})
	var log string
	for _, c := range f.calls {
		if strings.HasPrefix(c, "log") {
			log = c
		}
	}
	for _, want := range []string{"--text", "--no-textconv", " -m"} {
		if !strings.Contains(log, want) {
			t.Fatalf("push log lacks %s: %q", want, log)
		}
	}
}

func TestPolicyAllows(t *testing.T) {
	tests := []struct {
		values []string
		host   string
		want   bool
	}{
		{nil, "evil.example", true},
		{[]string{"github.com"}, "github.com", true},
		{[]string{"GitHub.com, dev.azure.com"}, "dev.azure.com", true},
		{[]string{"github.com"}, "gitlab.com", false},
		{[]string{"github.com"}, "notgithub.com", false},
		{[]string{"github.com"}, "", true},
		{[]string{"*.corp.example"}, "git.corp.example", true},
		{[]string{"*.corp.example"}, "corp.example", false},
		{[]string{"*.corp.example"}, "xcorp.example", false},
	}
	for _, tc := range tests {
		if got := ParsePolicy(tc.values).Allows(tc.host); got != tc.want {
			t.Errorf("%v %q: got %v, want %v", tc.values, tc.host, got, tc.want)
		}
	}
}

// fake is a Runner that answers git calls by the first words of the command and records them.
type fake struct {
	answers map[string]string // key: space-joined args
	errs    map[string]error
	files   map[string]string
	calls   []string
}

func (f *fake) Git(globals []string, args ...string) (string, error) {
	key := strings.Join(args, " ")
	f.calls = append(f.calls, strings.Join(append(append([]string{}, globals...), args...), " "))
	for k, err := range f.errs {
		if strings.HasPrefix(key, k) {
			return "", err
		}
	}
	for k, v := range f.answers {
		if strings.HasPrefix(key, k) {
			return v, nil
		}
	}
	return "", nil
}

func (f *fake) ReadFile(p string) ([]byte, error) {
	if s, ok := f.files[p]; ok {
		return []byte(s), nil
	}
	return nil, os.ErrNotExist
}

func plantedDiff() string {
	return "diff --git a/a.js b/a.js\n--- a/a.js\n+++ b/a.js\n@@ -0,0 +4 @@\n+k=\"" + fakeGoogle + "\"\n"
}

func TestCheckGitCommit(t *testing.T) {
	t.Run("staged key refused with file and line only", func(t *testing.T) {
		f := &fake{answers: map[string]string{"diff --cached": plantedDiff()}}
		v := CheckGit(f, []string{"commit", "-m", "x"}, Policy{})
		if v.Allowed || len(v.Findings) != 1 || v.Findings[0].File != "a.js" || v.Findings[0].Line != 4 {
			t.Fatalf("got %+v", v)
		}
		if strings.Contains(v.Reason, fakeGoogle) || !strings.Contains(v.Reason, "a.js:4") {
			t.Fatalf("reason: %s", v.Reason)
		}
	})
	t.Run("clean passes", func(t *testing.T) {
		if v := CheckGit(&fake{}, []string{"commit", "-m", "x"}, Policy{}); !v.Allowed {
			t.Fatalf("got %+v", v)
		}
	})
	t.Run("working diff only with -a", func(t *testing.T) {
		f := &fake{answers: map[string]string{"diff -U0": plantedDiff()}}
		if v := CheckGit(f, []string{"commit", "-m", "x"}, Policy{}); !v.Allowed {
			t.Fatalf("working diff scanned without -a: %+v", v)
		}
		for _, args := range [][]string{{"commit", "-a", "-m", "x"}, {"commit", "-am", "x"}, {"commit", "--all", "-m", "x"}} {
			if v := CheckGit(f, args, Policy{}); v.Allowed {
				t.Errorf("%v: working key not found", args)
			}
		}
	})
	t.Run("-m value containing a is not -a", func(t *testing.T) {
		f := &fake{answers: map[string]string{"diff -U0": plantedDiff()}}
		if v := CheckGit(f, []string{"commit", "-m", "-a"}, Policy{}); !v.Allowed {
			t.Fatalf("message value read as a flag: %+v", v)
		}
	})
	t.Run("paths take the working tree content", func(t *testing.T) {
		f := &fake{answers: map[string]string{"diff -U0": plantedDiff()}}
		if v := CheckGit(f, []string{"commit", "-m", "x", "a.js"}, Policy{}); v.Allowed {
			t.Fatal("path commit not scanned")
		}
	})
	t.Run("key in the message", func(t *testing.T) {
		if v := CheckGit(&fake{}, []string{"commit", "-m", "use " + fakeGitHub}, Policy{}); v.Allowed {
			t.Fatal("message key not found")
		}
	})
	t.Run("global -C is kept for the checks", func(t *testing.T) {
		f := &fake{}
		CheckGit(f, []string{"-C", "/repo", "commit", "-m", "x"}, Policy{})
		if len(f.calls) == 0 || !strings.HasPrefix(f.calls[0], "-C /repo diff --cached") {
			t.Fatalf("calls: %v", f.calls)
		}
	})
	t.Run("git failure fails closed", func(t *testing.T) {
		f := &fake{errs: map[string]error{"diff": errors.New("not a git repository")}}
		if v := CheckGit(f, []string{"commit", "-m", "x"}, Policy{}); v.Allowed {
			t.Fatal("allowed after a failed diff")
		}
	})
}

func TestCheckGitPush(t *testing.T) {
	remoteURL := func(u string) map[string]string { return map[string]string{"remote get-url --push": u + "\n"} }

	t.Run("host outside the policy fails before any log", func(t *testing.T) {
		f := &fake{answers: remoteURL("https://evil.example/o/r.git")}
		v := CheckGit(f, []string{"push", "origin", "main"}, ParsePolicy([]string{"github.com"}))
		if v.Allowed || !strings.Contains(v.Reason, "evil.example") || !strings.Contains(v.Reason, "git config --add") {
			t.Fatalf("got %+v", v)
		}
		for _, c := range f.calls {
			if strings.HasPrefix(c, "log") {
				t.Fatalf("scanned history after a host refusal: %v", f.calls)
			}
		}
	})
	t.Run("key in unpushed commits", func(t *testing.T) {
		f := &fake{answers: remoteURL("git@github.com:o/r.git")}
		f.answers["log -p"] = plantedDiff()
		v := CheckGit(f, []string{"push"}, ParsePolicy([]string{"github.com"}))
		if v.Allowed || len(v.Findings) != 1 {
			t.Fatalf("got %+v", v)
		}
	})
	t.Run("log is bounded by the remote", func(t *testing.T) {
		f := &fake{answers: remoteURL("https://github.com/o/r")}
		CheckGit(f, []string{"push", "origin", "feature"}, Policy{})
		var log string
		for _, c := range f.calls {
			if strings.HasPrefix(c, "log") {
				log = c
			}
		}
		if !strings.Contains(log, " feature --not --remotes=origin") {
			t.Fatalf("log call: %q", log)
		}
	})
	t.Run("clean push passes", func(t *testing.T) {
		f := &fake{answers: remoteURL("https://github.com/o/r")}
		if v := CheckGit(f, []string{"push", "-u", "origin", "main"}, ParsePolicy([]string{"github.com"})); !v.Allowed {
			t.Fatalf("got %+v", v)
		}
	})
	t.Run("delete push scans nothing", func(t *testing.T) {
		f := &fake{answers: remoteURL("https://github.com/o/r")}
		f.answers["log -p"] = plantedDiff()
		if v := CheckGit(f, []string{"push", "origin", ":old"}, Policy{}); !v.Allowed {
			t.Fatalf("got %+v", v)
		}
	})
	t.Run("url given directly", func(t *testing.T) {
		f := &fake{errs: map[string]error{"remote get-url": errors.New("no such remote")}}
		v := CheckGit(f, []string{"push", "https://evil.example/x.git", "main"}, ParsePolicy([]string{"github.com"}))
		if v.Allowed || !strings.Contains(v.Reason, "evil.example") {
			t.Fatalf("got %+v", v)
		}
	})
	t.Run("--repo overrides the remote", func(t *testing.T) {
		f := &fake{errs: map[string]error{"remote get-url": errors.New("no such remote")}}
		v := CheckGit(f, []string{"push", "--repo=https://evil.example/x.git"}, ParsePolicy([]string{"github.com"}))
		if v.Allowed {
			t.Fatalf("got %+v", v)
		}
	})
	t.Run("unknown remote name", func(t *testing.T) {
		f := &fake{errs: map[string]error{"remote get-url": errors.New("no such remote")}}
		if v := CheckGit(f, []string{"push", "nope"}, Policy{}); v.Allowed {
			t.Fatal("allowed an unknown remote")
		}
	})
	t.Run("refspec that looks like a flag is refused", func(t *testing.T) {
		f := &fake{answers: remoteURL("https://github.com/o/r")}
		if v := CheckGit(f, []string{"push", "origin", "--output=x:y"}, Policy{}); !v.Allowed {
			// a leading -- is a flag to push, not a refspec; it must not reach log
			for _, c := range f.calls {
				if strings.Contains(c, "--output") {
					t.Fatalf("flag reached log: %v", f.calls)
				}
			}
		}
		if v := CheckGit(f, []string{"push", "origin", "--", "--output=x:y"}, Policy{}); v.Allowed {
			t.Fatal("dash refspec allowed")
		}
	})
}

func TestOtherGitCommandsPassThrough(t *testing.T) {
	f := &fake{}
	for _, args := range [][]string{{"status"}, {"log", "-p"}, {"pull"}, {"-C", "x", "diff"}, {}} {
		if v := CheckGit(f, args, ParsePolicy([]string{"github.com"})); !v.Allowed {
			t.Errorf("%v refused", args)
		}
	}
	if len(f.calls) != 0 {
		t.Fatalf("ran git for a pass-through command: %v", f.calls)
	}
}

func TestCheckGh(t *testing.T) {
	f := &fake{files: map[string]string{"body.md": "see " + fakeGitHub + "\n", "clean.md": "all good\n"}}
	tests := []struct {
		name string
		args []string
		ok   bool
		file string
	}{
		{"body-file with key", []string{"pr", "create", "--body-file", "body.md"}, false, "body.md"},
		{"body-file equals form", []string{"pr", "create", "--body-file=body.md"}, false, "body.md"},
		{"short -F", []string{"pr", "create", "-F", "body.md"}, false, "body.md"},
		{"notes-file", []string{"release", "create", "v1", "--notes-file", "body.md"}, false, "body.md"},
		{"inline body", []string{"issue", "create", "--body", "k=" + fakeGoogle}, false, "--body"},
		{"inline short body", []string{"issue", "create", "-b", fakeGoogle}, false, "-b"},
		{"inline title", []string{"pr", "create", "--title", fakeGoogle}, false, "--title"},
		{"clean file", []string{"pr", "create", "--body-file", "clean.md"}, true, ""},
		{"stdin refused", []string{"pr", "create", "--body-file", "-"}, false, ""},
		{"missing file refused", []string{"pr", "create", "--body-file", "gone.md"}, false, ""},
		{"other gh command not scanned", []string{"pr", "merge", "--body", fakeGoogle}, true, ""},
		{"pr list", []string{"pr", "list"}, true, ""},
		{"no args", nil, true, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := CheckGh(f, tc.args)
			if v.Allowed != tc.ok {
				t.Fatalf("allowed=%v, want %v: %+v", v.Allowed, tc.ok, v)
			}
			if tc.file != "" && (len(v.Findings) != 1 || v.Findings[0].File != tc.file || v.Findings[0].Line < 1) {
				t.Fatalf("findings: %+v", v.Findings)
			}
			if strings.Contains(v.Reason, fakeGoogle) || strings.Contains(v.Reason, fakeGitHub) {
				t.Fatalf("reason prints the key: %s", v.Reason)
			}
			if !v.Allowed && v.Reason == "" {
				t.Fatal("refusal without a next step")
			}
		})
	}
}

func TestCheckPrePush(t *testing.T) {
	sha := strings.Repeat("a", 40)
	other := strings.Repeat("b", 40)
	f := &fake{answers: map[string]string{"log -p": plantedDiff()}}
	line := "refs/heads/main " + sha + " refs/heads/main " + zeroSHA + "\n"
	if v := CheckPrePush(f, "origin", "https://github.com/o/r", line, Policy{}); v.Allowed {
		t.Fatal("new branch key not found")
	}
	if !strings.Contains(strings.Join(f.calls, "\n"), sha+" --not --remotes=origin") {
		t.Fatalf("calls: %v", f.calls)
	}
	f.calls = nil
	line = "refs/heads/main " + sha + " refs/heads/main " + other + "\n"
	CheckPrePush(f, "origin", "https://github.com/o/r", line, Policy{})
	if !strings.Contains(strings.Join(f.calls, "\n"), other+".."+sha) {
		t.Fatalf("calls: %v", f.calls)
	}
	del := "(delete) " + zeroSHA + " refs/heads/old " + other + "\n"
	if v := CheckPrePush(&fake{answers: map[string]string{"log -p": plantedDiff()}}, "origin", "https://github.com/o/r", del, Policy{}); !v.Allowed {
		t.Fatal("delete refused")
	}
	if v := CheckPrePush(&fake{}, "origin", "https://evil.example/r", "", ParsePolicy([]string{"github.com"})); v.Allowed {
		t.Fatal("host policy skipped in the hook")
	}
}

func TestCheckPreCommit(t *testing.T) {
	if v := CheckPreCommit(&fake{answers: map[string]string{"diff --cached": plantedDiff()}}); v.Allowed {
		t.Fatal("staged key allowed")
	}
	if v := CheckPreCommit(&fake{}); !v.Allowed {
		t.Fatal("clean refused")
	}
}

func TestHookScript(t *testing.T) {
	for _, n := range HookNames {
		s := HookScript(n)
		if !strings.HasPrefix(s, "#!/bin/sh\n") || !IsOurHook(s) || !strings.Contains(s, "exec baryovm guard check "+n+` "$@"`) {
			t.Errorf("%s: %q", n, s)
		}
	}
	if IsOurHook("#!/bin/sh\necho hi\n") {
		t.Error("foreign hook read as ours")
	}
}
