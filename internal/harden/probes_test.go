// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package harden

import (
	"os/exec"
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

func TestFilterMatchesScannerLines(t *testing.T) {
	r := filterRegexp(t, false)
	for _, req := range []string{
		"GET /.env HTTP/1.1",
		"GET /.env.local HTTP/1.1",
		"GET /.git/config HTTP/1.1",
		"GET /.aws/credentials HTTP/1.1",
		"GET /app/.ssh/id_rsa HTTP/1.1",
		"GET /.svn/entries HTTP/1.1",
		"GET /.hg/store HTTP/1.1",
		"GET /.htpasswd HTTP/1.1",
		"GET /.npmrc HTTP/1.1",
		"GET /.DS_Store HTTP/1.1",
		"GET /vendor/phpunit/phpunit/src/Util/PHP/eval-stdin.php HTTP/1.1",
		"GET /?path=php://filter/read=convert.base64-encode/resource=/etc/passwd HTTP/1.1",
		"GET /....//....//....//..../proc/self/environ HTTP/1.1",
		"GET /download?file=../../etc/passwd HTTP/1.1",
		"GET /?x=${jndi:ldap://203.0.113.5/a} HTTP/1.1",
		"POST /.env HTTP/1.1",
	} {
		m := r.FindStringSubmatch(logLine(req))
		if m == nil {
			t.Errorf("scanner line was not matched: %s", req)
			continue
		}
		if got := m[r.SubexpIndex("host")]; got != "203.0.113.9" {
			t.Errorf("host for %q = %q", req, got)
		}
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
		} {
			if r.MatchString(logLine(req)) {
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
		if narrow.MatchString(logLine(req)) {
			t.Errorf("narrow filter matched a PHP path: %s", req)
		}
		if !wide.MatchString(logLine(req)) {
			t.Errorf("PHP tier did not match: %s", req)
		}
	}
	// a path that only starts like php must not match either tier
	if wide.MatchString(logLine("GET /phpinfo-guide HTTP/1.1")) || wide.MatchString(logLine("GET /page.phpx HTTP/1.1")) {
		t.Error("PHP tier matched something that is not a php path")
	}
}

func TestMapPatternsMatchURIs(t *testing.T) {
	compile := func(php bool) []*regexp.Regexp {
		ps := append([]string{}, probeCore...)
		if php {
			ps = append(ps, probePHP...)
		}
		var out []*regexp.Regexp
		for _, p := range ps {
			out = append(out, regexp.MustCompile("(?i)"+p))
		}
		return out
	}
	hit := func(rs []*regexp.Regexp, uri string) bool {
		for _, r := range rs {
			if r.MatchString(uri) {
				return true
			}
		}
		return false
	}
	core, wide := compile(false), compile(true)
	for _, u := range []string{"/.env", "/.git/config", "/.aws/credentials", "/..../..../proc/self/environ", "/vendor/phpunit/x", "/etc/passwd", "/.ENV"} {
		if !hit(core, u) {
			t.Errorf("map missed %s", u)
		}
	}
	for _, u := range []string{"/.well-known/acme-challenge/abc", "/_next/static/x.js", "/_next/static/chunks/x.js", "/api/content", "/", "/about", "/wp-admin/install.php", "/index.php"} {
		if hit(core, u) {
			t.Errorf("core map matched %s", u)
		}
	}
	for _, u := range []string{"/wp-admin/install.php", "/wp-login.php", "/xmlrpc.php", "/a/b.php"} {
		if !hit(wide, u) {
			t.Errorf("PHP tier map missed %s", u)
		}
	}
	for _, u := range []string{"/.well-known/acme-challenge/abc", "/_next/static/x.js", "/api/content"} {
		if hit(wide, u) {
			t.Errorf("PHP tier map matched %s", u)
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

// The PHP tier is decided from nginx -T. Its patterns may appear in the script only
// inside the branch that runs when fastcgi_pass is absent.
func TestPHPTierOnlyWithoutFastcgiPass(t *testing.T) {
	endsBlock := regexp.MustCompile(`\n\s*fi\n`)
	for _, drop := range []bool{false, true} {
		s := script(Options{NginxDropProbes: drop})
		mustContain(t, s, `grep -qE '^[[:space:]]*fastcgi_pass[[:space:]]'`, "PHPTIER=0", "PHPTIER=1",
			"core list only (nginx has fastcgi_pass", "say nginx-php "+StateReport)
		// PHPTIER is raised only in the else branch of the fastcgi_pass test
		fc := strings.Index(s, "fastcgi_pass[[:space:]]'")
		on := strings.Index(s, "PHPTIER=1")
		els := strings.Index(s[fc:], "else")
		if fc < 0 || on < 0 || els < 0 || on < fc+els {
			t.Errorf("PHPTIER=1 is not set in the no-fastcgi branch (fc=%d on=%d else=%d)", fc, on, els)
		}
		// each PHP pattern sits only inside an if [ "$PHPTIER" = "1" ] block
		for _, p := range probePHP {
			found := false
			for at := 0; ; {
				i := strings.Index(s[at:], p)
				if i < 0 {
					break
				}
				i += at
				found = true
				guard := strings.LastIndex(s[:i], `if [ "$PHPTIER" = "1" ]; then`)
				if guard < 0 || endsBlock.MatchString(s[guard:i]) {
					t.Errorf("PHP pattern %q appears outside the PHPTIER guard (drop=%v)", p, drop)
				}
				at = i + len(p)
			}
			if !found {
				t.Errorf("PHP pattern %q is not in the script at all (drop=%v)", p, drop)
			}
		}
	}
}

func TestProbeJailIsItsOwnFileAndLeavesJailLocalAlone(t *testing.T) {
	s := probeJailSection(Options{})
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
	// the jail is written whenever nginx and fail2ban are there, flag or not
	mustContain(t, script(Options{}), "say nginx-probes applied", "command -v nginx", "command -v fail2ban-client")
}

func TestProbeJailRestoresAndReloadsOnlyOnSuccess(t *testing.T) {
	s := probeJailSection(Options{})
	test := strings.Index(s, "fail2ban-client -t")
	reload := strings.Index(s, "systemctl reload fail2ban")
	restore := strings.Index(s, `sudo rm -f "$PFILTER"`)
	rejected := strings.Index(s, "fail2ban rejected the scanner jail")
	if test < 0 || reload < 0 || restore < 0 || rejected < 0 {
		t.Fatalf("missing piece: test=%d reload=%d restore=%d rejected=%d", test, reload, restore, rejected)
	}
	if !(test < reload && reload < restore) {
		t.Errorf("order wrong: test=%d reload=%d restore=%d", test, reload, restore)
	}
	mustContain(t, s, `install -m 644 -o root -g root "$PREVFILT" "$PFILTER"`, `install -m 644 -o root -g root "$PREVJAIL2" "$PJAIL"`)
}

func TestProbesDryRunWritesNothing(t *testing.T) {
	s := script(Options{DryRun: true, NginxDropProbes: true})
	mustContain(t, s, "DRY=1", "say nginx-probes would-apply", "say nginx-drop would-apply")
	// every write sits after the dry-run branch, in the else that follows it
	for _, w := range []string{`sudo install -m 644 -o root -g root "$NEWFILT"`, `sudo install -m 644 -o root -g root "$NEWMAP"`, `sudo tee "$f" < "$EDITED"`} {
		at := strings.Index(s, w)
		if at < 0 {
			t.Errorf("write %q not found", w)
			continue
		}
		dry := strings.LastIndex(s[:at], `elif [ "$DRY" = "1" ]; then`)
		if dry < 0 || !strings.Contains(s[dry:at], "\nelse\n") && !strings.Contains(s[dry:at], "else\n") {
			t.Errorf("write %q is not behind the dry-run branch", w)
		}
	}
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
	mustContain(t, with, "/etc/nginx/conf.d/00-baryovm-probes.conf", "/etc/nginx/baryovm/drop-probes.conf", "say nginx-drop already-correct", "say nginx-drop would-apply")
	// without the flag it counts and reports
	mustContain(t, without, "say nginx-drop "+StateReport, "lack $PINC", "--nginx-drop-probes", "say nginx-drop already-correct", "mode=count")
	if strings.Contains(without, "mode=edit") {
		t.Error("without the flag the script edits server blocks")
	}
}

func TestDropRestoresEveryFileWhenNginxRejects(t *testing.T) {
	s := script(Options{NginxDropProbes: true})
	test := strings.Index(s, "if sudo nginx -t >/dev/null 2>&1; then\n        sudo systemctl reload nginx")
	reload := strings.Index(s[test:], "sudo systemctl reload nginx") + test
	restoreFiles := strings.Index(s, `sudo tee "$f" < "$BK/file.`)
	restoreMap := strings.Index(s, `sudo rm -f "$PMAP"`)
	restoreSnip := strings.Index(s, `sudo rm -f "$PSNIP"`)
	if test < 0 || restoreFiles < 0 || restoreMap < 0 || restoreSnip < 0 || reload < 0 {
		t.Fatalf("missing piece: test=%d files=%d map=%d snip=%d reload=%d", test, restoreFiles, restoreMap, restoreSnip, reload)
	}
	if !(reload > test && restoreFiles > reload && restoreMap > reload && restoreSnip > reload) {
		t.Error("restore must come after the reload that only runs on success")
	}
	// every touched file is backed up before anything is written
	backup := strings.Index(s, `sudo cat "$f" > "$BK/file.`)
	write := strings.Index(s, `sudo install -m 644 -o root -g root "$NEWMAP"`)
	if backup < 0 || write < 0 || backup > write {
		t.Errorf("backup (%d) must come before the first write (%d)", backup, write)
	}
	mustContain(t, s, "say nginx-drop "+StateReport+` "nginx -t rejected the drop step`, "every file was restored", "nginx was not reloaded")
}

func TestSecondRunReportsAlreadyCorrect(t *testing.T) {
	s := script(Options{NginxDropProbes: true})
	// the comparison against what is on disk comes before any write
	cmp := strings.Index(s, `[ "$LACK" = "0" ] && sudo test -f "$PMAP" && sudo cmp -s "$NEWMAP" "$PMAP"`)
	ok := strings.Index(s, "say nginx-drop already-correct")
	if cmp < 0 || ok < cmp {
		t.Errorf("already-correct check missing or out of order: %d %d", cmp, ok)
	}
	mustContain(t, s, `sudo cmp -s "$NEWFILT" "$PFILTER"`, "say nginx-probes already-correct")
}

func TestDropSnippetAndMapShape(t *testing.T) {
	s := script(Options{NginxDropProbes: true})
	mustContain(t, s, `echo 'if ($baryovm_probe) {'`, `echo '    return 444;'`, `echo 'map $uri $baryovm_probe {'`, `echo '    default 0;'`, `"~*%s" 1;`)
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
