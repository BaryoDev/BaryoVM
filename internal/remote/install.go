// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package remote

import (
	"encoding/json"
	"fmt"
	"path"

	"github.com/BaryoDev/BaryoVM/internal/sshx"
)

// InstallBinary is where the wrapper binary goes on the VM. root-owned, so the SSH user that the
// forced command runs as cannot replace the program it is forced to run.
const InstallBinary = "/usr/local/bin/baryovm-remote"

// KeyOptions are the authorized_keys options added beside command=. A CI key that only runs the
// wrapper has no use for a terminal or any forwarding.
const KeyOptions = "no-port-forwarding,no-X11-forwarding,no-agent-forwarding,no-pty"

// AuthorizedKeysLine is the line the VM owner adds to authorized_keys. publicKey is the CI key's
// public half; it is never read or edited by baryovm.
func AuthorizedKeysLine(publicKey string) string {
	return fmt.Sprintf(`command="%s remote serve",%s %s`, InstallBinary, KeyOptions, publicKey)
}

// TemplateConfig is the allowlist written on first install. Its one stack allows nothing, so
// installing grants no access until the owner edits the file.
func TemplateConfig() []byte {
	c := Config{Stacks: map[string]StackConfig{
		"example": {
			Dir:       "/opt/example",
			HealthURL: "http://127.0.0.1:8080/health",
			Ops:       []string{},
		},
	}}
	b, _ := json.MarshalIndent(c, "", "  ")
	return append(b, '\n')
}

// UnameArch maps `uname -m` to a Go GOARCH, or "" for an architecture without a release binary.
func UnameArch(uname string) string {
	switch uname {
	case "x86_64", "amd64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	}
	return ""
}

// The install commands below run as the SSH user with sudo -n, and write root-owned files.

// UploadCmd streams stdin into a temp file in the user's home.
func UploadCmd(tmp string) string { return "cat > " + sshx.Quote(tmp) }

// InstallBinaryCmd moves the uploaded binary into place as root:root 0755.
func InstallBinaryCmd(tmp string) string {
	return sshx.Sudo(true, "install -m 0755 -o root -g root "+sshx.Quote(tmp)+" "+sshx.Quote(InstallBinary)) + " && rm -f " + sshx.Quote(tmp)
}

// InstallConfigCmd creates /etc/baryovm and the template, root:root, and leaves an existing file
// alone: re-running install must never reset an allowlist the owner edited.
func InstallConfigCmd(tmp string) string {
	dir := path.Dir(DefaultConfigPath)
	return sshx.Sudo(true, "install -d -m 0755 -o root -g root "+sshx.Quote(dir)) +
		" && if " + sshx.Sudo(true, "test -e "+sshx.Quote(DefaultConfigPath)) + "; then echo kept; else " +
		sshx.Sudo(true, "install -m 0644 -o root -g root "+sshx.Quote(tmp)+" "+sshx.Quote(DefaultConfigPath)) + " && echo created; fi" +
		" && rm -f " + sshx.Quote(tmp)
}
