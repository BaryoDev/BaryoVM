// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package release

import (
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/sshx"
)

// VhostMarker is how BaryoVM recognises a file it wrote. It sits on the first line of every
// generated config, and a file at the vhost path without it is somebody else's: it is not
// overwritten unless the operator says --adopt. Without this a release would replace a hand-tuned
// vhost on the strength of a hostname match alone.
const VhostMarker = "Managed by BaryoVM"

const (
	acmeWebroot   = "/var/www/letsencrypt"
	nginxConfDir  = "/etc/nginx/conf.d"
	leDir         = "/etc/letsencrypt"
	sslOptionsRel = "/etc/letsencrypt/options-ssl-nginx.conf"
	sslDHParam    = "/etc/letsencrypt/ssl-dhparams.pem"
	prevSuffix    = ".baryovm-prev"
)

// TLS describes the public face of a site: which names it answers to, the certificate for them and
// where requests go. One block produces one nginx vhost file and one certificate.
type TLS struct {
	// Hosts are the names served. They all go on the certificate.
	Hosts []string `json:"hosts"`
	// Canonical is the one name that serves content; every other name redirects to it. Defaults to
	// the first host.
	Canonical string `json:"canonical,omitempty"`
	// Redirects are extra names that only redirect to Canonical. They are added to the certificate
	// too, because a redirect from a name the certificate does not cover is a browser warning.
	Redirects []string `json:"redirects,omitempty"`
	// Email is the ACME account contact.
	Email string `json:"email"`
	// Root is the static document root. Defaults to the manifest's remoteRoot. Unused with Proxy.
	Root string `json:"root,omitempty"`
	// Proxy sends requests to a local app instead of serving Root.
	Proxy *Proxy `json:"proxy,omitempty"`
}

// Proxy is an upstream on the VM's loopback.
type Proxy struct {
	Port int `json:"port"`
	// MaxBodySize is nginx's client_max_body_size, e.g. "32m". Omitted means nginx's default.
	MaxBodySize string `json:"maxBodySize,omitempty"`
	// LargeHeaders raises the proxy buffers. A CMS backoffice login sets auth cookies larger than
	// the default buffer and gets a 502 without it.
	LargeHeaders bool `json:"largeHeaders,omitempty"`
}

// SSLFiles says which of certbot's helper files exist on the VM. `certbot certonly` does not
// create them (only the nginx plugin does), so including them blindly makes nginx -t fail on a
// host that never ran the plugin.
type SSLFiles struct {
	Options bool
	DHParam bool
}

var (
	dnsLabel   = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	emailRe    = regexp.MustCompile(`^[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)+$`)
	sizeRe     = regexp.MustCompile(`^[0-9]+[kKmMgG]?$`)
	safePathRe = regexp.MustCompile(`^/[A-Za-z0-9._/-]*$`)
	slugRe     = regexp.MustCompile(`[^a-z0-9]`)
)

// validHost accepts a plain DNS name and nothing else. Host names are pasted straight into an
// nginx config and into a certbot command, so anything that is not letters, digits, hyphens and
// dots (a semicolon, a brace, a newline, a quote) would be config injection rather than a name.
func validHost(h string) error {
	if len(h) == 0 || len(h) > 253 {
		return fmt.Errorf("tls: %q is not a valid host name", h)
	}
	for _, l := range strings.Split(h, ".") {
		if !dnsLabel.MatchString(l) {
			return fmt.Errorf("tls: %q is not a plain DNS name (lowercase letters, digits, hyphens and dots only)", h)
		}
	}
	return nil
}

