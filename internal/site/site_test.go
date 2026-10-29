// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package site

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
)

func TestValidateDomain(t *testing.T) {
	for _, d := range []string{"example.com", "a-b.example.co.uk", "x1.io"} {
		if err := ValidateDomain(d); err != nil {
			t.Errorf("%q refused: %v", d, err)
		}
	}
	for _, d := range []string{"", "localhost", "Example.com", "a b.com", "a;b.com", "a.com;rm -rf /", "a.com\nb.com", "a'b.com", "-a.com", "a-.com", "a..com", "a.com/x", "a$(id).com", strings.Repeat("a", 64) + ".com"} {
		if err := ValidateDomain(d); err == nil {
			t.Errorf("%q accepted", d)
		}
	}
}

func TestValidateRoot(t *testing.T) {
	for _, r := range []string{"/var/www/example.com", "/srv/sites/a_b-c", "/var/www/a/b"} {
		if err := ValidateRoot(r); err != nil {
			t.Errorf("%q refused: %v", r, err)
		}
	}
	for _, r := range []string{"", "/", "//", "relative/path", "./x", "/var/www/../etc", "/var/www/x/..", "/etc", "/var/www", "/var/www/", ACMEDir, "/a b", "/a;b", "/a'b", "/a\nb", "/a$(id)", "~/x"} {
		if err := ValidateRoot(r); err == nil {
			t.Errorf("%q accepted", r)
		}
	}
}

func TestOptionsValidateChecksEmail(t *testing.T) {
	o := Options{Domain: "example.com", Root: "/var/www/example.com", Email: "bad"}
	if o.Validate() == nil {
		t.Error("bad email accepted")
	}
	o.Email = "ops@example.com"
	if err := o.Validate(); err != nil {
		t.Error(err)
	}
}

func TestCommandsExactStrings(t *testing.T) {
	const dom, root = "example.com", "/var/www/example.com"
	conf := "'/etc/nginx/conf.d/example.com.conf'"
	cases := []struct{ name, got, want string }{
		{"mkdir", MkdirCmd(root), "sudo -n mkdir -p '/var/www/example.com' '/var/www/letsencrypt'"},
		{"restorecon", RestoreconCmd(root), "sudo -n restorecon -R '/var/www/example.com'"},
		{"test", TestCmd(), "sudo -n nginx -t"},
		{"reload", ReloadCmd(), "sudo -n systemctl reload nginx"},
		{"backup", BackupCmd(dom), "sudo -n cp -p " + conf + " '/etc/nginx/conf.d/example.com.conf.baryovm-prev'"},
		{"restore previous", RestoreCmd(dom, true), "sudo -n mv -f '/etc/nginx/conf.d/example.com.conf.baryovm-prev' " + conf},
		{"restore none", RestoreCmd(dom, false), "sudo -n rm -f " + conf},
		{"drop backup", DropBackupCmd(dom), "sudo -n rm -f '/etc/nginx/conf.d/example.com.conf.baryovm-prev'"},
		{"certbot", CertbotCmd(dom, "ops@example.com"),
			"sudo -n certbot certonly --webroot -w '/var/www/letsencrypt' --non-interactive --agree-tos -m 'ops@example.com' --keep-until-expiring --cert-name 'example.com' -d 'example.com'"},
		{"certbot no email", CertbotCmd(dom, ""),
			"sudo -n certbot certonly --webroot -w '/var/www/letsencrypt' --non-interactive --agree-tos --register-unsafely-without-email --keep-until-expiring --cert-name 'example.com' -d 'example.com'"},
		{"write", WriteCmd(dom, "hi"), "printf '%s' 'aGk=' | base64 -d | sudo -n tee " + conf + " > /dev/null"},
		{"selinux probe", ProbeSELinuxCmd(), "if command -v selinuxenabled >/dev/null 2>&1 && selinuxenabled; then echo selinux; else echo none; fi"},
		{"conf probe", ProbeConfCmd(dom),
			`sudo -n "${SHELL:-/bin/sh}" -c 'if [ -e '\''/etc/nginx/conf.d/example.com.conf'\'' ]; then if head -n 1 '\''/etc/nginx/conf.d/example.com.conf'\'' | grep -qF '\''Managed by BaryoVM'\''; then echo managed; else echo foreign; fi; else echo absent; fi'`},
		{"cert probe", ProbeCertCmd(dom),
			`sudo -n "${SHELL:-/bin/sh}" -c 'if [ -f '\''/etc/letsencrypt/live/example.com/fullchain.pem'\'' ]; then echo present; else echo missing; fi'`},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s:\n got  %s\n want %s", c.name, c.got, c.want)
		}
	}
}

