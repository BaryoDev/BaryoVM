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
const psSuffix = "; exit $LASTEXITCODE"

// decodePayload reads the PowerShell wrapper back out of a powershell -EncodedCommand line.
func decodePayload(t *testing.T, line string) string {
	t.Helper()
	if !strings.HasPrefix(line, encodedPrefix) {
		t.Fatalf("not a powershell command line: %q", line)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSuffix(strings.TrimPrefix(line, encodedPrefix), psSuffix))
	if err != nil {
		t.Fatalf("payload is not base64: %v", err)
	}
	u := make([]uint16, len(raw)/2)
	for i := range u {
		u[i] = uint16(raw[2*i]) | uint16(raw[2*i+1])<<8
	}
	return string(utf16.Decode(u))
}

// decodeEncoded reads the user's script back out of the wrapper, without the epilogue that
// captures its result.
func decodeEncoded(t *testing.T, line string) string {
	t.Helper()
	payload := decodePayload(t, line)
	const open = "FromBase64String('"
	i := strings.Index(payload, open)
	if i < 0 {
		t.Fatalf("no script literal in payload: %q", payload)
	}
	rest := payload[i+len(open):]
	raw, err := base64.StdEncoding.DecodeString(rest[:strings.Index(rest, "'")])
	if err != nil {
		t.Fatalf("script literal is not base64: %v", err)
	}
	return strings.TrimSuffix(string(raw), "\n$global:bvmOk=$?;$global:bvmCode=$LASTEXITCODE")
}

func TestWindowsScriptTravelsWithoutAnOuterQuotingLayer(t *testing.T) {
	// The line has to mean the same thing whether the host's default shell is cmd.exe or
	// powershell, so nothing in it may be a character either of them interprets.
	script := `Write-Output 'it''s $x; "q" ` + "`t" + ` |&<>%^'`
	for _, sh := range []Shell{ShellCmd, ShellPowerShell} {
		line := Windows.Script(sh, script)
		if strings.ContainsAny(strings.TrimSuffix(strings.TrimPrefix(line, encodedPrefix), psSuffix), " \"'$;`|&<>%^") {
			t.Errorf("%s: payload carries shell syntax: %q", sh, line)
		}
		if got := decodeEncoded(t, line); got != script {
			t.Errorf("%s: script lost in transit: got %q", sh, got)
		}
	}
}

// Under powershell as the SSH shell, any failed child reports 1 unless the line says otherwise.
// Under cmd.exe the same text would be handed to powershell.exe as an argument and fail.
func TestWindowsScriptKeepsTheExitCodeOnlyUnderPowerShell(t *testing.T) {
	if got := Windows.Script(ShellPowerShell, "exit 3"); !strings.HasSuffix(got, psSuffix) {
		t.Errorf("powershell shell: missing the exit suffix: %q", got)
	}
	if got := Windows.Script(ShellCmd, "exit 3"); strings.Contains(got, "exit $") {
		t.Errorf("cmd shell: line must be one powershell invocation: %q", got)
	}
	if Windows.Script("", "exit 3") != Windows.Script(ShellCmd, "exit 3") {
		t.Error("an unset shell means cmd")
	}
}

func TestWindowsPayloadKeepsErrorsAsText(t *testing.T) {
	payload := decodePayload(t, Windows.Script(ShellCmd, "Write-Error boom"))
	// Without this a non-interactive powershell writes progress records to stderr as CLIXML.
	if !strings.HasPrefix(payload, "$ProgressPreference='SilentlyContinue'") {
		t.Errorf("progress is not silenced: %q", payload)
	}
	// Errors are written to stderr by us, as their message, instead of left to be serialised.
	for _, want := range []string{"[Console]::Error.WriteLine($_.ToString())", "2>&1", "NativeCommandError", "exit $global:bvmCode", "catch{[Console]::Error.WriteLine($_.ToString());exit 1}"} {
		if !strings.Contains(payload, want) {
			t.Errorf("payload does not contain %q:\n%s", want, payload)
		}
	}
	// The script is data, so it cannot end the wrapper's own quoting.
	if strings.Contains(payload, "Write-Error boom") {
		t.Errorf("the script must travel encoded, not spliced into the wrapper:\n%s", payload)
	}
}

func TestLinuxScriptIsTheCommandAsGiven(t *testing.T) {
	if got := Linux.Script(ShellCmd, "df -h"); got != "df -h" {
		t.Errorf("got %q", got)
	}
}

func TestWindowsCommandQuotesEachWord(t *testing.T) {
	got := decodeEncoded(t, Windows.Command(ShellCmd, []string{`C:\Program Files\app.exe`, "it's", "$x", "a`b;c"}))
	want := `& 'C:\Program Files\app.exe' 'it''s' '$x' 'a` + "`" + `b;c'`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestLinuxCommandQuotesEachWord(t *testing.T) {
	if got, want := Linux.Command(ShellCmd, []string{"echo", "a'b"}), `'echo' 'a'\''b'`; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestParseShell(t *testing.T) {
	cases := []struct {
		in      string
		want    Shell
		wantErr bool
	}{
		{"", ShellCmd, false},
		{"CMD", ShellCmd, false},
		{"powershell", ShellPowerShell, false},
		{"bash", "", true},
	}
	for _, c := range cases {
		got, err := ParseShell(c.in)
		if (err != nil) != c.wantErr || got != c.want {
			t.Errorf("ParseShell(%q) = %q, %v", c.in, got, err)
		}
	}
}

func TestDetectShell(t *testing.T) {
	cases := []struct {
		name string
		out  string
		err  error
		want Shell
		ok   bool
	}{
		{"cmd expands %OS%", "Windows_NT\r\n", nil, ShellCmd, true},
		{"powershell leaves it as text", "%OS%\r\n", nil, ShellPowerShell, true},
		{"anything else is not guessed", "hello", nil, "", false},
		{"a failed probe is an error", "", errors.New("exit 1"), "", false},
	}
	for _, c := range cases {
		h := &fakeHost{answers: map[string]fakeAnswer{"echo %OS%": {out: c.out, err: c.err}}}
		got, err := DetectShell(h)
		if (err == nil) != c.ok || got != c.want {
			t.Errorf("%s: got %q, %v", c.name, got, err)
		}
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
