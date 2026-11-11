// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package harden

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// filterRegexp turns the failregex into a Go regexp the way fail2ban would read it:
// <HOST> becomes an address group.
func filterRegexp(t *testing.T, php bool) *regexp.Regexp {
	t.Helper()
	re := strings.ReplaceAll(failregexLine(php), "<HOST>", `(?P<host>(?:\d{1,3}\.){3}\d{1,3}|[0-9a-fA-F:]+)`)
	r, err := regexp.Compile(re)
	if err != nil {
		t.Fatalf("failregex does not compile: %v", err)
	}
	return r
}

func logLine(request string) string {
	return `203.0.113.9 - - [10/Nov/2026:10:00:00 +0000] "` + request + `" 404 153 "-" "curl/8.0"`
}

var accessDate = regexp.MustCompile(`\[\d{2}/[A-Za-z]{3}/\d{4}:\d{2}:\d{2}:\d{2} [+-]\d{4}\]`)

// failSees is the line as fail2ban hands it to failregex: the matched timestamp is cut out
// first, which leaves empty brackets. A real fail2ban 1.1.0 host matched 0 of 9155 lines
// with \[[^\]]+\] for that reason.
func failSees(line string) string { return accessDate.ReplaceAllString(line, "[]") }

// hit runs the filter on a request the way fail2ban sees it.
func hit(r *regexp.Regexp, request string) bool { return r.MatchString(failSees(logLine(request))) }

func TestFilterMatchesScannerLines(t *testing.T) {
	r := filterRegexp(t, false)
	for _, req := range []string{
		"GET /.env HTTP/1.1",
		"GET /.env.local HTTP/1.1",
		"GET /.env_prod HTTP/1.1",
		"GET /.git_bak/config HTTP/1.1",
		"GET /.git/config HTTP/1.1",
		"GET /.aws/credentials HTTP/1.1",
		"GET /app/.ssh/id_rsa HTTP/1.1",
		"GET /.svn/entries HTTP/1.1",
		"GET /.hg/store HTTP/1.1",
		"GET /.htpasswd HTTP/1.1",
		"GET /.npmrc HTTP/1.1",
		"GET /.DS_Store HTTP/1.1",
		"GET /vendor/phpunit/phpunit/src/Util/PHP/eval-stdin.php HTTP/1.1",
		"GET /....//....//....//..../proc/self/environ HTTP/1.1",
		"POST /.env HTTP/1.1",
		"GET /a/etc/passwd HTTP/1.1",
		"GET /${jndi:ldap/x HTTP/1.1",
	} {
		m := r.FindStringSubmatch(failSees(logLine(req)))
		if m == nil {
			t.Errorf("scanner line was not matched: %s", req)
			continue
		}
		if got := m[r.SubexpIndex("host")]; got != "203.0.113.9" {
			t.Errorf("host for %q = %q", req, got)
		}
	}
}

// A page on another site can make a visitor's browser send these. Only the path may ban.
// One case with the date left in, for a filter fed a whole line.
func TestFilterMatchesAWholeLineToo(t *testing.T) {
	if !filterRegexp(t, false).MatchString(logLine("GET /.env HTTP/1.1")) {
		t.Error("a whole log line with its date was not matched")
	}
}

func TestFilterIgnoresProbesInTheQueryString(t *testing.T) {
	for _, php := range []bool{false, true} {
		r := filterRegexp(t, php)
		for _, req := range []string{
			"GET /?x=/.env HTTP/1.1",
			"GET /?path=php://filter/read=convert.base64-encode/resource=/etc/passwd HTTP/1.1",
			"GET /download?file=../../etc/passwd HTTP/1.1",
			"GET /page?next=/.git/config HTTP/1.1",
			"GET /?x=${jndi:ldap://203.0.113.5/a} HTTP/1.1",
			"GET /search?q=wp-login.php HTTP/1.1",
		} {
			if hit(r, req) {
				t.Errorf("php=%v: a query string triggered the filter: %s", php, req)
			}
		}
	}
	// the same path with a query string still matches, because the path is the probe
	if !hit(filterRegexp(t, false), "GET /.env?x=1 HTTP/1.1") {
		t.Error("a probe path with a query string was not matched")
	}
}