func TestEveryElevatedCommandUsesSudoN(t *testing.T) {
	for name, c := range map[string]string{
		"mkdir": MkdirCmd("/var/www/x"), "restorecon": RestoreconCmd("/var/www/x"), "backup": BackupCmd("a.com"),
		"write": WriteCmd("a.com", "x"), "test": TestCmd(), "reload": ReloadCmd(), "certbot": CertbotCmd("a.com", ""),
		"restore": RestoreCmd("a.com", true), "drop": DropBackupCmd("a.com"), "probe": ProbeConfCmd("a.com"),
	} {
		if !strings.Contains(c, "sudo -n ") {
			t.Errorf("%s does not use sudo -n: %s", name, c)
		}
		if strings.Contains(strings.ReplaceAll(c, "sudo -n ", ""), "sudo") {
			t.Errorf("%s has a bare sudo: %s", name, c)
		}
	}
}

func TestInjectionStaysQuoted(t *testing.T) {
	// The validators refuse these, so the quoting is the second wall: even handed a hostile value,
	// no command may let it out of its single quotes.
	evil := "x'; touch /tmp/pwn; echo '"
	for name, c := range map[string]string{
		"mkdir": MkdirCmd(evil), "restorecon": RestoreconCmd(evil), "certbot": CertbotCmd(evil, evil),
		"restore": RestoreCmd(evil, true), "backup": BackupCmd(evil),
	} {
		if strings.Contains(c, evil) {
			t.Errorf("%s interpolated an unquoted value: %s", name, c)
		}
		if !strings.Contains(c, `'\''`) {
			t.Errorf("%s did not escape the quote: %s", name, c)
		}
	}
}

func TestHTTPBlockIsPort80Only(t *testing.T) {
	b := HTTPBlock(Options{Domain: "example.com", Root: "/var/www/example.com"})
	if !strings.HasPrefix(b, "# Managed by BaryoVM") {
		t.Errorf("first line is not the marker: %q", strings.SplitN(b, "\n", 2)[0])
	}
	for _, want := range []string{"listen 80;", "server_name example.com;", "location /.well-known/acme-challenge/ {", "root /var/www/letsencrypt;"} {
		if !strings.Contains(b, want) {
			t.Errorf("http block lacks %q:\n%s", want, b)
		}
	}
	for _, bad := range []string{"443", "ssl_certificate", "gzip"} {
		if strings.Contains(b, bad) {
			t.Errorf("http block has %q:\n%s", bad, b)
		}
	}
}

func TestFullBlockTemplate(t *testing.T) {
	o := Options{Domain: "example.com", Root: "/var/www/example.com"}
	b := FullBlock(o, SSLFiles{Options: true, DHParam: true})
	for _, want := range []string{
		"# Managed by BaryoVM (baryovm site add)",
		"return 301 https://example.com$request_uri;",
		"listen 443 ssl;",
		"ssl_certificate /etc/letsencrypt/live/example.com/fullchain.pem;",
		"ssl_certificate_key /etc/letsencrypt/live/example.com/privkey.pem;",
		"include /etc/letsencrypt/options-ssl-nginx.conf;",
		"ssl_dhparam /etc/letsencrypt/ssl-dhparams.pem;",
		"root /var/www/example.com;",
		"try_files $uri $uri.html $uri/ =404;",
		"location ^~ /_next/static/ {",
		"location ^~ /assets/ {",
		`add_header Cache-Control "public, max-age=31536000, immutable" always;`,
		`add_header Cache-Control "no-cache" always;`,
		"location ~ /\\. {",
	} {
		if !strings.Contains(b, want) {
			t.Errorf("full block lacks %q", want)
		}
	}
	// gzip: the exact block from #18, once per static location (two hashed prefixes plus /), never
	// at server level or for text/html.
	gz := "        gzip on;\n        gzip_comp_level 5;\n        gzip_min_length 1024;\n        gzip_vary on;\n        gzip_types application/javascript text/javascript text/css image/svg+xml application/wasm application/json;\n"
	if n := strings.Count(b, gz); n != 3 || strings.Count(b, "gzip on;") != 3 {
		t.Errorf("gzip block appears %d times, want 3 (inside each static location only)", n)
	}
	if strings.Contains(b, "text/html") {
		t.Error("HTML is in gzip_types")
	}
	for _, line := range strings.Split(b, "\n") {
		if strings.Contains(line, "gzip") && !strings.HasPrefix(line, "        ") {
			t.Errorf("gzip outside a location: %q", line)
		}
	}
	if strings.Contains(b, "proxy_pass") {
		t.Error("a static site block proxies")
	}
	// Braces balance: a template slip fails nginx -t on the host.
	if strings.Count(b, "{") != strings.Count(b, "}") {
		t.Errorf("unbalanced braces:\n%s", b)
	}
}

