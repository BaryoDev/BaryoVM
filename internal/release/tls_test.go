// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package release

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func golden(t *testing.T, name, got string) {
	t.Helper()
	want, err := os.ReadFile(filepath.Join("testdata", "tls", name+".golden"))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if got != string(want) {
		t.Fatalf("%s differs from golden.\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

var bothSSL = SSLFiles{Options: true, DHParam: true}

func TestVhostGolden(t *testing.T) {
	cases := []struct {
		name string
		tls  TLS
		root string
	}{
		{"static", TLS{Hosts: []string{"example.com", "www.example.com"}, Canonical: "example.com", Email: "ops@example.com"}, "/var/www/example"},
		{"redirects", TLS{Hosts: []string{"example.com"}, Redirects: []string{"www.example.com", "old.example.com"}, Canonical: "example.com", Email: "ops@example.com", Root: "/srv/site"}, "/var/www/ignored"},
		{"proxy", TLS{Hosts: []string{"app.example.com"}, Email: "ops@example.com", Proxy: &Proxy{Port: 5012, MaxBodySize: "32m"}}, "/unused"},
		{"proxy_large", TLS{Hosts: []string{"app.example.com"}, Email: "ops@example.com", Proxy: &Proxy{Port: 5012, MaxBodySize: "32m", LargeHeaders: true}}, "/unused"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			if err := c.tls.Validate(c.root); err != nil {
				t.Fatalf("validate: %v", err)
			}
			golden(t, c.name, c.tls.Vhost(c.root, bothSSL))
		})
	}
}

func TestACMEVhostGolden(t *testing.T) {
	tl := TLS{Hosts: []string{"example.com", "www.example.com"}, Canonical: "example.com", Email: "ops@example.com"}
	golden(t, "acme", tl.ACMEVhost())
}

func TestVhostOmitsSSLFilesThatAreAbsent(t *testing.T) {
	tl := TLS{Hosts: []string{"example.com"}, Email: "ops@example.com"}
	out := tl.Vhost("/var/www/x", SSLFiles{})
	if strings.Contains(out, "options-ssl-nginx.conf") || strings.Contains(out, "ssl_dhparam") {
		t.Fatalf("absent files were included:\n%s", out)
	}
	if !strings.Contains(out, "ssl_certificate /etc/letsencrypt/live/example.com/fullchain.pem;") {
		t.Fatalf("certificate path missing:\n%s", out)
	}
}

func TestGeneratedFilesCarryTheOwnershipMarker(t *testing.T) {
	tl := TLS{Hosts: []string{"example.com"}, Email: "ops@example.com"}
	for name, body := range map[string]string{"vhost": tl.Vhost("/x", bothSSL), "acme": tl.ACMEVhost()} {
		first := strings.SplitN(body, "\n", 2)[0]
		if !strings.Contains(first, VhostMarker) {
			t.Errorf("%s: first line lacks the marker: %q", name, first)
		}
	}
}

func TestTLSRejectsHostNamesThatAreNotPlainDNS(t *testing.T) {
	bad := []string{
		"", "exa mple.com", "example.com;", "example.com\nserver_name evil.com", "a.com{", "*.example.com",
		"under_score.example.com", "-lead.example.com", "trail-.example.com", "a..b.com", "example.com/", "$host",
		"exa'mple.com", "example.com#", "http://example.com", "example.com:443", "..", strings.Repeat("a", 64) + ".com",
	}
	for _, h := range bad {
		tl := TLS{Hosts: []string{h}, Email: "ops@example.com"}
		if err := tl.Validate("/var/www/x"); err == nil {
			t.Errorf("host %q was accepted", h)
		}
		tl = TLS{Hosts: []string{"example.com"}, Redirects: []string{h}, Email: "ops@example.com"}
		if err := tl.Validate("/var/www/x"); err == nil {
			t.Errorf("redirect %q was accepted", h)
		}
	}
}

func TestTLSAcceptsPlainHostNames(t *testing.T) {
	tl := TLS{Hosts: []string{"example.com", "www.example.com", "a-b.c1.example.co.uk", "localhost"}, Email: "ops@example.com"}
	if err := tl.Validate("/var/www/x"); err != nil {
		t.Fatalf("plain names refused: %v", err)
	}
}

func TestTLSRejectsOtherBadFields(t *testing.T) {
	ok := func() TLS { return TLS{Hosts: []string{"example.com"}, Email: "ops@example.com"} }
	cases := map[string]func(*TLS){
		"no hosts":            func(t *TLS) { t.Hosts = nil },
		"canonical not host":  func(t *TLS) { t.Canonical = "other.example.com" },
		"no email":            func(t *TLS) { t.Email = "" },
		"email with space":    func(t *TLS) { t.Email = "a b@example.com" },
		"email with quote":    func(t *TLS) { t.Email = "a'b@example.com" },
		"relative root":       func(t *TLS) { t.Root = "site" },
		"root with semicolon": func(t *TLS) { t.Root = "/srv/a;b" },
		"port zero":           func(t *TLS) { t.Proxy = &Proxy{Port: 0} },
		"port too big":        func(t *TLS) { t.Proxy = &Proxy{Port: 70000} },
		"body size injection": func(t *TLS) { t.Proxy = &Proxy{Port: 80, MaxBodySize: "1m; evil"} },
	}
	for name, mut := range cases {
		tl := ok()
		mut(&tl)
		if err := tl.Validate("/var/www/x"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	tl := ok()
	if err := tl.Validate(""); err == nil {
		t.Error("static site with no root and no remoteRoot was accepted")
	}
}

func TestStaticVhostDeniesDotFiles(t *testing.T) {
	tl := TLS{Hosts: []string{"example.com"}, Email: "ops@example.com"}
	out := tl.Vhost("/var/www/x", bothSSL)
	wk := strings.Index(out, "location ^~ /.well-known/")
	dot := strings.Index(out, `location ~ /\. {`)
	root := strings.LastIndex(out, "location / {")
	if wk < 0 || dot < 0 || (wk >= dot || dot >= root) {
		t.Fatalf("want .well-known, then the dot rule, then location /:\n%s", out)
	}
	proxy := TLS{Hosts: []string{"example.com"}, Email: "ops@example.com", Proxy: &Proxy{Port: 80}}
	if strings.Contains(proxy.Vhost("/x", bothSSL), `location ~ /\.`) {
		t.Fatal("a proxy vhost serves no files, it should not carry the dot rule")
	}
}

func TestTLSRefusesRootsThatExposeTheHost(t *testing.T) {
	for _, r := range []string{"/", "/etc", "/root", "/home", "/var", "/usr", "/opt", "/etc/", "/srv/../etc", "/srv/site/..", "/../x"} {
		tl := TLS{Hosts: []string{"example.com"}, Email: "ops@example.com", Root: r}
		if err := tl.Validate("/var/www/x"); err == nil {
			t.Errorf("root %q was accepted", r)
		}
	}
	tl := TLS{Hosts: []string{"example.com"}, Email: "ops@example.com"}
	if err := tl.Validate("/etc"); err == nil {
		t.Error("remoteRoot /etc was accepted as the fallback root")
	}
}

func TestTLSAcceptsASubdirectoryOfASystemRoot(t *testing.T) {
	for _, r := range []string{"/opt/site/out", "/var/www/example", "/srv/site"} {
		tl := TLS{Hosts: []string{"example.com"}, Email: "ops@example.com", Root: r}
		if err := tl.Validate("/unused"); err != nil {
			t.Errorf("root %q refused: %v", r, err)
		}
	}
}

func TestLoadValidatesTLS(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "m.json")
	body := `{"localRoot":` + strconv.Quote(dir) + `,"remoteRoot":"/var/www/x","sync":["out/"],"tls":{"hosts":["bad host"],"email":"ops@example.com"}}`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p, false); err == nil {
		t.Fatal("manifest with an invalid tls host loaded")
	}
	body = `{"localRoot":` + strconv.Quote(dir) + `,"remoteRoot":"/var/www/x","sync":["out/"],"tls":{"hosts":["example.com"],"email":"ops@example.com"}}`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := Load(p, false)
	if err != nil || m.TLS == nil {
		t.Fatalf("valid tls block: %v %+v", err, m)
	}
}

func TestCertbotCmd(t *testing.T) {
	tl := TLS{Hosts: []string{"www.example.com", "example.com"}, Redirects: []string{"old.example.com"}, Canonical: "example.com", Email: "ops@example.com"}
	got := tl.CertbotCmd()
	want := "sudo -n certbot certonly --webroot -w '/var/www/letsencrypt' --non-interactive --agree-tos -m 'ops@example.com' " +
		"--keep-until-expiring --cert-name 'example.com' -d 'example.com' -d 'www.example.com' -d 'old.example.com'"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

// fakeHost answers by substring and records everything, like the runners elsewhere in this repo.
type fakeHost struct {
	seen   []string
	vhost  string // managed, foreign, absent
	cert   string // present, missing
	ssl    string
	failOn string // substring of a command that fails
}

func (f *fakeHost) Run(cmd string) (string, error) {
	f.seen = append(f.seen, cmd)
	if f.failOn != "" && strings.Contains(cmd, f.failOn) {
		return "nginx: configuration file test failed", fmt.Errorf("exit 1")
	}
	switch {
	case strings.Contains(cmd, "echo managed"):
		return f.vhost + "\n", nil
	case strings.Contains(cmd, "echo present"):
		return f.cert + "\n", nil
	case strings.Contains(cmd, "echo dhparam"):
		return f.ssl, nil
	}
	return "", nil
}

func (f *fakeHost) index(sub string) int {
	for i, c := range f.seen {
		if strings.Contains(c, sub) {
			return i
		}
	}
	return -1
}

func (f *fakeHost) wrote() []string {
	var out []string
	for _, c := range f.seen {
		if strings.Contains(c, "base64 -d") {
			out = append(out, c)
		}
	}
	return out
}

func tlsManifest() *Manifest {
	return &Manifest{LocalRoot: "/a", RemoteRoot: "/var/www/example",
		TLS: &TLS{Hosts: []string{"example.com"}, Email: "ops@example.com"}}
}

func direct(_ string, fn func() error) error { return fn() }

func TestApplyTLSRefusesAVhostWithoutTheMarker(t *testing.T) {
	h := &fakeHost{vhost: "foreign", cert: "missing"}
	err := tlsManifest().ApplyTLS(h, TLSOptions{}, direct)
	if err == nil || !strings.Contains(err.Error(), "--adopt") {
		t.Fatalf("want a refusal naming --adopt, got %v", err)
	}
	if len(h.wrote()) != 0 || h.index("certbot") >= 0 || h.index("reload") >= 0 {
		t.Fatalf("a refused run touched the host: %v", h.seen)
	}
}

func TestApplyTLSAdoptBacksUpTheOldFileFirst(t *testing.T) {
	h := &fakeHost{vhost: "foreign", cert: "present", ssl: "options\ndhparam\n"}
	if err := tlsManifest().ApplyTLS(h, TLSOptions{Adopt: true, Stamp: "20261028"}, direct); err != nil {
		t.Fatal(err)
	}
	bak := h.index("example.com.conf.baryovm-bak-20261028")
	w := h.index("base64 -d")
	if bak < 0 || w < 0 || bak > w {
		t.Fatalf("backup must come before the first write: %v", h.seen)
	}
}

func TestApplyTLSSequenceWhenNoCertificateYet(t *testing.T) {
	h := &fakeHost{vhost: "absent", cert: "missing", ssl: "options\ndhparam\n"}
	if err := tlsManifest().ApplyTLS(h, TLSOptions{}, direct); err != nil {
		t.Fatal(err)
	}
	w := h.wrote()
	if len(w) != 2 {
		t.Fatalf("want an ACME write then the real vhost, got %d writes", len(w))
	}
	cb := h.index("certbot")
	// certbot runs between the two writes.
	i1 := h.index("base64 -d")
	i2 := -1
	for i := i1 + 1; i < len(h.seen); i++ {
		if strings.Contains(h.seen[i], "base64 -d") {
			i2 = i
			break
		}
	}
	if i1 >= cb || cb >= i2 {
		t.Fatalf("certbot must sit between the ACME vhost and the real one: %v", h.seen)
	}
	last := h.seen[len(h.seen)-1]
	if !strings.Contains(last, "systemctl reload nginx") {
		t.Fatalf("last step should be the reload, got %q", last)
	}
}

func TestApplyTLSSkipsTheACMEStageWhenACertificateExists(t *testing.T) {
	h := &fakeHost{vhost: "managed", cert: "present", ssl: "options\ndhparam\n"}
	if err := tlsManifest().ApplyTLS(h, TLSOptions{}, direct); err != nil {
		t.Fatal(err)
	}
	if n := len(h.wrote()); n != 1 {
		t.Fatalf("want one write, got %d", n)
	}
	if h.index("certbot") < 0 {
		t.Fatal("certbot must still run so a new host name gets added")
	}
}

func TestApplyTLSRestoresAndDoesNotReloadWhenNginxTestFails(t *testing.T) {
	h := &fakeHost{vhost: "managed", cert: "present", ssl: "options\ndhparam\n", failOn: "nginx -t"}
	err := tlsManifest().ApplyTLS(h, TLSOptions{}, direct)
	if err == nil {
		t.Fatal("a failing nginx -t was reported as success")
	}
	if h.index("reload") >= 0 {
		t.Fatalf("nginx was reloaded after a failed test: %v", h.seen)
	}
	if h.index("mv -f") < 0 || !strings.Contains(strings.Join(h.seen, "\n"), ".baryovm-prev") {
		t.Fatalf("previous file was not restored: %v", h.seen)
	}
}

func TestApplyTLSRemovesTheNewFileWhenThereWasNoPreviousOne(t *testing.T) {
	h := &fakeHost{vhost: "absent", cert: "present", ssl: "options\ndhparam\n", failOn: "nginx -t"}
	if err := tlsManifest().ApplyTLS(h, TLSOptions{}, direct); err == nil {
		t.Fatal("expected failure")
	}
	if h.index("rm -f '/etc/nginx/conf.d/example.com.conf'") < 0 {
		t.Fatalf("the new file was left behind: %v", h.seen)
	}
}

func TestApplyTLSWritesTheRenderedVhost(t *testing.T) {
	h := &fakeHost{vhost: "managed", cert: "present", ssl: "options\ndhparam\n"}
	m := tlsManifest()
	if err := m.ApplyTLS(h, TLSOptions{}, direct); err != nil {
		t.Fatal(err)
	}
	cmd := h.wrote()[0]
	start := strings.Index(cmd, "printf '%s' '") + len("printf '%s' '")
	b64 := cmd[start : start+strings.Index(cmd[start:], "'")]
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != m.TLS.Vhost(m.RemoteRoot, bothSSL) {
		t.Fatalf("written content differs from the rendered vhost:\n%s", raw)
	}
}

func TestTLSDryRunMentionsEveryStepAndNeverNeedsAHost(t *testing.T) {
	out := tlsManifest().TLSDryRun(TLSOptions{Adopt: true, Stamp: "s"})
	for _, want := range []string{"certbot certonly", "nginx -t", "systemctl reload nginx", "listen 443 ssl", "acme-challenge", "/etc/nginx/conf.d/example.com.conf"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry run lacks %q:\n%s", want, out)
		}
	}
}

// nginx hashes variable names into buckets of variables_hash_bucket_size (64 by default), and a
// long name fails nginx -t with "could not build variables_hash". The upgrade map is named from
// the host, so a long host must still give a short name. Found on a real host with
// umbraco.playground.baryo.dev.
func TestUpgradeMapNameStaysShortForALongHost(t *testing.T) {
	long := strings.Repeat("a", 60) + "." + strings.Repeat("b", 60) + ".example.com"
	for _, h := range []string{"umbraco.playground.baryo.dev", long} {
		tl := TLS{Hosts: []string{h}, Email: "ops@example.com", Proxy: &Proxy{Port: 8080}}
		out := tl.Vhost("", bothSSL)
		m := regexp.MustCompile(`map \$http_upgrade (\$\S+) \{`).FindStringSubmatch(out)
		if m == nil {
			t.Fatalf("%s: no upgrade map in:\n%s", h, out)
		}
		if len(m[1]) > 24 {
			t.Errorf("%s: map variable %s is %d characters, want 24 or fewer", h, m[1], len(m[1]))
		}
		if !strings.Contains(out, "proxy_set_header Connection "+m[1]+";") {
			t.Errorf("%s: Connection header does not use %s", h, m[1])
		}
	}
	a := TLS{Hosts: []string{"a.example.com"}, Email: "ops@example.com", Proxy: &Proxy{Port: 1}}
	b := TLS{Hosts: []string{"b.example.com"}, Email: "ops@example.com", Proxy: &Proxy{Port: 1}}
	if a.upgradeVar() == b.upgradeVar() {
		t.Error("two hosts share one map name, and map names are global across conf.d")
	}
}
