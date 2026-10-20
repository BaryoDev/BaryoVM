// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package sshx

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestQuotePowerShell(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "''"},
		{"simple", "'simple'"},
		{"with space", "'with space'"},
		// Inside single quotes PowerShell expands nothing, so $, backtick and ; stay literal.
		{"$env:USERNAME", "'$env:USERNAME'"},
		{"a`nb", "'a`nb'"},
		{"a; Remove-Item C:\\", "'a; Remove-Item C:\\'"},
		// The one character that ends the string is doubled.
		{"it's", "'it''s'"},
		{"'", "''''"},
		// PowerShell treats the typographic single quotes as quotes too.
		{"a\u2019b", "'a\u2019\u2019b'"},
		{"a\u2018b", "'a\u2018\u2018b'"},
		{"all: ' $x `t ;", "'all: '' $x `t ;'"},
	}
	for _, c := range cases {
		if got := Windows.Quote(c.in); got != c.want {
			t.Errorf("Windows.Quote(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestQuotePOSIXIsUnchanged(t *testing.T) {
	for _, in := range []string{"", "a'b", "$HOME; rm -rf /", "a`b`c"} {
		if got, want := Linux.Quote(in), Quote(in); got != want {
			t.Errorf("Linux.Quote(%q) = %q, want %q", in, got, want)
		}
	}
	if got, want := OS("").Quote("a'b"), Quote("a'b"); got != want {
		t.Errorf("an unset OS must quote as POSIX: got %q, want %q", got, want)
	}
}

const encodedPrefix = "powershell -NoProfile -NonInteractive -EncodedCommand "

// decodeEncoded reads the script back out of a powershell -EncodedCommand line.
func decodeEncoded(t *testing.T, line string) string {
	t.Helper()
	if !strings.HasPrefix(line, encodedPrefix) {
		t.Fatalf("not a powershell command line: %q", line)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(line, encodedPrefix))
	if err != nil {
		t.Fatalf("payload is not base64: %v", err)
	}
	u := make([]uint16, len(raw)/2)
	for i := range u {
		u[i] = uint16(raw[2*i]) | uint16(raw[2*i+1])<<8
	}
	return string(utf16.Decode(u))
}

func TestWindowsScriptTravelsWithoutAnOuterQuotingLayer(t *testing.T) {
	// The line has to mean the same thing whether the host's default shell is cmd.exe or
	// powershell, so nothing in it may be a character either of them interprets.
	script := `Write-Output 'it''s $x; "q" ` + "`t" + ` |&<>%^'`
	line := Windows.Script(script)
	if strings.ContainsAny(strings.TrimPrefix(line, encodedPrefix), " \"'$;`|&<>%^") {
		t.Errorf("payload carries shell syntax: %q", line)
	}
	if got := decodeEncoded(t, line); !strings.HasSuffix(got, script) {
		t.Errorf("script lost in transit: got %q", got)
	}
}

func TestWindowsScriptSilencesProgress(t *testing.T) {
	// Without this a non-interactive powershell writes progress records to stderr as CLIXML.
	if got := decodeEncoded(t, Windows.Script("Get-Date")); !strings.HasPrefix(got, "$ProgressPreference='SilentlyContinue';") {
		t.Errorf("got %q", got)
	}
}

func TestLinuxScriptIsTheCommandAsGiven(t *testing.T) {
	if got := Linux.Script("df -h"); got != "df -h" {
		t.Errorf("got %q", got)
	}
}

func TestWindowsCommandQuotesEachWord(t *testing.T) {
	got := decodeEncoded(t, Windows.Command([]string{`C:\Program Files\app.exe`, "it's", "$x", "a`b;c"}))
	want := `& 'C:\Program Files\app.exe' 'it''s' '$x' 'a` + "`" + `b;c'`
	if !strings.HasSuffix(got, want) {
		t.Errorf("got %q, want suffix %q", got, want)
	}
}

func TestLinuxCommandQuotesEachWord(t *testing.T) {
	if got, want := Linux.Command([]string{"echo", "a'b"}), `'echo' 'a'\''b'`; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestAsAdmin(t *testing.T) {
	if got := Linux.AsAdmin(true, "docker build"); got != "sudo -n docker build" {
		t.Errorf("linux: got %q", got)
	}
	if got := Linux.AsAdmin(false, "docker build"); got != "docker build" {
		t.Errorf("linux, not asked: got %q", got)
	}
	// OpenSSH gives an administrator an elevated session, and there is no sudo to prefix.
	if got := Windows.AsAdmin(true, "docker build"); got != "docker build" {
		t.Errorf("windows: got %q", got)
	}
}

func TestAsAdminShellLinuxIsSudoShell(t *testing.T) {
	if got, want := Linux.AsAdminShell(true, "a && b"), SudoShell(true, "a && b"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := Windows.AsAdminShell(true, "a; b"); got != "a; b" {
		t.Errorf("windows: got %q", got)
	}
}

func TestParseOS(t *testing.T) {
	cases := []struct {
		in      string
		want    OS
		wantErr bool
	}{
		{"linux", Linux, false},
		{"Windows", Windows, false},
		{" windows ", Windows, false},
		{"", Linux, false},
		{"freebsd", "", true},
	}
	for _, c := range cases {
		got, err := ParseOS(c.in)
		if (err != nil) != c.wantErr || got != c.want {
			t.Errorf("ParseOS(%q) = %q, %v", c.in, got, err)
		}
	}
}

func TestResolvedDefaultsToLinux(t *testing.T) {
	if OS("").Resolved() != Linux || Windows.Resolved() != Windows {
		t.Error("an empty OS means linux, anything else is itself")
	}
}

func TestOSFromUname(t *testing.T) {
	cases := []struct {
		in   string
		want OS
		ok   bool
	}{
		{"Linux\n", Linux, true},
		{"MINGW64_NT-10.0-20348\n", Windows, true},
		{"MSYS_NT-10.0-20348", Windows, true},
		{"CYGWIN_NT-10.0", Windows, true},
		{"Darwin\n", "", false},
		{"", "", false},
		{"'uname' is not recognized as an internal or external command", "", false},
	}
	for _, c := range cases {
		got, ok := osFromUname(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("osFromUname(%q) = %q, %v", c.in, got, ok)
		}
	}
}

func TestOSFromPlatform(t *testing.T) {
	if got, ok := osFromPlatform("Win32NT\r\n"); got != Windows || !ok {
		t.Errorf("got %q, %v", got, ok)
	}
	if _, ok := osFromPlatform("Unix"); ok {
		t.Error("Unix is not a Windows answer")
	}
	if _, ok := osFromPlatform(""); ok {
		t.Error("empty output is not an answer")
	}
}

// fakeHost answers by command line: the first matching prefix wins.
type fakeHost struct {
	answers map[string]fakeAnswer
	ran     []string
}

type fakeAnswer struct {
	out string
	err error
}

func (f *fakeHost) Run(cmd string) (string, error) {
	f.ran = append(f.ran, cmd)
	for prefix, a := range f.answers {
		if strings.HasPrefix(cmd, prefix) {
			return a.out, a.err
		}
	}
	return "", errors.New("no such command")
}

func TestDetectOSLinuxStopsAtUname(t *testing.T) {
	h := &fakeHost{answers: map[string]fakeAnswer{"uname -s": {out: "Linux\n"}}}
	got, err := DetectOS(h)
	if err != nil || got != Linux {
		t.Fatalf("got %q, %v", got, err)
	}
	if len(h.ran) != 1 {
		t.Errorf("a Linux host needs one probe, ran %v", h.ran)
	}
}

func TestDetectOSFallsBackToPowerShell(t *testing.T) {
	h := &fakeHost{answers: map[string]fakeAnswer{
		"uname -s":   {err: errors.New("'uname' is not recognized")},
		"powershell": {out: "Win32NT\r\n"},
	}}
	got, err := DetectOS(h)
	if err != nil || got != Windows {
		t.Fatalf("got %q, %v", got, err)
	}
	if len(h.ran) != 2 || !strings.Contains(decodeEncoded(t, h.ran[1]), "OSVersion.Platform") {
		t.Errorf("expected uname then an encoded powershell probe, ran %v", h.ran)
	}
}

func TestDetectOSWindowsWithPOSIXToolsOnPath(t *testing.T) {
	h := &fakeHost{answers: map[string]fakeAnswer{"uname -s": {out: "MINGW64_NT-10.0\n"}}}
	if got, err := DetectOS(h); err != nil || got != Windows {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestDetectOSRefusesWhatItDoesNotRecognise(t *testing.T) {
	h := &fakeHost{answers: map[string]fakeAnswer{
		"uname -s":   {out: "Darwin\n"},
		"powershell": {err: errors.New("not found")},
	}}
	_, err := DetectOS(h)
	if err == nil || !strings.Contains(err.Error(), "--os") {
		t.Fatalf("expected an error pointing at --os, got %v", err)
	}
}