func TestFullBlockOmitsMissingCertbotHelpers(t *testing.T) {
	b := FullBlock(Options{Domain: "example.com", Root: "/var/www/example.com"}, SSLFiles{})
	if strings.Contains(b, "options-ssl-nginx.conf") || strings.Contains(b, "ssl-dhparams.pem") {
		t.Errorf("helper files included though absent:\n%s", b)
	}
}

func TestWriteCmdPayloadRoundTrips(t *testing.T) {
	blk := FullBlock(Options{Domain: "example.com", Root: "/var/www/example.com"}, SSLFiles{})
	cmd := WriteCmd("example.com", blk)
	enc := cmd[strings.Index(cmd, "' '")+3:]
	enc = enc[:strings.Index(enc, "'")]
	dec, err := base64.StdEncoding.DecodeString(enc)
	if err != nil || string(dec) != blk {
		t.Errorf("payload did not round trip: %v", err)
	}
}

// fakeRemote answers commands by substring and records every one in order.
type fakeRemote struct {
	seen  []string
	out   map[string]string // substring of the command to its stdout
	fail  map[string]bool   // substring of the command to fail
	count map[string]int
}

func (f *fakeRemote) Run(cmd string) (string, error) {
	f.seen = append(f.seen, cmd)
	for m := range f.fail {
		if strings.Contains(cmd, m) {
			if f.count == nil {
				f.count = map[string]int{}
			}
			f.count[m]++
			return "boom on " + m, fmt.Errorf("exit status 1")
		}
	}
	for m, o := range f.out {
		if strings.Contains(cmd, m) {
			return o, nil
		}
	}
	return "", nil
}

func plainStep(_ string, fn func() error) error { return fn() }

var testOpts = Options{Domain: "example.com", Root: "/var/www/example.com", Email: "ops@example.com"}

// kind turns a recorded command into a short label, so an order assertion reads as a sequence.
func kind(cmd string) string {
	switch {
	case strings.Contains(cmd, "head -n 1"):
		return "probe conf"
	case strings.Contains(cmd, "options-ssl-nginx.conf") && strings.Contains(cmd, "echo options"):
		return "probe ssl"
	case strings.Contains(cmd, "selinuxenabled"):
		return "probe selinux"
	case strings.Contains(cmd, "mkdir -p"):
		return "mkdir"
	case strings.Contains(cmd, "restorecon"):
		return "restorecon"
	case strings.Contains(cmd, "base64 -d"):
		enc := cmd[strings.Index(cmd, "printf '%s' '")+13:]
		dec, _ := base64.StdEncoding.DecodeString(enc[:strings.Index(enc, "'")])
		if strings.Contains(string(dec), "443") {
			return "write full"
		}
		return "write http"
	case strings.Contains(cmd, "nginx -t"):
		return "nginx -t"
	case strings.Contains(cmd, "systemctl reload"):
		return "reload"
	case strings.Contains(cmd, "certbot"):
		return "certbot"
	case strings.Contains(cmd, "cp -p"):
		return "backup"
	case strings.Contains(cmd, "mv -f"):
		return "restore prev"
	case strings.Contains(cmd, "rm -f") && strings.Contains(cmd, ".baryovm-prev"):
		return "drop backup"
	case strings.Contains(cmd, "rm -f"):
		return "remove new"
	}
	return cmd
}

func kinds(f *fakeRemote) []string {
	var k []string
	for _, c := range f.seen {
		k = append(k, kind(c))
	}
	return k
}

func eq(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, " | ") != strings.Join(want, " | ") {
		t.Errorf("order:\n got  %s\n want %s", strings.Join(got, " | "), strings.Join(want, " | "))
	}
}