func TestFilterNeverMatchesRealTraffic(t *testing.T) {
	for _, php := range []bool{false, true} {
		r := filterRegexp(t, php)
		for _, req := range []string{
			"GET /.well-known/acme-challenge/Zm9vYmFy HTTP/1.1",
			"GET /.well-known/security.txt HTTP/1.1",
			"GET /_next/static/chunks/x.js HTTP/1.1",
			"GET /_next/static/x.js HTTP/1.1",
			"GET /api/content?x=1 HTTP/1.1",
			"GET / HTTP/1.1",
			"GET /about HTTP/1.1",
			"GET /.github/workflows/ci.yml HTTP/1.1",
			"GET /environment HTTP/1.1",
			"GET /blog/etc/passwd-guide HTTP/1.1",
			"GET /blog/etc/passwd_notes HTTP/1.1",
			"GET /vendor/phpunit-docs HTTP/1.1",
			"GET /assets/foo.php.min.js HTTP/1.1",
			"GET /api/x.php-looking HTTP/1.1",
			"GET /wp-admin-guide HTTP/1.1",
		} {
			if hit(r, req) {
				t.Errorf("php=%v: real traffic was matched: %s", php, req)
			}
		}
	}
}

func TestFilterPHPTierOnlyWithThePHPList(t *testing.T) {
	narrow, wide := filterRegexp(t, false), filterRegexp(t, true)
	for _, req := range []string{
		"GET /wp-admin/install.php HTTP/1.1",
		"GET /wp-login.php HTTP/1.1",
		"GET /wp-content/plugins/x/readme.txt HTTP/1.1",
		"GET /wp-includes/x.js HTTP/1.1",
		"POST /xmlrpc.php HTTP/1.1",
		"GET /shell.php HTTP/1.1",
	} {
		if hit(narrow, req) {
			t.Errorf("narrow filter matched a PHP path: %s", req)
		}
		if !hit(wide, req) {
			t.Errorf("PHP tier did not match: %s", req)
		}
	}
	// a path that only starts like php must not match either tier
	if hit(wide, "GET /phpinfo-guide HTTP/1.1") || hit(wide, "GET /page.phpx HTTP/1.1") {
		t.Error("PHP tier matched something that is not a php path")
	}
}

// mapHits runs the map patterns on a path the way nginx does: case insensitive (~*), on $uri.
func mapHits(php bool, uri string) bool {
	ps := append([]string{}, probeCore...)
	if php {
		ps = append(ps, probePHP...)
	}
	for _, p := range ps {
		if regexp.MustCompile("(?i)" + p).MatchString(uri) {
			return true
		}
	}
	return false
}

func TestMapPatternsMatchURIs(t *testing.T) {
	for _, u := range []string{"/.env", "/.ENV", "/.Git/config", "/.env_prod", "/.git/config", "/.git_bak/config", "/.aws/credentials", "/..../..../proc/self/environ", "/vendor/phpunit/x", "/etc/passwd", "/a/etc/passwd"} {
		if !mapHits(false, u) {
			t.Errorf("map missed %s", u)
		}
	}
	for _, u := range []string{
		"/.well-known/acme-challenge/abc", "/_next/static/x.js", "/_next/static/chunks/x.js", "/api/content", "/", "/about",
		"/wp-admin/install.php", "/index.php", "/blog/etc/passwd-guide", "/vendor/phpunit-docs", "/.github/x",
	} {
		if mapHits(false, u) {
			t.Errorf("core map matched %s", u)
		}
	}
	for _, u := range []string{"/wp-admin/install.php", "/wp-login.php", "/xmlrpc.php", "/a/b.php", "/a/b.php/c"} {
		if !mapHits(true, u) {
			t.Errorf("PHP tier map missed %s", u)
		}
	}
	for _, u := range []string{"/.well-known/acme-challenge/abc", "/_next/static/x.js", "/api/content", "/assets/foo.php.min.js", "/api/x.php-looking", "/wp-admin-guide"} {
		if mapHits(true, u) {
			t.Errorf("PHP tier map matched %s", u)
		}
	}
}

// The map and the filter are both case insensitive, so the two agree on what a probe is.
func TestMapAndFilterAgree(t *testing.T) {
	for _, php := range []bool{false, true} {
		r := filterRegexp(t, php)
		for _, u := range []string{
			"/.env", "/.ENV", "/.env_prod", "/.git/config", "/.github/x", "/.well-known/acme-challenge/a", "/_next/static/x.js", "/",
			"/etc/passwd", "/blog/etc/passwd-guide", "/vendor/phpunit/x", "/vendor/phpunit-docs", "/a.php", "/a.php.min.js", "/wp-login.php",
			"/wp-admin-guide", "/xmlrpc.php", "/proc/self/environ", "/x/jndi:y",
		} {
			if got, want := mapHits(php, u), hit(r, "GET "+u+" HTTP/1.1"); got != want {
				t.Errorf("php=%v %s: map=%v filter=%v", php, u, got, want)
			}
		}
	}
}

