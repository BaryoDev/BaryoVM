// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package site provisions a static site on a VM: webroot, nginx server block, certificate.
//
// Commands and the server block are built by pure functions (cmds.go, template.go); Add holds the
// order and the rollback and talks to an sshx.Runner, so tests drive it with a fake. Nothing here
// prints, shells out locally, or imports internal/cli. DNS is out of scope: the name has to
// resolve to the VM before certbot can issue.
package site

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	// ACMEDir is the shared webroot certbot writes challenge files into. It is the same directory
	// `stack tls` uses, so the two commands agree on where the challenge lives.
	ACMEDir      = "/var/www/letsencrypt"
	nginxConfDir = "/etc/nginx/conf.d"
	leDir        = "/etc/letsencrypt"
	sslOptions   = leDir + "/options-ssl-nginx.conf"
	sslDHParam   = leDir + "/ssl-dhparams.pem"
	prevSuffix   = ".baryovm-prev"

	// Marker sits on the first line of every file this package writes, so a file at the path
	// without it is known to be somebody else's.
	Marker = "Managed by BaryoVM"
)

var (
	dnsLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	emailRe  = regexp.MustCompile(`^[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)+$`)
	rootRe   = regexp.MustCompile(`^/[A-Za-z0-9._/-]*$`)
)

// ValidateDomain accepts a plain DNS name with at least two labels and nothing else. The name is
// pasted into an nginx config and into a certbot command, so a semicolon, brace, quote, space or
// newline would be config injection rather than a name.
func ValidateDomain(d string) error {
	if len(d) == 0 || len(d) > 253 {
		return fmt.Errorf("%q is not a valid domain: use a host name such as app.example.com", d)
	}
	labels := strings.Split(d, ".")
	if len(labels) < 2 {
		return fmt.Errorf("%q is not a valid domain: use a host name such as app.example.com", d)
	}
	for _, l := range labels {
		if !dnsLabel.MatchString(l) {
			return fmt.Errorf("%q is not a valid domain: lowercase letters, digits, hyphens and dots only", d)
		}
	}
	return nil
}

// ValidateRoot checks the webroot before it reaches a command. It must be absolute, made of plain
// path characters, free of .. segments, and not a system directory: a webroot of / or /etc would
// publish the host, and `site deploy` runs rsync --delete into it.
func ValidateRoot(r string) error {
	if !rootRe.MatchString(r) {
		return fmt.Errorf("--root %q must be an absolute path of letters, digits, dot, dash, underscore and slash only", r)
	}
	for _, seg := range strings.Split(r, "/") {
		if seg == ".." {
			return fmt.Errorf("--root %q must not contain a .. segment", r)
		}
	}
	switch strings.TrimRight(r, "/") {
	case "", "/etc", "/root", "/home", "/var", "/usr", "/opt", "/bin", "/sbin", "/lib", "/boot", "/dev", "/proc", "/sys", "/tmp", "/srv", "/var/www", ACMEDir:
		return fmt.Errorf("--root %q would publish a system directory or the shared challenge directory: give the site its own directory, such as /var/www/example.com", r)
	}
	return nil
}

// ValidateEmail checks the optional ACME contact address.
func ValidateEmail(e string) error {
	if e != "" && !emailRe.MatchString(e) {
		return fmt.Errorf("--email %q is not a valid address", e)
	}
	return nil
}

// ConfPath is the server block file for a domain.
func ConfPath(domain string) string { return nginxConfDir + "/" + domain + ".conf" }

// Options is what `site add` was asked to do.
type Options struct {
	Domain string
	Root   string
	Email  string // ACME contact; empty registers without one
	Force  bool   // replace a site that already exists
}

// Validate checks every value before any command is built.
func (o Options) Validate() error {
	if err := ValidateDomain(o.Domain); err != nil {
		return err
	}
	if err := ValidateRoot(o.Root); err != nil {
		return err
	}
	return ValidateEmail(o.Email)
}
