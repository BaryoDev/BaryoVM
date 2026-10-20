// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package sshx

import (
	"encoding/base64"
	"fmt"
	"strings"
	"unicode/utf16"
)

// OS is the operating system of a host, which decides the shell commands are written for.
type OS string

const (
	Linux   OS = "linux"
	Windows OS = "windows"
)

// Resolved treats an empty OS as Linux. Every fleet entry written before the OS was recorded is a
// Linux host, so absence has to mean that and not "unknown".
func (o OS) Resolved() OS {
	if o == "" {
		return Linux
	}
	return o
}

// ParseOS reads a user-supplied OS name. Empty is Linux, the same default as Resolved.
func ParseOS(s string) (OS, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "linux":
		return Linux, nil
	case "windows":
		return Windows, nil
	}
	return "", fmt.Errorf("unknown OS %q: use linux or windows", s)
}

// Quote quotes one value for the shell of this OS: POSIX sh on Linux, PowerShell on Windows.
func (o OS) Quote(s string) string {
	if o.Resolved() == Windows {
		return quotePowerShell(s)
	}
	return Quote(s)
}

// quotePowerShell single-quotes a value. Nothing inside single quotes is expanded, so $, a backtick
// and ; stay literal. The quote itself is doubled, and so are the typographic single quotes, which
// PowerShell treats as quote characters too and which would otherwise end the string early.
func quotePowerShell(s string) string {
	r := strings.NewReplacer("'", "''", "‘", "‘‘", "’", "’’", "‚", "‚‚", "‛", "‛‛")
	return "'" + r.Replace(s) + "'"
}

const powerShell = "powershell -NoProfile -NonInteractive"

// Shell is the program OpenSSH on the host starts to run a command line. It matters on Windows,
// where the default is cmd.exe unless the administrator set powershell.exe, and the two read the
// same line differently. Empty means cmd.exe, the OpenSSH default.
type Shell string

const (
	ShellCmd        Shell = "cmd"
	ShellPowerShell Shell = "powershell"
)

// ParseShell reads a shell name, with empty meaning cmd.
func ParseShell(s string) (Shell, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "cmd":
		return ShellCmd, nil
	case "powershell":
		return ShellPowerShell, nil
	}
	return "", fmt.Errorf("unknown shell %q: use cmd or powershell", s)
}

// DetectShell asks a Windows host which shell runs its SSH commands. %OS% is expanded by cmd.exe
// (to Windows_NT) and left as the literal text by PowerShell, which is the whole probe.
func DetectShell(r Runner) (Shell, error) {
	out, err := r.Run("echo %OS%")
	if err != nil {
		return "", fmt.Errorf("could not tell which shell the host runs SSH commands in: %w", err)
	}
	switch strings.TrimSpace(out) {
	case "Windows_NT":
		return ShellCmd, nil
	case "%OS%":
		return ShellPowerShell, nil
	}
	return "", fmt.Errorf("could not tell which shell the host runs SSH commands in: unexpected output %q", strings.TrimSpace(out))
}

// Script returns the command line that runs a script on a host of this OS whose SSH shell is sh.
//
// On Linux the script is the command line already, as before. On Windows it is
// `powershell -NoProfile -NonInteractive -EncodedCommand <base64>`. The base64 is there because the
// line is first parsed by the host's SSH shell, and no quoting is correct for both cmd.exe and
// powershell; base64 has nothing in it either of them reads. What it carries is windowsPayload.
//
// When the SSH shell is powershell, the line ends with `; exit $LASTEXITCODE`. Without it that
// shell reports 1 for any failed child, whatever the child's own exit code was. cmd.exe would pass
// the extra text to powershell.exe as an argument and fail, so it is not added there.
func (o OS) Script(sh Shell, script string) string {
	if o.Resolved() != Windows {
		return script
	}
	u := utf16.Encode([]rune(windowsPayload(script)))
	b := make([]byte, 0, len(u)*2)
	for _, c := range u {
		b = append(b, byte(c), byte(c>>8))
	}
	line := powerShell + " -EncodedCommand " + base64.StdEncoding.EncodeToString(b)
	if sh == ShellPowerShell {
		line += "; exit $LASTEXITCODE"
	}
	return line
}