func TestPatternsAreSafeInShellAndNginxQuoting(t *testing.T) {
	for _, p := range append(append([]string{}, probeCore...), probePHP...) {
		if strings.ContainsAny(p, "'\";{} \t") {
			t.Errorf("pattern %q has a character that breaks shell or nginx quoting", p)
		}
		if _, err := regexp.Compile(p); err != nil {
			t.Errorf("pattern %q: %v", p, err)
		}
	}
}

// ---- running the section under bash with stubs --------------------------------------

// probeEnv runs nginxProbesAt for real: bash, stubs for sudo, nginx, systemctl and
// fail2ban-client on PATH, and every file under a temporary root.
type probeEnv struct {
	t     *testing.T
	bash  string
	root  string
	bin   string
	tmp   string
	log   string
	files []string
}

func newProbeEnv(t *testing.T) *probeEnv {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	e := &probeEnv{t: t, bash: bash, root: t.TempDir(), bin: t.TempDir(), tmp: t.TempDir()}
	e.log = filepath.Join(e.tmp, "calls.log")
	stub := func(name, body string) {
		if err := os.WriteFile(filepath.Join(e.bin, name), []byte("#!/bin/bash\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// sudo runs the command, and drops -o and -g from install because the tests are not root
	stub("sudo", `[ "$1" = "-n" ] && shift
if [ "$1" = "install" ]; then
  shift
  args=()
  while [ $# -gt 0 ]; do
    case "$1" in -o|-g) shift 2 ;; *) args+=("$1"); shift ;; esac
  done
  exec install "${args[@]}"
fi
exec "$@"
`)
	stub("nginx", `case "$1" in
  -T)
    IFS=: read -ra fs <<< "$STUB_NGINX_FILES"
    for f in "${fs[@]}"; do echo "# configuration file $f:"; cat "$f"; done ;;
  -t) if [ -n "${STUB_NGINX_TEST_FAIL:-}" ]; then echo "$STUB_NGINX_TEST_FAIL" >&2; exit 1; fi ;;
  -s) if [ -n "${STUB_NGINX_RELOAD_FAIL:-}" ]; then exit 1; fi; echo "nginx -s $2" >> "$STUB_LOG" ;;
esac
exit 0
`)
	stub("systemctl", `echo "systemctl $*" >> "$STUB_LOG"
case "$1 $2" in
  "reload nginx"|"restart nginx") [[ ",${STUB_RELOAD_FAIL:-}," == *",nginx,"* ]] && exit 1 ;;
  "reload fail2ban"|"restart fail2ban") [[ ",${STUB_RELOAD_FAIL:-}," == *",fail2ban,"* ]] && exit 1 ;;
esac
exit 0
`)
	stub("fail2ban-client", `if [ -n "${STUB_F2B_FAIL:-}" ]; then exit 1; fi
exit 0
`)
	return e
}

func (e *probeEnv) path(rel string) string { return filepath.Join(e.root, rel) }

// write puts a file under the root. An nginx file is also listed in the stub's nginx -T.
func (e *probeEnv) write(rel, content string, listed bool) string {
	e.t.Helper()
	p := e.path(rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		e.t.Fatal(err)
	}
	if listed {
		e.files = append(e.files, p)
	}
	return p
}

func (e *probeEnv) read(abs string) string {
	b, err := os.ReadFile(abs)
	if err != nil {
		return "<missing>"
	}
	return string(b)
}

func (e *probeEnv) calls() string { return e.read(e.log) }

// conf is a one server nginx config. extra goes inside the server block.
func (e *probeEnv) conf(realip bool, extra string) string {
	c := "http {\n    access_log " + e.root + "/var/log/nginx/access.log;\n"
	if realip {
		c += "    set_real_ip_from 10.0.0.0/8;\n"
	}
	return c + "    server {\n        listen 80;\n" + extra + "    }\n}\n"
}

func (e *probeEnv) run(o Options, dry bool, env ...string) (string, int) {
	e.t.Helper()
	d := "0"
	if dry {
		d = "1"
	}
	script := "set -u\nDRY=" + d + "\nsay() { echo \"CHANGE|$1|$2|$3\"; }\n" + nginxProbesAt(o, e.root) + "\necho DONE\n"
	cmd := exec.Command(e.bash, "-c", script)
	cmd.Env = append([]string{
		"PATH=" + e.bin + ":/usr/bin:/bin", "TMPDIR=" + e.tmp, "HOME=" + e.tmp,
		"STUB_LOG=" + e.log, "STUB_NGINX_FILES=" + strings.Join(e.files, ":"),
	}, env...)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			e.t.Fatal(err)
		}
		code = ee.ExitCode()
	}
	return string(out), code
}

func TestPHPTierDecision(t *testing.T) {
	fcgi := "        location ~ x { fastcgi_pass 127.0.0.1:9000; }\n"
	for _, c := range []struct {
		name       string
		noPHP      bool
		fastcgi    bool
		wantPHP    bool
		wantDetail string
	}{
		{"flag absent", false, false, false, "the PHP and WordPress list is off; pass --nginx-no-php"},
		{"flag absent, fastcgi", false, true, false, "the PHP and WordPress list is off; pass --nginx-no-php"},
		{"flag and no fastcgi", true, false, true, "core list plus PHP and WordPress paths"},
		{"flag but fastcgi", true, true, false, "nginx has fastcgi_pass"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newProbeEnv(t)
			extra := ""
			if c.fastcgi {
				extra = fcgi
			}
			e.write("etc/nginx/nginx.conf", e.conf(true, extra), true)
			out, code := e.run(Options{NginxBanProbes: true, NginxDropProbes: true, NginxNoPHP: c.noPHP}, false)
			if code != 0 {
				t.Fatalf("exit %d: %s", code, out)
			}
			filter := e.read(e.path(probeFilterFile))
			mapf := e.read(e.path(probeMapFile))
			if got := strings.Contains(filter, `\.php`) && strings.Contains(filter, "/wp-"); got != c.wantPHP {
				t.Errorf("filter has the PHP tier = %v, want %v:\n%s", got, c.wantPHP, filter)
			}
			if got := strings.Contains(mapf, `"~*\.php`) && strings.Contains(mapf, "/wp-"); got != c.wantPHP {
				t.Errorf("map has the PHP tier = %v, want %v:\n%s", got, c.wantPHP, mapf)
			}
			if !strings.Contains(out, c.wantDetail) {
				t.Errorf("detail %q missing from:\n%s", c.wantDetail, out)
			}
			// the core list is always there
			if !strings.Contains(filter, `/\.(?:env|git`) || !strings.Contains(mapf, `"~*/\.(?:env|git`) {
				t.Error("the core list is missing")
			}
			if c.noPHP && c.fastcgi && !strings.Contains(out, "CHANGE|nginx-php|report|") {
				t.Errorf("fastcgi_pass under --nginx-no-php was not reported:\n%s", out)
			}
		})
	}
}