func TestAddHappyPathOrder(t *testing.T) {
	f := &fakeRemote{out: map[string]string{"head -n 1": "absent\n", "echo options": "options\ndhparam\n", "selinuxenabled": "selinux\n"}}
	rep, err := Add(f, testOpts, plainStep)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, kinds(f), []string{
		"probe conf", "probe ssl", "probe selinux", "mkdir", "restorecon",
		"write http", "nginx -t", "reload", "certbot", "write full", "nginx -t", "reload",
	})
	if !rep.SELinux || rep.RolledBack || rep.FailedStep != "" || len(rep.Steps) != 8 {
		t.Errorf("report = %+v", rep)
	}
}

func TestAddWithoutSELinuxSkipsRestorecon(t *testing.T) {
	f := &fakeRemote{out: map[string]string{"head -n 1": "absent\n", "selinuxenabled": "none\n"}}
	rep, err := Add(f, testOpts, plainStep)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range kinds(f) {
		if k == "restorecon" {
			t.Error("restorecon ran without SELinux")
		}
	}
	if rep.SELinux {
		t.Error("selinux reported")
	}
}

func TestPlanMatchesTheRealOrder(t *testing.T) {
	f := &fakeRemote{out: map[string]string{"head -n 1": "absent\n", "echo options": "options\ndhparam\n", "selinuxenabled": "selinux\n"}}
	if _, err := Add(f, testOpts, plainStep); err != nil {
		t.Fatal(err)
	}
	var planned []string
	for _, s := range Plan(testOpts) {
		if strings.HasPrefix(s.Command, "write ") {
			if strings.Contains(s.Command, "full") {
				planned = append(planned, "write full")
			} else {
				planned = append(planned, "write http")
			}
			continue
		}
		planned = append(planned, kind(s.Command))
	}
	eq(t, planned, kinds(f))
}

func TestAddNginxTestFailureRollsBackAndNamesTheStep(t *testing.T) {
	// The full block is rejected after the HTTP block was reloaded: the original (none) is
	// removed and nginx is reloaded back to it. It is never reloaded with the bad file.
	f := &fakeRemote{out: map[string]string{"head -n 1": "absent\n", "selinuxenabled": "none\n"}}
	// Fail only the second nginx -t.
	calls := 0
	g := &funcRemote{f: f, hook: func(cmd string) error {
		if kind(cmd) == "nginx -t" {
			calls++
			if calls == 2 {
				return fmt.Errorf("exit status 1")
			}
		}
		return nil
	}}
	rep, err := Add(g, testOpts, plainStep)
	var se *StepError
	if err == nil || !asStep(err, &se) || se.Step != StepFullBlock {
		t.Fatalf("err = %v, want a StepError for %q", err, StepFullBlock)
	}
	if rep.FailedStep != StepFullBlock || !rep.RolledBack {
		t.Errorf("report = %+v", rep)
	}
	eq(t, kinds(f), []string{
		"probe conf", "probe ssl", "probe selinux", "mkdir",
		"write http", "nginx -t", "reload", "certbot",
		"write full", "nginx -t", "remove new", "nginx -t", "reload",
	})
}

func TestAddNginxTestFailureOnFirstWriteNeverReloads(t *testing.T) {
	f := &fakeRemote{out: map[string]string{"head -n 1": "managed\n", "selinuxenabled": "none\n"}, fail: map[string]bool{"nginx -t": true}}
	o := testOpts
	o.Force = true
	rep, err := Add(f, o, plainStep)
	if err == nil || rep.FailedStep != StepHTTPBlock || !rep.RolledBack {
		t.Fatalf("err = %v, report = %+v", err, rep)
	}
	if !strings.Contains(err.Error(), StepHTTPBlock) || !strings.Contains(err.Error(), "nginx -t rejected") {
		t.Errorf("error does not name the step and cause: %v", err)
	}
	// The previous file is put back; nginx was never reloaded, so nothing needs reloading.
	eq(t, kinds(f), []string{
		"probe conf", "probe ssl", "probe selinux", "mkdir",
		"backup", "write http", "nginx -t", "restore prev",
	})
}