// Validate checks the block and fills the canonical default. remoteRoot is the fallback static
// root.
func (t *TLS) Validate(remoteRoot string) error {
	if len(t.Hosts) == 0 {
		return fmt.Errorf("tls: hosts is required")
	}
	for _, h := range t.Hosts {
		if err := validHost(h); err != nil {
			return err
		}
	}
	for _, h := range t.Redirects {
		if err := validHost(h); err != nil {
			return err
		}
	}
	if t.Canonical == "" {
		t.Canonical = t.Hosts[0]
	}
	if err := validHost(t.Canonical); err != nil {
		return err
	}
	found := false
	for _, h := range t.Hosts {
		if h == t.Canonical {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("tls: canonical %q must be one of hosts", t.Canonical)
	}
	if !emailRe.MatchString(t.Email) {
		return fmt.Errorf("tls: email %q is not a valid address", t.Email)
	}
	if t.Proxy != nil {
		if t.Proxy.Port < 1 || t.Proxy.Port > 65535 {
			return fmt.Errorf("tls: proxy.port must be 1 to 65535")
		}
		if t.Proxy.MaxBodySize != "" && !sizeRe.MatchString(t.Proxy.MaxBodySize) {
			return fmt.Errorf("tls: proxy.maxBodySize %q must look like 32m", t.Proxy.MaxBodySize)
		}
		return nil
	}
	root := t.staticRoot(remoteRoot)
	if root == "" {
		return fmt.Errorf("tls: set root, set proxy, or give the manifest a remoteRoot")
	}
	if !safePathRe.MatchString(root) {
		return fmt.Errorf("tls: root %q must be an absolute path of plain characters", root)
	}
	for _, seg := range strings.Split(root, "/") {
		if seg == ".." {
			return fmt.Errorf("tls: root %q must not contain a .. segment", root)
		}
	}
	// A system directory as a web root publishes the host. The fallback to remoteRoot makes this
	// easy to hit by accident, so name the value and the way out.
	switch strings.TrimRight(root, "/") {
	case "", "/etc", "/root", "/home", "/var", "/usr", "/opt":
		return fmt.Errorf("tls: root %q would publish a system directory: set tls.root to the site's own directory", root)
	}
	return nil
}

func (t *TLS) staticRoot(remoteRoot string) string {
	if t.Root != "" {
		return t.Root
	}
	return strings.TrimSuffix(remoteRoot, "/")
}

// Names is every name on the certificate: canonical first, then the other hosts, then redirects.
func (t *TLS) Names() []string {
	seen := map[string]bool{}
	var out []string
	add := func(h string) {
		if !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	add(t.canon())
	for _, h := range t.Hosts {
		add(h)
	}
	for _, h := range t.Redirects {
		add(h)
	}
	return out
}

func (t *TLS) canon() string {
	if t.Canonical != "" {
		return t.Canonical
	}
	return t.Hosts[0]
}

// VhostPath is the file the generated config lives in.
func (t *TLS) VhostPath() string { return nginxConfDir + "/" + t.canon() + ".conf" }

func header() string {
	return "# " + VhostMarker + " (baryovm stack tls). Hand edits are overwritten on the next run.\n"
}

// port80 is the plain-HTTP server: it answers ACME challenges from the shared webroot and sends
// everything else to https. Host-specific on purpose, so it does not depend on an include that may
// not exist on this box.
func (t *TLS) port80() string {
	return "server {\n" +
		"    listen 80;\n" +
		"    listen [::]:80;\n" +
		"    server_name " + strings.Join(t.Names(), " ") + ";\n\n" +
		"    location /.well-known/acme-challenge/ {\n" +
		"        root " + acmeWebroot + ";\n" +
		"    }\n\n" +
		"    location / {\n" +
		"        return 301 https://" + t.canon() + "$request_uri;\n" +
		"    }\n" +
		"}\n"
}

// ACMEVhost is the stage-one config: port 80 only, so nginx can start and serve the challenge
// before any certificate exists. A 443 block pointing at a missing certificate fails nginx -t.
func (t *TLS) ACMEVhost() string {
	return header() + "# Stage 1: port 80 only, until the certificate for " + t.canon() + " exists.\n\n" + t.port80()
}

func (t *TLS) sslBlock(f SSLFiles) string {
	live := leDir + "/live/" + t.canon()
	s := "    ssl_certificate " + live + "/fullchain.pem;\n" +
		"    ssl_certificate_key " + live + "/privkey.pem;\n"
	if f.Options {
		s += "    include " + sslOptionsRel + ";\n"
	}
	if f.DHParam {
		s += "    ssl_dhparam " + sslDHParam + ";\n"
	}
	return s + "\n    add_header Strict-Transport-Security \"max-age=31536000\" always;\n\n"
}

// Vhost is the full config: port 80, a redirect server for every non-canonical name, and the
// canonical 443 server. Pure, so it is covered by golden files.
func (t *TLS) Vhost(remoteRoot string, f SSLFiles) string {
	var b strings.Builder
	b.WriteString(header())
	b.WriteString("\n")

	upgrade := ""
	if t.Proxy != nil {
		// A map per site, named from the canonical host: map names are global across every file in
		// conf.d, so a shared name would collide with a second site's file.
		upgrade = "$baryovm_connection_upgrade_" + slugRe.ReplaceAllString(t.canon(), "_")
		b.WriteString("map $http_upgrade " + upgrade + " {\n    default upgrade;\n    ''      close;\n}\n\n")
	}

	b.WriteString(t.port80())

	var others []string
	for _, n := range t.Names() {
		if n != t.canon() {
			others = append(others, n)
		}
	}
	if len(others) > 0 {
		b.WriteString("\nserver {\n    listen 443 ssl;\n    listen [::]:443 ssl;\n")
		b.WriteString("    server_name " + strings.Join(others, " ") + ";\n\n")
		b.WriteString(t.sslBlock(f))
		b.WriteString("    return 301 https://" + t.canon() + "$request_uri;\n}\n")
	}

	b.WriteString("\nserver {\n    listen 443 ssl;\n    listen [::]:443 ssl;\n")
	b.WriteString("    server_name " + t.canon() + ";\n\n")
	b.WriteString(t.sslBlock(f))

	if t.Proxy == nil {
		b.WriteString("    root " + t.staticRoot(remoteRoot) + ";\n    index index.html;\n\n")
		// The root defaults to remoteRoot, which for a compose stack is the compose directory and
		// holds its .env. Dot files are never served; /.well-known/ stays reachable (ACME,
		// security.txt) and is matched first with ^~ so the regex rule below cannot catch it.
		b.WriteString("    location ^~ /.well-known/ {\n        try_files $uri =404;\n    }\n\n")
		b.WriteString("    location ~ /\\. {\n        deny all;\n        return 404;\n    }\n\n")
		b.WriteString("    location / {\n        try_files $uri $uri/ =404;\n    }\n}\n")
		return b.String()
	}

	if t.Proxy.MaxBodySize != "" {
		b.WriteString("    client_max_body_size " + t.Proxy.MaxBodySize + ";\n\n")
	}
	// Set by nginx, never passed through. A client-supplied X-Forwarded-For or -Proto would let a
	// caller impersonate another address or claim https. Upgrade is the one value that has to come
	// from the client, since it is what asks for a websocket.
	b.WriteString("    location / {\n")
	b.WriteString(fmt.Sprintf("        proxy_pass http://127.0.0.1:%d;\n", t.Proxy.Port))
	b.WriteString("        proxy_http_version 1.1;\n")
	b.WriteString("        proxy_set_header Host $host;\n")
	b.WriteString("        proxy_set_header X-Forwarded-For $remote_addr;\n")
	b.WriteString("        proxy_set_header X-Forwarded-Proto $scheme;\n")
	b.WriteString("        proxy_set_header Upgrade $http_upgrade;\n")
	b.WriteString("        proxy_set_header Connection " + upgrade + ";\n")
	if t.Proxy.LargeHeaders {
		b.WriteString("        proxy_buffer_size 16k;\n        proxy_buffers 8 16k;\n        proxy_busy_buffers_size 32k;\n")
	}
	b.WriteString("    }\n}\n")
	return b.String()
}

// CertbotCmd issues or keeps the certificate. --cert-name pins the live directory to the canonical
// host, which is where the vhost points; without it certbot names the directory after the first -d
// and a later reorder would orphan the path.
func (t *TLS) CertbotCmd() string {
	q := sshx.Quote
	cmd := "sudo -n certbot certonly --webroot -w " + q(acmeWebroot) +
		" --non-interactive --agree-tos -m " + q(t.Email) +
		" --keep-until-expiring --cert-name " + q(t.canon())
	for _, n := range t.Names() {
		cmd += " -d " + q(n)
	}
	return cmd
}

// VhostState is what is at the vhost path on the VM.
type VhostState string

const (
	VhostAbsent  VhostState = "absent"
	VhostManaged VhostState = "managed"
	VhostForeign VhostState = "foreign"
)

func (t *TLS) probeVhostCmd() string {
	p, q := sshx.Quote(t.VhostPath()), sshx.Quote
	return sshx.SudoShell(true, "if [ -e "+p+" ]; then if head -n 1 "+p+" | grep -qF "+q(VhostMarker)+
		"; then echo managed; else echo foreign; fi; else echo absent; fi")
}

func (t *TLS) probeCertCmd() string {
	f := sshx.Quote(leDir + "/live/" + t.canon() + "/fullchain.pem")
	return sshx.SudoShell(true, "if [ -f "+f+" ]; then echo present; else echo missing; fi")
}

func probeSSLCmd() string {
	return sshx.SudoShell(true, "[ -f "+sshx.Quote(sslOptionsRel)+" ] && echo options; [ -f "+
		sshx.Quote(sslDHParam)+" ] && echo dhparam; true")
}

func writeCmd(path, content string) string {
	return "printf '%s' " + sshx.Quote(base64.StdEncoding.EncodeToString([]byte(content))) +
		" | base64 -d | sudo -n tee " + sshx.Quote(path) + " > /dev/null"
}

func cpCmd(from, to string) string {
	return "sudo -n cp -p " + sshx.Quote(from) + " " + sshx.Quote(to)
}

// TLSOptions are the run-time switches of the TLS step.
type TLSOptions struct {
	// Adopt takes over a vhost file that BaryoVM did not write, after copying it aside.
	Adopt bool
	// Stamp names the backup made when adopting. The caller supplies it so this stays pure.
	Stamp string
}

func (o TLSOptions) stamp() string {
	if o.Stamp == "" {
		return "backup"
	}
	return o.Stamp
}

// StepFunc wraps one labelled step. The cli passes ui.Step; this package does not print.
type StepFunc func(label string, fn func() error) error

func tail(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > 8 {
		lines = lines[len(lines)-8:]
	}
	return strings.Join(lines, "\n")
}

// ApplyTLS runs the whole sequence on the VM. Safe to rerun: an existing certificate skips the
// port-80 stage, certbot keeps an unexpired one, and the vhost is rewritten only after nginx -t
// passes on it.
func (m *Manifest) ApplyTLS(r sshx.Runner, o TLSOptions, step StepFunc) error {
	t := m.TLS
	if t == nil {
		return fmt.Errorf("the manifest has no tls block")
	}
	path := t.VhostPath()
	run := func(cmd string) error {
		out, err := r.Run(cmd)
		if err != nil {
			return fmt.Errorf("%w: %s", err, tail(out))
		}
		return nil
	}

	var state VhostState
	var certPresent bool
	var ssl SSLFiles
	if err := step("tls: inspect "+path, func() error {
		out, err := r.Run(t.probeVhostCmd())
		if err != nil {
			return fmt.Errorf("%w: %s", err, tail(out))
		}
		state = VhostState(strings.TrimSpace(out))
		switch state {
		case VhostAbsent, VhostManaged, VhostForeign:
		default:
			return fmt.Errorf("could not tell what is at %s: %q", path, strings.TrimSpace(out))
		}
		if state == VhostForeign && !o.Adopt {
			return fmt.Errorf("%s exists and was not written by BaryoVM: pass --adopt to take it over (the old file is kept as a backup)", path)
		}
		out, err = r.Run(t.probeCertCmd())
		if err != nil {
			return fmt.Errorf("%w: %s", err, tail(out))
		}
		certPresent = strings.TrimSpace(out) == "present"
		out, err = r.Run(probeSSLCmd())
		if err != nil {
			return fmt.Errorf("%w: %s", err, tail(out))
		}
		ssl = SSLFiles{Options: strings.Contains(out, "options"), DHParam: strings.Contains(out, "dhparam")}
		return nil
	}); err != nil {
		return err
	}

	if state == VhostForeign {
		bak := path + ".baryovm-bak-" + o.stamp()
		if err := step("tls: back up "+path, func() error { return run(cpCmd(path, bak)) }); err != nil {
			return err
		}
	}

	// write puts content at the vhost path and keeps it only if nginx -t accepts it. The previous
	// file is restored on failure, or the new one removed if there was none, and nginx is not
	// reloaded either way: a reload of a config that failed its test is how a bad vhost takes down
	// every other site on the box.
	exists := state != VhostAbsent
	write := func(label, content string) error {
		return step(label, func() error {
			prev := path + prevSuffix
			if exists {
				if err := run(cpCmd(path, prev)); err != nil {
					return err
				}
			}
			if err := run(writeCmd(path, content)); err != nil {
				return err
			}
			if err := run("sudo -n nginx -t"); err != nil {
				restore := "sudo -n rm -f " + sshx.Quote(path)
				if exists {
					restore = "sudo -n mv -f " + sshx.Quote(prev) + " " + sshx.Quote(path)
				}
				if rerr := run(restore); rerr != nil {
					return fmt.Errorf("nginx -t failed (%v) and restoring %s also failed: %w", err, path, rerr)
				}
				return fmt.Errorf("nginx -t rejected the new config, previous file restored and nginx not reloaded: %w", err)
			}
			exists = true
			_ = run("sudo -n rm -f " + sshx.Quote(prev))
			return nil
		})
	}
	reload := func(label string) error {
		return step(label, func() error { return run("sudo -n systemctl reload nginx") })
	}

	if err := step("tls: webroot", func() error { return run("sudo -n mkdir -p " + sshx.Quote(acmeWebroot)) }); err != nil {
		return err
	}
	if !certPresent {
		if err := write("tls: port 80 vhost", t.ACMEVhost()); err != nil {
			return err
		}
		if err := reload("tls: reload nginx"); err != nil {
			return err
		}
	}
	if err := step("tls: certificate", func() error { return run(t.CertbotCmd()) }); err != nil {
		return err
	}
	if err := write("tls: vhost", t.Vhost(m.RemoteRoot, ssl)); err != nil {
		return err
	}
	return reload("tls: reload nginx")
}

// TLSPlan is what a dry run reports: the two configs and the commands in order. Commands that
// write a file are shown as "write <path>" because their payload is the config printed alongside.
type TLSPlan struct {
	VhostPath string   `json:"vhostPath"`
	ACMEVhost string   `json:"acmeVhost"`
	Vhost     string   `json:"vhost"`
	Commands  []string `json:"commands"`
}

// PlanTLS builds the dry-run plan without touching a host. It assumes the worst case: no
// certificate yet and both certbot helper files present.
func (m *Manifest) PlanTLS(o TLSOptions) TLSPlan {
	t := m.TLS
	path := t.VhostPath()
	cmds := []string{
		"inspect: " + t.probeVhostCmd(),
		"inspect: " + t.probeCertCmd(),
		"inspect: " + probeSSLCmd(),
	}
	if o.Adopt {
		cmds = append(cmds, "if the file is not BaryoVM's: "+cpCmd(path, path+".baryovm-bak-"+o.stamp()))
	}
	cmds = append(cmds,
		"sudo -n mkdir -p "+sshx.Quote(acmeWebroot),
		"write "+path+" (port 80 vhost, only when no certificate exists)",
		"sudo -n nginx -t",
		"sudo -n systemctl reload nginx",
		t.CertbotCmd(),
		"write "+path+" (full vhost)",
		"sudo -n nginx -t",
		"sudo -n systemctl reload nginx",
	)
	return TLSPlan{
		VhostPath: path,
		ACMEVhost: t.ACMEVhost(),
		Vhost:     t.Vhost(m.RemoteRoot, SSLFiles{Options: true, DHParam: true}),
		Commands:  cmds,
	}
}

// TLSDryRun renders the plan as text.
func (m *Manifest) TLSDryRun(o TLSOptions) string {
	p := m.PlanTLS(o)
	var b strings.Builder
	b.WriteString("# port 80 vhost, " + p.VhostPath + "\n" + p.ACMEVhost + "\n")
	b.WriteString("# full vhost, " + p.VhostPath + "\n" + p.Vhost + "\n")
	b.WriteString("# commands\n")
	for i, c := range p.Commands {
		b.WriteString(fmt.Sprintf("%d. %s\n", i+1, c))
	}
	return b.String()
}