func TestProbeJailWritesAndReloads(t *testing.T) {
	e := newProbeEnv(t)
	e.write("etc/nginx/nginx.conf", e.conf(true, ""), true)
	out, code := e.run(Options{NginxBanProbes: true}, false)
	if code != 0 || !strings.Contains(out, "CHANGE|nginx-probes|applied|") {
		t.Fatalf("exit %d: %s", code, out)
	}
	jail := e.read(e.path(probeJailFile))
	for _, w := range []string{"[baryovm-nginx-probes]", "backend = auto", "maxretry = 5", "logpath = " + e.root + "/var/log/nginx/access.log", probeIgnoreIP} {
		if !strings.Contains(jail, w) {
			t.Errorf("jail is missing %q:\n%s", w, jail)
		}
	}
	if strings.Contains(jail, "[DEFAULT]") {
		t.Error("the jail has a DEFAULT section")
	}
	if !strings.Contains(e.calls(), "systemctl reload fail2ban") {
		t.Errorf("fail2ban was not reloaded:\n%s", e.calls())
	}
	// a second run with nothing changed
	out, code = e.run(Options{NginxBanProbes: true}, false)
	if code != 0 || !strings.Contains(out, "CHANGE|nginx-probes|already-correct|") || strings.Contains(out, "|applied|") {
		t.Errorf("second run, exit %d: %s", code, out)
	}
}

func TestProbeJailRestoresWhenFail2banRejects(t *testing.T) {
	t.Run("previous files come back", func(t *testing.T) {
		e := newProbeEnv(t)
		e.write("etc/nginx/nginx.conf", e.conf(true, ""), true)
		e.write("etc/fail2ban/filter.d/baryovm-nginx-probes.conf", "OLD FILTER\n", false)
		e.write("etc/fail2ban/jail.d/baryovm-nginx-probes.local", "OLD JAIL\n", false)
		out, code := e.run(Options{NginxBanProbes: true}, false, "STUB_F2B_FAIL=1")
		if code != 1 || !strings.Contains(out, "ERROR|fail2ban rejected the scanner jail") || strings.Contains(out, "DONE") {
			t.Errorf("exit %d: %s", code, out)
		}
		if got := e.read(e.path(probeFilterFile)); got != "OLD FILTER\n" {
			t.Errorf("filter = %q", got)
		}
		if got := e.read(e.path(probeJailFile)); got != "OLD JAIL\n" {
			t.Errorf("jail = %q", got)
		}
		if strings.Contains(e.calls(), "reload fail2ban") {
			t.Error("fail2ban was reloaded after it rejected the jail")
		}
	})
	t.Run("new files are removed", func(t *testing.T) {
		e := newProbeEnv(t)
		e.write("etc/nginx/nginx.conf", e.conf(true, ""), true)
		_, code := e.run(Options{NginxBanProbes: true}, false, "STUB_F2B_FAIL=1")
		if code != 1 {
			t.Errorf("exit %d", code)
		}
		for _, f := range []string{probeFilterFile, probeJailFile} {
			if _, err := os.Stat(e.path(f)); err == nil {
				t.Errorf("%s was left behind", f)
			}
		}
	})
}

