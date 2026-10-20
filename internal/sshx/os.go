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

// Script returns the command line that runs a script in this OS's shell.
//
// On Linux the script is the command line already, as before. On Windows it is
// `powershell -NoProfile -NonInteractive` with the script passed as -EncodedCommand (base64 of
// UTF-16LE), which is -Command with the script sent as data. The reason is that the line is first
// parsed by the host's default SSH shell, which is cmd.exe or powershell depending on how OpenSSH
// was set up, and no quoting is correct for both. Base64 has nothing in it either of them reads.
func (o OS) Script(script string) string {
	if o.Resolved() != Windows {
		return script
	}
	// Silence the progress stream, which a non-interactive powershell otherwise writes to stderr
	// as CLIXML.
	u := utf16.Encode([]rune("$ProgressPreference='SilentlyContinue';" + script))
	b := make([]byte, 0, len(u)*2)
	for _, c := range u {
		b = append(b, byte(c), byte(c>>8))
	}
	return powerShell + " -EncodedCommand " + base64.StdEncoding.EncodeToString(b)
}

// Command builds a command line that runs argv as one program with its arguments, every word
// quoted for this OS's shell.
func (o OS) Command(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		parts[i] = o.Quote(a)
	}
	if o.Resolved() == Windows {
		return o.Script("& " + strings.Join(parts, " "))
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
	psOut, psErr := r.Run(Windows.Script("[System.Environment]::OSVersion.Platform"))
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