// windowsPayload is the PowerShell that runs a user's script and keeps the result plain.
//
// powershell -EncodedCommand writes its error stream to stderr as CLIXML when stderr is not a
// terminal, which is always the case over SSH. So the script's own errors are caught here and
// written as text instead: each error record's message goes to stderr, and the native-command
// kind (a program writing to its stderr) is not treated as a failure, only its exit code is.
// Progress output is silenced for the same CLIXML reason.
//
// The script travels as base64 inside this text, so nothing in it can break the wrapper, and a
// script that does not parse reports its error as text too. The exit code is the script's own
// `exit`, else the code of a native command that was the last thing to fail, else 1 if a PowerShell
// error was written. A native command that only wrote to its stderr and exited 0 succeeded, though
// PowerShell 5.1 sets $? false for it.
func windowsPayload(script string) string {
	body := script + "\n$global:bvmOk=$?;$global:bvmCode=$LASTEXITCODE"
	b64 := base64.StdEncoding.EncodeToString([]byte(body))
	return strings.Join([]string{
		"$ProgressPreference='SilentlyContinue'",
		"try{$s=[ScriptBlock]::Create([Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('" + b64 + "')))}catch{[Console]::Error.WriteLine($_.Exception.Message);exit 1}",
		"$global:bvmFail=$false",
		"try{& $s 2>&1|ForEach-Object{if($_ -is [Management.Automation.ErrorRecord]){[Console]::Error.WriteLine($_.ToString());if($_.FullyQualifiedErrorId -ne 'NativeCommandError'){$global:bvmFail=$true}}else{$_}}}catch{[Console]::Error.WriteLine($_.ToString());exit 1}",
		"if($global:bvmOk -eq $false -and $global:bvmCode){exit $global:bvmCode}",
		"if($global:bvmFail){exit 1}",
	}, "\n")
}

// Command builds a command line that runs argv as one program with its arguments, every word
// quoted for this OS's shell.
func (o OS) Command(sh Shell, argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		parts[i] = o.Quote(a)
	}
	if o.Resolved() == Windows {
		return o.Script(sh, "& "+strings.Join(parts, " "))
	}
	return strings.Join(parts, " ")
}

// AsAdmin is Sudo for either OS: sudo -n on Linux, and the command untouched on Windows, where
// OpenSSH gives an administrator an elevated session and there is no sudo to prefix.
func (o OS) AsAdmin(admin bool, cmd string) string {
	if o.Resolved() == Windows {
		return cmd
	}
	return Sudo(admin, cmd)
}

// AsAdminShell is SudoShell for either OS, for a command that came from a manifest.
func (o OS) AsAdminShell(admin bool, cmd string) string {
	if o.Resolved() == Windows {
		return cmd
	}
	return SudoShell(admin, cmd)
}

// DetectOS asks a host what it is. A POSIX probe goes first, because it is one cheap command on
// Linux; a host that cannot answer it (cmd.exe has no uname) gets a PowerShell probe, which works
// whether the default shell is cmd.exe or powershell.
func DetectOS(r Runner) (OS, error) {
	unameOut, unameErr := r.Run("uname -s")
	if unameErr == nil {
		if o, ok := osFromUname(unameOut); ok {
			return o, nil
		}
	}
	psOut, psErr := r.Run(Windows.Script(ShellCmd, "[System.Environment]::OSVersion.Platform"))
	if psErr == nil {
		if o, ok := osFromPlatform(psOut); ok {
			return o, nil
		}
	}
	return "", fmt.Errorf("could not tell whether the host is Linux or Windows (pass --os to say): uname: %s; powershell: %s",
		probeResult(unameOut, unameErr), probeResult(psOut, psErr))
}

func probeResult(out string, err error) string {
	if err != nil {
		return err.Error()
	}
	return fmt.Sprintf("unrecognised output %q", strings.TrimSpace(out))
}

// osFromUname reads `uname -s`. Linux is Linux. MINGW, MSYS and CYGWIN are what a Windows host
// with Git for Windows or Cygwin on its path reports; they are still Windows.
func osFromUname(out string) (OS, bool) {
	s := strings.TrimSpace(out)
	switch {
	case s == "Linux":
		return Linux, true
	case strings.HasPrefix(s, "MINGW"), strings.HasPrefix(s, "MSYS"), strings.HasPrefix(s, "CYGWIN"):
		return Windows, true
	}
	return "", false
}

// osFromPlatform reads [System.Environment]::OSVersion.Platform, which is Win32NT on Windows.
func osFromPlatform(out string) (OS, bool) {
	if strings.TrimSpace(out) == "Win32NT" {
		return Windows, true
	}
	return "", false
}