func TestProbeJailReloadFailureIsAnError(t *testing.T) {
	e := newProbeEnv(t)
	e.write("etc/nginx/nginx.conf", e.conf(true, ""), true)
	out, code := e.run(Options{NginxBanProbes: true}, false, "STUB_RELOAD_FAIL=fail2ban")
	if code != 1 || !strings.Contains(out, "ERROR|") || !strings.Contains(out, "could not be reloaded") {
		t.Errorf("exit %d: %s", code, out)
	}
	if strings.Contains(out, "|applied|") {
		t.Errorf("a failed reload was reported as applied:\n%s", out)
	}
}

func TestProbeJailIsOptInWhenRun(t *testing.T) {
	e := newProbeEnv(t)
	e.write("etc/nginx/nginx.conf", e.conf(true, ""), true)
	out, code := e.run(Options{}, false)
	if code != 0 || !strings.Contains(out, "CHANGE|nginx-probes|report|a fail2ban jail for nginx scanners is available") {
		t.Fatalf("exit %d: %s", code, out)
	}
	if _, err := os.Stat(e.path(probeJailFile)); err == nil {
		t.Error("a jail was written without --nginx-ban-probes")
	}
	// a jail an earlier run wrote stays, and is reported
	e.write("etc/fail2ban/jail.d/baryovm-nginx-probes.local", "MINE\n", false)
	out, _ = e.run(Options{}, false)
	if !strings.Contains(out, "left as it is") || e.read(e.path(probeJailFile)) != "MINE\n" {
		t.Errorf("an existing jail was not left alone:\n%s", out)
	}
}

func TestProbeJailReportsMissingRealIP(t *testing.T) {
	e := newProbeEnv(t)
	e.write("etc/nginx/nginx.conf", e.conf(false, ""), true)
	out, code := e.run(Options{NginxBanProbes: true}, false)
	if code != 0 || !strings.Contains(out, "CHANGE|nginx-realip|report|") || !strings.Contains(out, "Configure set_real_ip_from first") {
		t.Errorf("exit %d: %s", code, out)
	}
	if !strings.Contains(out, "|applied|") {
		t.Error("the jail was not still written")
	}
	e2 := newProbeEnv(t)
	e2.write("etc/nginx/nginx.conf", e2.conf(true, ""), true)
	if out, _ := e2.run(Options{NginxBanProbes: true}, false); strings.Contains(out, "nginx-realip") {
		t.Errorf("realip reported although set_real_ip_from is set:\n%s", out)
	}
}

func TestProbesDryRunWritesNothingWhenRun(t *testing.T) {
	e := newProbeEnv(t)
	conf := e.conf(true, "")
	cp := e.write("etc/nginx/nginx.conf", conf, true)
	out, code := e.run(Options{NginxBanProbes: true, NginxDropProbes: true}, true)
	if code != 0 || !strings.Contains(out, "CHANGE|nginx-probes|would-apply|") || !strings.Contains(out, "CHANGE|nginx-drop|would-apply|") {
		t.Fatalf("exit %d: %s", code, out)
	}
	if e.read(cp) != conf {
		t.Error("a dry run edited the nginx config")
	}
	for _, f := range []string{probeFilterFile, probeJailFile, probeMapFile, probeSnippet} {
		if _, err := os.Stat(e.path(f)); err == nil {
			t.Errorf("a dry run wrote %s", f)
		}
	}
}

