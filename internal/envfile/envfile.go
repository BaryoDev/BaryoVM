// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package envfile sets, lists and removes keys in a stack's .env on the VM.
//
// A value is a secret, so it never becomes part of a command line, local or remote, where `ps` on
// the VM would show it (#93). It travels on the ssh session's stdin and a small remote script
// writes it. The command strings built here are pure functions of the path and the key, which is
// what lets a test assert that no value can be in them.
package envfile

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/sshx"
)

// DefaultFile is the env file of a stack registered without --env-file, relative to its directory.
const DefaultFile = ".env"

// Runner is a remote that can also take a secret on stdin. *sshx.Client is one.
type Runner interface {
	sshx.Runner
	RunInput(cmd string, stdin string) (string, error)
}

var keyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidKey refuses a key that is not a plain shell-style name. It runs before any remote call.
func ValidKey(key string) error {
	if !keyRe.MatchString(key) {
		return fmt.Errorf("invalid key %q: use letters, digits and underscores, not starting with a digit", key)
	}
	return nil
}

// ValidValue refuses a value that cannot be one line of an env file.
func ValidValue(v string) error {
	if strings.ContainsAny(v, "\n\r\x00") {
		return fmt.Errorf("the value has a line break or NUL: an env file holds one line per key")
	}
	return nil
}

// Path is the env file on the VM: the stack's registered env file (default .env) under its
// directory, or the file itself when the registration holds an absolute path.
func Path(dir, file string) string {
	if file == "" {
		file = DefaultFile
	}
	if path.IsAbs(file) {
		return file
	}
	return path.Join(dir, file)
}

// SetCmd is the remote script that replaces or appends one KEY=value line, with the value read from
// stdin. Creates the file and its directory when missing and leaves the file at mode 600.
//
// The value is copied to a temp file and read by awk with getline, not passed with -v: -v would
// both put it on awk's command line and process backslash escapes in it. The rewrite goes back
// with cat into the existing file, so its owner survives, and the temp files are mode 600 from
// umask and removed on exit.
func SetCmd(file, key string, sudo bool) string {
	script := strings.Join([]string{
		"set -e",
		"umask 077",
		"f=" + sshx.Quote(file),
		`(umask 022; mkdir -p "$(dirname "$f")")`,
		`vf=$(mktemp); nf=$(mktemp)`,
		`trap 'rm -f "$vf" "$nf"' EXIT`,
		`cat > "$vf"`,
		`[ -e "$f" ] || : > "$f"`,
		`awk -v k=` + sshx.Quote(key) + ` -v vf="$vf" 'BEGIN{getline v < vf; close(vf)} {p="";r=$0; if(match($0,/^export[ \t]+/)){p=substr($0,1,RLENGTH);r=substr($0,RLENGTH+1)}} index(r,k"=")==1{if(!d){print p k"="v;d=1};next} {print} END{if(!d)print k"="v}' "$f" > "$nf"`,
		`cat "$nf" > "$f"`,
		`chmod 600 "$f"`,
	}, "\n")
	return sshx.SudoShell(sudo, script)
}

// UnsetCmd is the remote script that removes the KEY line. A missing file has nothing to remove.
func UnsetCmd(file, key string, sudo bool) string {
	script := strings.Join([]string{
		"set -e",
		"umask 077",
		"f=" + sshx.Quote(file),
		`[ -f "$f" ] || exit 0`,
		`nf=$(mktemp)`,
		`trap 'rm -f "$nf"' EXIT`,
		`awk -v k=` + sshx.Quote(key) + ` '{r=$0; if(match($0,/^export[ \t]+/))r=substr($0,RLENGTH+1)} index(r,k"=")!=1' "$f" > "$nf"`,
		`cat "$nf" > "$f"`,
		`chmod 600 "$f"`,
	}, "\n")
	return sshx.SudoShell(sudo, script)
}

// ListCmd prints the keys of the file, one per line. The cut happens on the VM, so a value never
// travels back over the connection.
func ListCmd(file string, sudo bool) string {
	script := "f=" + sshx.Quote(file) + "\n" +
		`if [ -f "$f" ]; then awk '{r=$0; if(match($0,/^export[ \t]+/))r=substr($0,RLENGTH+1)} r ~ /^[A-Za-z_][A-Za-z0-9_]*=/{print substr(r,1,index(r,"=")-1)}' "$f"; fi`
	return sshx.SudoShell(sudo, script)
}

// Set writes KEY=value. Nothing is sent if the key or value is refused.
func Set(r Runner, file, key, value string, sudo bool) error {
	if err := ValidKey(key); err != nil {
		return err
	}
	if err := ValidValue(value); err != nil {
		return err
	}
	_, err := r.RunInput(SetCmd(file, key, sudo), value+"\n")
	return err
}

// Unset removes KEY.
func Unset(r sshx.Runner, file, key string, sudo bool) error {
	if err := ValidKey(key); err != nil {
		return err
	}
	_, err := r.Run(UnsetCmd(file, key, sudo))
	return err
}

// List returns the keys in the file, in order, without duplicates.
func List(r sshx.Runner, file string, sudo bool) ([]string, error) {
	out, err := r.Run(ListCmd(file, sudo))
	if err != nil {
		return nil, err
	}
	return parseKeys(out), nil
}

func parseKeys(out string) []string {
	var keys []string
	seen := map[string]bool{}
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || seen[l] || !keyRe.MatchString(l) {
			continue
		}
		seen[l] = true
		keys = append(keys, l)
	}
	return keys
}