func TestAddCertbotFailureRestoresTheLastGoodState(t *testing.T) {
	f := &fakeRemote{out: map[string]string{"head -n 1": "absent\n", "selinuxenabled": "none\n"}, fail: map[string]bool{"certbot": true}}
	rep, err := Add(f, testOpts, plainStep)
	if err == nil || rep.FailedStep != StepCert || !rep.RolledBack {
		t.Fatalf("err = %v, report = %+v", err, rep)
	}
	if !strings.Contains(err.Error(), `"certificate"`) {
		t.Errorf("error does not name the step: %v", err)
	}
	// The HTTP block was live; the file is removed and nginx reloaded back to no site, so the
	// host is not left redirecting a name to a certificate that does not exist.
	eq(t, kinds(f), []string{
		"probe conf", "probe ssl", "probe selinux", "mkdir",
		"write http", "nginx -t", "reload", "certbot",
		"remove new", "nginx -t", "reload",
	})
}

func TestAddForceWithExistingFileRestoresIt(t *testing.T) {
	f := &fakeRemote{out: map[string]string{"head -n 1": "foreign\n", "selinuxenabled": "none\n"}, fail: map[string]bool{"certbot": true}}
	o := testOpts
	o.Force = true
	if _, err := Add(f, o, plainStep); err == nil {
		t.Fatal("expected failure")
	}
	k := kinds(f)
	eq(t, k[len(k)-3:], []string{"restore prev", "nginx -t", "reload"})
	if k[4] != "backup" {
		t.Errorf("the original was not copied aside before the first write: %v", k)
	}
}

func TestAddRefusesAnExistingSiteWithoutForce(t *testing.T) {
	for _, state := range []string{"managed\n", "foreign\n"} {
		f := &fakeRemote{out: map[string]string{"head -n 1": state}}
		rep, err := Add(f, testOpts, plainStep)
		if err == nil || !strings.Contains(err.Error(), "--force") || rep.FailedStep != StepInspect {
			t.Errorf("%q: err = %v, report = %+v", state, err, rep)
		}
		if len(f.seen) != 1 {
			t.Errorf("%q: ran %d commands, want only the probe: %v", state, len(f.seen), kinds(f))
		}
	}
}

func TestAddReloadFailureRestoresToo(t *testing.T) {
	f := &fakeRemote{out: map[string]string{"head -n 1": "absent\n", "selinuxenabled": "none\n"}, fail: map[string]bool{"systemctl reload": true}}
	rep, err := Add(f, testOpts, plainStep)
	if err == nil || rep.FailedStep != StepHTTPReload || !rep.RolledBack {
		t.Fatalf("err = %v, report = %+v", err, rep)
	}
	for _, k := range kinds(f) {
		if k == "certbot" {
			t.Error("certbot ran after the reload failed")
		}
	}
}

func TestAddRefusesInvalidInputBeforeAnyCommand(t *testing.T) {
	for _, o := range []Options{
		{Domain: "a;b.com", Root: "/var/www/x"},
		{Domain: "example.com", Root: "/"},
		{Domain: "example.com", Root: "/var/www/../etc"},
		{Domain: "example.com", Root: "relative"},
	} {
		f := &fakeRemote{}
		if _, err := Add(f, o, plainStep); err == nil {
			t.Errorf("%+v accepted", o)
		}
		if len(f.seen) != 0 {
			t.Errorf("%+v ran %v", o, f.seen)
		}
	}
}

func TestFinishOrder(t *testing.T) {
	f := &fakeRemote{out: map[string]string{"selinuxenabled": "selinux\n"}}
	if _, err := Finish(f, "/var/www/example.com", plainStep); err != nil {
		t.Fatal(err)
	}
	eq(t, kinds(f), []string{"probe selinux", "restorecon", "nginx -t", "reload"})

	f = &fakeRemote{out: map[string]string{"selinuxenabled": "none\n"}, fail: map[string]bool{"nginx -t": true}}
	done, err := Finish(f, "/var/www/example.com", plainStep)
	if err == nil || !strings.Contains(err.Error(), `"nginx -t"`) {
		t.Fatalf("err = %v", err)
	}
	eq(t, kinds(f), []string{"probe selinux", "nginx -t"})
	if len(done) != 1 {
		t.Errorf("done = %v", done)
	}
}

// funcRemote wraps a fakeRemote and lets a test fail one particular call.
type funcRemote struct {
	f    *fakeRemote
	hook func(cmd string) error
}

func (r *funcRemote) Run(cmd string) (string, error) {
	out, err := r.f.Run(cmd)
	if err != nil {
		return out, err
	}
	if e := r.hook(cmd); e != nil {
		return "rejected", e
	}
	return out, nil
}

func asStep(err error, target **StepError) bool {
	for err != nil {
		if se, ok := err.(*StepError); ok {
			*target = se
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