func TestDropAppliesThenReportsAlreadyCorrect(t *testing.T) {
	e := newProbeEnv(t)
	cp := e.write("etc/nginx/nginx.conf", e.conf(true, ""), true)
	o := Options{NginxBanProbes: true, NginxDropProbes: true}
	out, code := e.run(o, false)
	if code != 0 || !strings.Contains(out, "CHANGE|nginx-drop|applied|") {
		t.Fatalf("exit %d: %s", code, out)
	}
	inc := "include " + e.path(probeSnippet) + ";"
	if !strings.Contains(e.read(cp), "    server {\n        "+inc+"\n        listen 80;") {
		t.Errorf("include not added right after server {:\n%s", e.read(cp))
	}
	if snip := e.read(e.path(probeSnippet)); !strings.Contains(snip, "return 444;") || !strings.Contains(snip, "if ($baryovm_probe)") {
		t.Errorf("snippet = %q", snip)
	}
	if !strings.Contains(e.calls(), "systemctl reload nginx") {
		t.Errorf("nginx was not reloaded:\n%s", e.calls())
	}
	after := e.read(cp)
	out, code = e.run(o, false)
	if code != 0 || !strings.Contains(out, "CHANGE|nginx-drop|already-correct|") || strings.Contains(out, "|applied|") {
		t.Errorf("second run, exit %d: %s", code, out)
	}
	if e.read(cp) != after {
		t.Error("the second run changed the config again")
	}
}

func TestDropWithoutFlagReportsTheCount(t *testing.T) {
	e := newProbeEnv(t)
	conf := e.conf(true, "")
	cp := e.write("etc/nginx/nginx.conf", conf, true)
	out, code := e.run(Options{}, false)
	if code != 0 || !strings.Contains(out, "CHANGE|nginx-drop|report|1 server block(s) lack") || !strings.Contains(out, "--nginx-drop-probes") {
		t.Fatalf("exit %d: %s", code, out)
	}
	if e.read(cp) != conf {
		t.Error("the config was edited without --nginx-drop-probes")
	}
	if _, err := os.Stat(e.path(probeMapFile)); err == nil {
		t.Error("a map was written without --nginx-drop-probes")
	}
}

func TestDropRestoresEveryFileAndNamesWhereNginxComplained(t *testing.T) {
	e := newProbeEnv(t)
	conf := e.conf(true, "")
	cp := e.write("etc/nginx/nginx.conf", conf, true)
	other := e.write("etc/nginx/conf.d/site.conf", "server {\n    listen 81;\n}\n", true)
	oldMap := e.write("etc/nginx/conf.d/00-baryovm-probes.conf", "OLD MAP\n", false)
	complaint := `nginx: [emerg] "if" directive is not allowed here in ` + other + `:2`
	out, code := e.run(Options{NginxDropProbes: true}, false, "STUB_NGINX_TEST_FAIL="+complaint)
	if code != 1 || !strings.Contains(out, "ERROR|nginx -t rejected the drop step at "+other+":2") || strings.Contains(out, "|applied|") {
		t.Errorf("exit %d: %s", code, out)
	}
	if e.read(cp) != conf || e.read(other) != "server {\n    listen 81;\n}\n" {
		t.Error("a touched config file was not restored")
	}
	if e.read(oldMap) != "OLD MAP\n" {
		t.Errorf("the previous map was not restored: %q", e.read(oldMap))
	}
	if _, err := os.Stat(e.path(probeSnippet)); err == nil {
		t.Error("the snippet this run created was left behind")
	}
	if strings.Contains(e.calls(), "reload nginx") {
		t.Error("nginx was reloaded after nginx -t failed")
	}
}

func TestDropReloadFailureIsAnError(t *testing.T) {
	e := newProbeEnv(t)
	e.write("etc/nginx/nginx.conf", e.conf(true, ""), true)
	out, code := e.run(Options{NginxDropProbes: true}, false, "STUB_RELOAD_FAIL=nginx", "STUB_NGINX_RELOAD_FAIL=1")
	if code != 1 || !strings.Contains(out, "ERROR|") || !strings.Contains(out, "nginx could not be reloaded") || strings.Contains(out, "|applied|") {
		t.Errorf("exit %d: %s", code, out)
	}
}

func TestDropReportsShapesItDoesNotEditAndNeverSaysAlreadyCorrect(t *testing.T) {
	e := newProbeEnv(t)
	conf := "http {\n    access_log " + e.root + "/var/log/nginx/access.log;\n    set_real_ip_from 10.0.0.0/8;\n    server { listen 80; }\n}\n"
	cp := e.write("etc/nginx/nginx.conf", conf, true)
	for _, o := range []Options{{}, {NginxDropProbes: true}, {NginxDropProbes: true}} {
		out, code := e.run(o, false)
		if code != 0 {
			t.Fatalf("exit %d: %s", code, out)
		}
		if !strings.Contains(out, "CHANGE|nginx-drop|report|1 server block(s) in a shape this step does not edit") || !strings.Contains(out, cp+":4") {
			t.Errorf("odd shape not reported with file and line:\n%s", out)
		}
		if strings.Contains(out, "nginx-drop|already-correct") {
			t.Errorf("already-correct with an unedited server block:\n%s", out)
		}
	}
	if e.read(cp) != conf {
		t.Error("a one-line server block was edited")
	}
}

