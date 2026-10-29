// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package site

import (
	"encoding/base64"

	"github.com/BaryoDev/BaryoVM/internal/sshx"
)

// Every remote command is built here, as a string, with every interpolated value through
// sshx.Quote and every root command through sshx.Sudo or sshx.SudoShell (`sudo -n`, never
// bare sudo). Add and Deploy only choose the order.

// State is what is at the server block path on the VM.
type State string

const (
	Absent  State = "absent"
	Managed State = "managed"
	Foreign State = "foreign"
)

// ProbeConfCmd says whether the server block file is absent, ours, or somebody else's.
func ProbeConfCmd(domain string) string {
	p, q := sshx.Quote(ConfPath(domain)), sshx.Quote
	return sshx.SudoShell(true, "if [ -e "+p+" ]; then if head -n 1 "+p+" | grep -qF "+q(Marker)+
		"; then echo managed; else echo foreign; fi; else echo absent; fi")
}

// ProbeCertCmd says whether certbot already holds a certificate for the domain.
func ProbeCertCmd(domain string) string {
	f := sshx.Quote(leDir + "/live/" + domain + "/fullchain.pem")
	return sshx.SudoShell(true, "if [ -f "+f+" ]; then echo present; else echo missing; fi")
}

// ProbeSSLCmd lists which of certbot's helper files exist.
func ProbeSSLCmd() string {
	return sshx.SudoShell(true, "[ -f "+sshx.Quote(sslOptions)+" ] && echo options; [ -f "+
		sshx.Quote(sslDHParam)+" ] && echo dhparam; true")
}

// ProbeSELinuxCmd prints selinux when SELinux is present and enforcing or permissive, none
// otherwise. Detected on the host, never assumed from the distribution.
func ProbeSELinuxCmd() string {
	return "if command -v selinuxenabled >/dev/null 2>&1 && selinuxenabled; then echo selinux; else echo none; fi"
}

// MkdirCmd creates the webroot and the challenge directory.
func MkdirCmd(root string) string {
	return sshx.Sudo(true, "mkdir -p "+sshx.Quote(root)+" "+sshx.Quote(ACMEDir))
}

// RestoreconCmd relabels the webroot so nginx may read it where SELinux is on.
func RestoreconCmd(root string) string {
	return sshx.Sudo(true, "restorecon -R "+sshx.Quote(root))
}

// BackupCmd copies the current server block aside, so a failed step can put it back.
func BackupCmd(domain string) string {
	return sshx.Sudo(true, "cp -p "+sshx.Quote(ConfPath(domain))+" "+sshx.Quote(ConfPath(domain)+prevSuffix))
}

// WriteCmd writes content to the server block path. The payload travels base64 encoded, so no byte
// of it is ever shell syntax.
func WriteCmd(domain, content string) string {
	return "printf '%s' " + sshx.Quote(base64.StdEncoding.EncodeToString([]byte(content))) +
		" | base64 -d | " + sshx.Sudo(true, "tee "+sshx.Quote(ConfPath(domain))) + " > /dev/null"
}

// TestCmd is nginx's own config test. Nothing reloads unless it passes.
func TestCmd() string { return sshx.Sudo(true, "nginx -t") }

// ReloadCmd applies a tested config.
func ReloadCmd() string { return sshx.Sudo(true, "systemctl reload nginx") }

// CertbotCmd issues the certificate in webroot mode, or keeps an unexpired one. --cert-name pins
// the live directory to the domain, which is where the server block points.
func CertbotCmd(domain, email string) string {
	q := sshx.Quote
	contact := "--register-unsafely-without-email"
	if email != "" {
		contact = "-m " + q(email)
	}
	return sshx.Sudo(true, "certbot certonly --webroot -w "+q(ACMEDir)+
		" --non-interactive --agree-tos "+contact+
		" --keep-until-expiring --cert-name "+q(domain)+" -d "+q(domain))
}

// RestoreCmd puts the previous server block back, or removes the new file when there was none.
func RestoreCmd(domain string, hadPrevious bool) string {
	p := sshx.Quote(ConfPath(domain))
	if hadPrevious {
		return sshx.Sudo(true, "mv -f "+sshx.Quote(ConfPath(domain)+prevSuffix)+" "+p)
	}
	return sshx.Sudo(true, "rm -f "+p)
}

// DropBackupCmd removes the copy BackupCmd made, once the new config is in for good.
func DropBackupCmd(domain string) string {
	return sshx.Sudo(true, "rm -f "+sshx.Quote(ConfPath(domain)+prevSuffix))
}
