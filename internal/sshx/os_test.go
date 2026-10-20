// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package sshx

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/BaryoDev/BaryoVM/internal/hostkeys"
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
		if got := quotePowerShell(c.in); got != c.want {
			t.Errorf("quotePowerShell(%q) = %q, want %q", c.in, got, c.want)
		}
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
	const open = "[ScriptBlock]::Create('"
	const closer = "')}catch{[Console]::Error.WriteLine($_.Exception.Message);exit 1}"
	i := strings.Index(payload, open)
	j := strings.LastIndex(payload, closer)
	if i < 0 || j < i {
		t.Fatalf("no script literal in payload: %q", payload)
	}
	lit := strings.NewReplacer("''", "'", "\u2018\u2018", "\u2018", "\u2019\u2019", "\u2019", "\u201a\u201a", "\u201a", "\u201b\u201b", "\u201b").Replace(payload[i+len(open) : j])
	return strings.TrimSuffix(lit, "\n$global:bvmOk=$?;$global:bvmCode=$LASTEXITCODE")
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
}

// The script sits in a single-quoted literal, so a quote in it must be doubled or it would end the
// literal and the rest of the script would run as part of the wrapper.
func TestWindowsPayloadKeepsAQuoteFromEndingTheScriptLiteral(t *testing.T) {
	scripts := []string{
		"x' ; Remove-Item C:\\ ; '",
		"it's a \u2019 and \u2018 and \u201a and \u201b",
		"line one\nline two\r\n# comment",
		"')}catch{exit 0}#",
	}
	for _, script := range scripts {
		line := Windows.Script(ShellCmd, script)
		if got := decodeEncoded(t, line); got != script {
			t.Errorf("script lost in transit: got %q, want %q", got, script)
		}
	}
	payload := decodePayload(t, Windows.Script(ShellCmd, "x' ; y"))
	if !strings.Contains(payload, "x'' ; y") {
		t.Errorf("the quote was not doubled:\n%s", payload)
	}
}

func TestLinuxScriptIsTheCommandAsGiven(t *testing.T) {
	if got := Linux.Script(ShellCmd, "df -h"); got != "df -h" {
		t.Errorf("got %q", got)
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

func TestResolvedNormalisesWhatAHandEditedFleetHolds(t *testing.T) {
	cases := map[OS]OS{" Windows ": Windows, "WINDOWS": Windows, "Linux": Linux, "  ": Linux, "freebsd": "freebsd"}
	for in, want := range cases {
		if got := in.Resolved(); got != want {
			t.Errorf("OS(%q).Resolved() = %q, want %q", in, got, want)
		}
	}
	// An unknown name stays unknown so a caller can refuse it, rather than being taken for Linux.
	if OS("freebsd").Known() || !OS("").Known() || !OS("Windows").Known() {
		t.Error("Known should accept linux and windows in any case, and empty, and nothing else")
	}
}

func TestShellResolvedNormalises(t *testing.T) {
	cases := map[Shell]Shell{"": ShellCmd, " PowerShell ": ShellPowerShell, "CMD": ShellCmd, "zsh": ShellCmd}
	for in, want := range cases {
		if got := in.Resolved(); got != want {
			t.Errorf("Shell(%q).Resolved() = %q, want %q", in, got, want)
		}
	}
	if Windows.Script(" PowerShell ", "exit 3") != Windows.Script(ShellPowerShell, "exit 3") {
		t.Error("Script should read the shell the same way Resolved does")
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
		// Everything that is not Windows is reached the same way, over a POSIX shell.
		{"Darwin\n", Linux, true},
		{"FreeBSD\n", Linux, true},
		{"", "", false},
		{"   \n", "", false},
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
		"uname -s":   {out: ""},
		"powershell": {err: errors.New("not found")},
	}}
	_, err := DetectOS(h)
	if err == nil || !strings.Contains(err.Error(), "--os") {
		t.Fatalf("expected an error pointing at --os, got %v", err)
	}
}

func TestDetectOSOtherUnixIsRecordedAsLinux(t *testing.T) {
	h := &fakeHost{answers: map[string]fakeAnswer{"uname -s": {out: "Darwin\n"}}}
	if got, err := DetectOS(h); err != nil || got != Linux {
		t.Fatalf("got %q, %v", got, err)
	}
	if len(h.ran) != 1 {
		t.Errorf("a host that answered uname needs no second probe, ran %v", h.ran)
	}
}

func TestDetectOSUnreachableHostPointsAtOSFlag(t *testing.T) {
	h := &fakeHost{answers: map[string]fakeAnswer{
		"uname -s":   {err: errors.New("connection refused")},
		"powershell": {err: errors.New("connection refused")},
	}}
	if _, err := DetectOS(h); err == nil || !strings.Contains(err.Error(), "pass --os") {
		t.Fatalf("expected the --os hint, got %v", err)
	}
}

func TestScriptLineRefusesWhatTheHostCannotTake(t *testing.T) {
	// Largest script that still fits, found by search so the test follows the limits and not a guess.
	fits := func(sh Shell, limit int) int {
		n := 0
		for step := 1 << 16; step > 0; step >>= 1 {
			if n+step > limit {
				continue
			}
			line, err := Windows.ScriptLine(sh, strings.Repeat("a", n+step))
			if err == nil && len(line) <= limit {
				n += step
			}
		}
		return n
	}
	for _, c := range []struct {
		sh    Shell
		limit int
	}{{ShellCmd, maxLineCmd - cmdPrefix}, {ShellPowerShell, maxLinePowerShell}} {
		n := fits(c.sh, c.limit)
		if n < 1000 {
			t.Fatalf("%s: even a short script is refused (largest fit %d)", c.sh, n)
		}
		line, err := Windows.ScriptLine(c.sh, strings.Repeat("a", n))
		if err != nil || len(line) > c.limit {
			t.Fatalf("%s: a script of %d should fit: %v", c.sh, n, err)
		}
		_, err = Windows.ScriptLine(c.sh, strings.Repeat("a", n+1))
		if err == nil {
			t.Fatalf("%s: a script of %d is over the limit and must be refused", c.sh, n+1)
		}
		for _, want := range []string{fmt.Sprint(n + 1), fmt.Sprint(c.limit)} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: message %q does not name %s", c.sh, err, want)
			}
		}
	}
}

func TestScriptLineLinuxIsNeverLimited(t *testing.T) {
	long := strings.Repeat("a", 100000)
	if line, err := Linux.ScriptLine(ShellCmd, long); err != nil || line != long {
		t.Errorf("linux: err %v, unchanged %v", err, line == long)
	}
}

func TestIsUnreachable(t *testing.T) {
	timeout := &net.OpError{Op: "dial", Err: errors.New("i/o timeout")}
	refused := fmt.Errorf("ssh dial 10.0.0.1:22: %w", &net.OpError{Op: "dial", Err: errors.New("connection refused")})
	changed := fmt.Errorf("ssh: handshake failed: %w", &hostkeys.ErrChanged{Host: "h"})
	unknown := fmt.Errorf("ssh dial: %w", &hostkeys.ErrUnknownHost{Host: "h"})
	for name, c := range map[string]struct {
		err  error
		want bool
	}{
		"timeout": {timeout, true}, "refused": {refused, true},
		"changed key": {changed, false}, "unknown host in strict mode": {unknown, false},
		"bad key file": {errors.New("read key /k: no such file"), false}, "nil": {nil, false},
	} {
		if got := IsUnreachable(c.err); got != c.want {
			t.Errorf("%s: got %v, want %v", name, got, c.want)
		}
	}
}