func TestProbesRunLastAndFailLoudly(t *testing.T) {
	s := script(Options{NginxBanProbes: true, NginxDropProbes: true})
	fact := strings.Index(s, `echo "FACT|fail2ban|active"`)
	probes := strings.Index(s, `PROBE_ERR=""`)
	exit := strings.LastIndex(s, `echo "ERROR|$PROBE_ERR"`)
	if fact < 0 || probes < fact || exit < probes {
		t.Errorf("order wrong: fact=%d probes=%d error=%d", fact, probes, exit)
	}
	if !strings.HasSuffix(strings.TrimSpace(s), "fi") || !strings.Contains(s[exit:], "exit 1") {
		t.Error("the probes section does not end by failing on a collected error")
	}
	if strings.Contains(s, "say nginx-probes "+StateReport+` "fail2ban rejected`) || strings.Contains(s, "say nginx-drop "+StateReport+` "nginx -t rejected`) {
		t.Error("a rejected jail or drop is reported as a report item, not an error")
	}
}

func TestNoPHPFlagIsBakedIntoTheScript(t *testing.T) {
	mustContain(t, script(Options{}), "NOPHP=0", "--nginx-no-php")
	mustContain(t, script(Options{NginxNoPHP: true}), "NOPHP=1", `grep -qE '^[[:space:]]*fastcgi_pass[[:space:]]'`, "say nginx-php "+StateReport)
}

func TestProbeJailIsItsOwnFileAndLeavesJailLocalAlone(t *testing.T) {
	s := probeJailSection(Options{NginxBanProbes: true}, "")
	mustContain(t, s, "/etc/fail2ban/jail.d/baryovm-nginx-probes.local", "/etc/fail2ban/filter.d/baryovm-nginx-probes.conf",
		"backend = auto", "logpath = $lp", "[baryovm-nginx-probes]", "fail2ban-client -t", "sudo cmp -s")
	if strings.Contains(s, "[DEFAULT]") || strings.Contains(s, "[sshd]") {
		t.Error("the probe jail writes a DEFAULT or sshd section")
	}
	if strings.Contains(s, `/etc/fail2ban/jail.local`) {
		t.Error("the probe jail touches jail.local")
	}
	if strings.Contains(s, "recidive") {
		t.Error("a recidive jail was added, bantime.increment already escalates")
	}
}

func TestProbeJailIsOptIn(t *testing.T) {
	off := script(Options{})
	for _, w := range []string{"[baryovm-nginx-probes]", `sudo install -m 644 -o root -g root "$NEWJAIL"`, `"maxretry = 5"`, "say nginx-probes applied", "say nginx-probes would-apply"} {
		if strings.Contains(off, w) {
			t.Errorf("without --nginx-ban-probes the script has %q", w)
		}
	}
	mustContain(t, off, "say nginx-probes "+StateReport, "--nginx-ban-probes", "is available", `sudo test -f "$PJAIL" || sudo test -f "$PFILTER"`, "left as it is")
	if strings.Contains(off, `sudo rm -f "$PJAIL"`) || strings.Contains(off, `sudo rm -f "$PFILTER"`) {
		t.Error("without the flag the script can delete an existing jail file")
	}
	mustContain(t, script(Options{NginxBanProbes: true}), "[baryovm-nginx-probes]", "say nginx-probes applied")
}

func TestProbeJailIgnoreIPKeepsDefaultsAndPrivateRanges(t *testing.T) {
	s := script(Options{NginxBanProbes: true})
	mustContain(t, s, "ignoreip = %(known/ignoreip)s 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 100.64.0.0/10 fc00::/7", `echo "maxretry = 5"`)
}

func TestDropFlagAddsTheDropSection(t *testing.T) {
	without := script(Options{})
	with := script(Options{NginxDropProbes: true})
	for _, needle := range []string{"return 444;", `map $uri $baryovm_probe`, "say nginx-drop applied", `sudo tee "$f" < "$EDITED"`} {
		if strings.Contains(without, needle) {
			t.Errorf("without the flag the script has %q", needle)
		}
		if !strings.Contains(with, needle) {
			t.Errorf("with the flag the script is missing %q", needle)
		}
	}
	mustContain(t, with, "/etc/nginx/conf.d/00-baryovm-probes.conf", "/etc/nginx/baryovm/drop-probes.conf", "say nginx-drop already-correct", "say nginx-drop would-apply", `"~*%s"`)
	if !strings.HasPrefix(probeFailPrefix, "(?i)") {
		t.Error("the map is case insensitive, so the filter has to be too")
	}
	mustContain(t, without, "say nginx-drop "+StateReport, "lack $PINC", "--nginx-drop-probes", "say nginx-drop already-correct", "mode=count")
	if strings.Contains(without, "mode=edit") {
		t.Error("without the flag the script edits server blocks")
	}
}

func TestDropSnippetAndMapShape(t *testing.T) {
	s := script(Options{NginxDropProbes: true})
	mustContain(t, s, `echo 'if ($baryovm_probe) {'`, `echo '    return 444;'`, `echo 'map $uri $baryovm_probe {'`, `echo '    default 0;'`)
	if strings.Count(s, "return 444") != 1 {
		t.Errorf("return 444 appears %d times, want only the snippet", strings.Count(s, "return 444"))
	}
}

func TestLogpathAwk(t *testing.T) {
	if _, err := exec.LookPath("awk"); err != nil {
		t.Skip("no awk")
	}
	in := `# configuration file /etc/nginx/nginx.conf:
http {
    access_log  /var/log/nginx/access.log main;
    server {
        access_log  logs/access.log;
    }
    server {
        access_log off;
    }
    server {
        access_log /var/log/nginx/example.com.access.log;
        access_log syslog:server=203.0.113.1 main;
        access_log /var/log/nginx/$host.log;
        # access_log /var/log/nginx/commented.log;
    }
}
`
	cmd := exec.Command("awk", logpathAwk)
	cmd.Stdin = strings.NewReader(in)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Fields(string(out))
	want := []string{"/var/log/nginx/access.log", "/var/log/nginx/example.com.access.log"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("logpaths = %q, want %q", got, want)
	}
}

func runIncludeAwk(t *testing.T, mode, input string) string {
	t.Helper()
	if _, err := exec.LookPath("awk"); err != nil {
		t.Skip("no awk")
	}
	cmd := exec.Command("awk", "-v", "mode="+mode, "-v", "inc=include "+probeSnippet+";", includeAwk)
	cmd.Stdin = strings.NewReader(input)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestIncludeAwkAddsOnceAndSkipsStream(t *testing.T) {
	in := `http {
    upstream app {
        server 127.0.0.1:3000;
    }
    server {
        listen 80;
    }
    server {
        include /etc/nginx/baryovm/drop-probes.conf;
        listen 443;
    }
}
stream {
    server {
        listen 53;
    }
}
`
	if got := strings.TrimSpace(runIncludeAwk(t, "count", in)); got != "1" {
		t.Errorf("count = %q, want 1 (one block lacks the include, upstream server and stream are skipped)", got)
	}
	edited := runIncludeAwk(t, "edit", in)
	want := "    server {\n        include /etc/nginx/baryovm/drop-probes.conf;\n        listen 80;\n"
	if !strings.Contains(edited, want) {
		t.Errorf("include was not added right after the server line:\n%s", edited)
	}
	if n := strings.Count(edited, "drop-probes.conf;"); n != 2 {
		t.Errorf("include appears %d times, want 2:\n%s", n, edited)
	}
	if strings.Contains(edited, "stream {\n    server {\n        include") {
		t.Error("a stream server block was edited")
	}
	// a second pass over the edited text finds nothing to do
	if got := strings.TrimSpace(runIncludeAwk(t, "count", edited)); got != "0" {
		t.Errorf("second count = %q, want 0", got)
	}
	if again := runIncludeAwk(t, "edit", edited); again != edited {
		t.Error("editing already edited text changed it")
	}
}

func TestIncludeAwkSkipsMailAndReportsShapesItDoesNotEdit(t *testing.T) {
	in := "http {\n" + // 1
		"    server { listen 80; }\n" + // 2 one line
		"    server\n" + // 3 brace on the next line
		"    {\n" + // 4
		"        listen 81;\n" + // 5
		"    }\n" + // 6
		"    server {\r\n" + // 7 CRLF
		"        listen 82;\r\n" + // 8
		"    }\r\n" + // 9
		"}\n" + // 10
		"mail {\n" + // 11
		"    server {\n" + // 12 not an http server
		"        listen 25;\n" + // 13
		"    }\n" + // 14
		"}\n" // 15
	if got := runIncludeAwk(t, "count", in); got != "0\nodd:2\nodd:3\nodd:7\n" {
		t.Errorf("count output = %q", got)
	}
	if got := runIncludeAwk(t, "edit", in); got != in {
		t.Errorf("edit changed a shape it must leave alone:\n%s", got)
	}
}
