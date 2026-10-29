// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"github.com/BaryoDev/BaryoVM/internal/release"
)

// siteHost is a fake VM for the site commands. It records every command and answers the probes the
// way a clean Rocky host with SELinux would; fail names a substring whose command exits non-zero.
type siteHost struct {
	seen    []string
	conf    string // answer to the server block probe
	selinux string
	fail    string
	failNth int // fail only the Nth match of fail, 0 for all
	hits    int
	onRun   func(cmd string) // runs before each command, to change things mid-run
}

func (h *siteHost) Run(cmd string) (string, error) {
	h.seen = append(h.seen, cmd)
	if h.onRun != nil {
		h.onRun(cmd)
	}
	if h.fail != "" && strings.Contains(cmd, h.fail) {
		h.hits++
		if h.failNth == 0 || h.hits == h.failNth {
			return "failed: " + h.fail, fmt.Errorf("exit status 1")
		}
	}
	switch {
	case strings.Contains(cmd, "head -n 1"):
		return h.conf, nil
	case strings.Contains(cmd, "selinuxenabled"):
		return h.selinux, nil
	case strings.Contains(cmd, "echo options"):
		return "options\ndhparam\n", nil
	}
	return "", nil
}

func (h *siteHost) Close() error { return nil }

func (h *siteHost) has(sub string) int {
	n := 0
	for _, c := range h.seen {
		if strings.Contains(c, sub) {
			n++
		}
	}
	return n
}

// order labels each recorded command, so a test reads as the sequence the VM saw.
func (h *siteHost) order() []string {
	var out []string
	for _, c := range h.seen {
		switch {
		case strings.Contains(c, "head -n 1"):
			out = append(out, "probe")
		case strings.Contains(c, "echo options"), strings.Contains(c, "selinuxenabled"), strings.Contains(c, "echo present"):
			out = append(out, "probe")
		case strings.Contains(c, "mkdir -p"):
			out = append(out, "mkdir")
		case strings.Contains(c, "restorecon"):
			out = append(out, "restorecon")
		case strings.Contains(c, "base64 -d"):
			out = append(out, "write")
		case strings.Contains(c, "nginx -t"):
			out = append(out, "nginx -t")
		case strings.Contains(c, "systemctl reload"):
			out = append(out, "reload")
		case strings.Contains(c, "certbot"):
			out = append(out, "certbot")
		case strings.Contains(c, "rm -f"):
			out = append(out, "rm")
		case strings.Contains(c, "mv -f"):
			out = append(out, "mv")
		case strings.Contains(c, "cp -p"):
			out = append(out, "cp")
		default:
			out = append(out, c)
		}
	}
	// collapse the three probes into one word
	var s []string
	for _, o := range out {
		if o == "probe" && len(s) > 0 && s[len(s)-1] == "probe" {
			continue
		}
		s = append(s, o)
	}
	return s
}

func siteFixture(t *testing.T) *siteHost {
	t.Helper()
	t.Setenv("BARYOVM_HOME", t.TempDir())
	store := &fleet.Store{VMs: []fleet.VM{{Name: "web1", Host: "10.0.0.2", User: "opc", KeyPath: "/keys/id"}}}
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	h := &siteHost{conf: "absent\n", selinux: "selinux\n"}
	prev := dialRelease
	dialRelease = func(*fleet.VM) (remoteSession, error) { return h, nil }
	t.Cleanup(func() { dialRelease = prev })
	return h
}

func noDial(t *testing.T) {
	t.Helper()
	prev := dialRelease
	dialRelease = func(*fleet.VM) (remoteSession, error) {
		t.Fatal("a remote connection was opened")
		return nil, nil
	}
	t.Cleanup(func() { dialRelease = prev })
}

var siteAddArgs = []string{"example.com", "--vm", "web1", "--root", "/var/www/example.com", "--email", "ops@example.com"}

func eqOrder(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, " | ") != strings.Join(want, " | ") {
		t.Errorf("order:\n got  %s\n want %s", strings.Join(got, " | "), strings.Join(want, " | "))
	}
}

func TestSiteAddHappyPathOrderAndRecord(t *testing.T) {
	h := siteFixture(t)
	stdout, _ := runCmd(t, newSiteAddCmd(), nil, true, siteAddArgs...)
	eqOrder(t, h.order(), []string{"probe", "mkdir", "restorecon", "write", "nginx -t", "reload", "certbot", "write", "nginx -t", "reload"})

	env, data := parseEnvelope(t, stdout)
	if env.Action != "site add" {
		t.Errorf("action = %q", env.Action)
	}
	d := dataField(t, data)
	if d["domain"] != "example.com" || d["config"] != "/etc/nginx/conf.d/example.com.conf" || d["selinux"] != true {
		t.Errorf("data = %s", data)
	}
	// Recorded for site deploy.
	store, err := fleet.Load()
	if err != nil {
		t.Fatal(err)
	}
	if s := store.FindSite("example.com"); s == nil || s.VM != "web1" || s.Root != "/var/www/example.com" {
		t.Errorf("site not recorded: %+v", s)
	}
}

func TestSiteAddJSONIsOneDocument(t *testing.T) {
	siteFixture(t)
	stdout, _ := runCmd(t, newSiteAddCmd(), nil, true, siteAddArgs...)
	dec := json.NewDecoder(strings.NewReader(stdout))
	n := 0
	for dec.More() {
		var v map[string]any
		if err := dec.Decode(&v); err != nil {
			t.Fatalf("not JSON: %v\n%s", err, stdout)
		}
		n++
	}
	if n != 1 {
		t.Errorf("%d documents on stdout, want 1:\n%s", n, stdout)
	}
}

func TestSiteAddFailureIsOneDocumentAndSilent(t *testing.T) {
	h := siteFixture(t)
	h.fail = "certbot"
	stdout, _, err := runCmdE(t, newSiteAddCmd(), nil, true, siteAddArgs...)
	if err == nil {
		t.Fatal("expected an error")
	}
	if ee, ok := err.(*exitError); !ok || !ee.silent || ee.code != 1 {
		t.Errorf("err = %#v, want a silent exitError with code 1", err)
	}
	if n := strings.Count(strings.TrimSpace(stdout), "\n"); n != 0 && !strings.HasPrefix(strings.TrimSpace(stdout), "{") {
		t.Errorf("stdout is not one document:\n%s", stdout)
	}
	var env struct {
		OK    bool           `json:"ok"`
		Error string         `json:"error"`
		Data  map[string]any `json:"data"`
	}
	if jerr := json.Unmarshal([]byte(stdout), &env); jerr != nil {
		t.Fatalf("stdout is not exactly one JSON document: %v\n%s", jerr, stdout)
	}
	if env.OK || env.Data["failedStep"] != "certificate" || env.Data["rolledBack"] != true {
		t.Errorf("envelope = %+v", env)
	}
	if !strings.Contains(env.Error, `"certificate"`) {
		t.Errorf("error does not name the step: %s", env.Error)
	}
	// Nothing recorded for a site that failed.
	store, _ := fleet.Load()
	if store.FindSite("example.com") != nil {
		t.Error("a failed add was recorded")
	}
}

func TestSiteAddNginxTestFailureRollsBack(t *testing.T) {
	h := siteFixture(t)
	h.fail, h.failNth = "nginx -t", 2 // the full block is rejected
	stdout, _, err := runCmdE(t, newSiteAddCmd(), nil, true, siteAddArgs...)
	if err == nil {
		t.Fatal("expected an error")
	}
	eqOrder(t, h.order(), []string{"probe", "mkdir", "restorecon", "write", "nginx -t", "reload", "certbot", "write", "nginx -t", "rm", "nginx -t", "reload"})
	var env struct {
		Data map[string]any `json:"data"`
	}
	if jerr := json.Unmarshal([]byte(stdout), &env); jerr != nil {
		t.Fatalf("not one JSON document: %v\n%s", jerr, stdout)
	}
	if env.Data["failedStep"] != "full block" || env.Data["rolledBack"] != true {
		t.Errorf("envelope does not report the step and the rollback:\n%s", stdout)
	}
}

func TestSiteAddCertbotFailureLeavesNginxConsistent(t *testing.T) {
	h := siteFixture(t)
	h.fail = "certbot"
	if _, _, err := runCmdE(t, newSiteAddCmd(), nil, true, siteAddArgs...); err == nil {
		t.Fatal("expected an error")
	}
	// The HTTP block was reloaded in; it is taken out again and nginx reloaded back.
	eqOrder(t, h.order(), []string{"probe", "mkdir", "restorecon", "write", "nginx -t", "reload", "certbot", "rm", "nginx -t", "reload"})
}

func TestSiteAddRefusesBadInputBeforeAnyRemoteCommand(t *testing.T) {
	siteFixture(t)
	noDial(t)
	for _, args := range [][]string{
		{"a;b.com", "--vm", "web1", "--root", "/var/www/x"},
		{"$(id).com", "--vm", "web1", "--root", "/var/www/x"},
		{"example.com", "--vm", "web1", "--root", "/"},
		{"example.com", "--vm", "web1", "--root", "/var/www/../etc"},
		{"example.com", "--vm", "web1", "--root", "relative/dir"},
		{"example.com", "--vm", "web1", "--root", "/var/www/x", "--email", "nope"},
	} {
		stdout, _, err := runCmdE(t, newSiteAddCmd(), nil, true, args...)
		if err == nil {
			t.Errorf("%v accepted", args)
			continue
		}
		var env struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		}
		if jerr := json.Unmarshal([]byte(stdout), &env); jerr != nil || env.OK || env.Error == "" {
			t.Errorf("%v: want one failure envelope, got %v: %s", args, jerr, stdout)
		}
	}
}

func TestSiteAddRefusesAKnownSiteWithoutForce(t *testing.T) {
	h := siteFixture(t)
	store, _ := fleet.Load()
	store.UpsertSite(fleet.Site{Domain: "example.com", VM: "web1", Root: "/var/www/example.com"})
	_ = store.Save()
	noDial(t)
	_, _, err := runCmdE(t, newSiteAddCmd(), nil, true, siteAddArgs...)
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("err = %v", err)
	}
	if len(h.seen) != 0 {
		t.Errorf("ran commands: %v", h.seen)
	}
}

func TestSiteAddRefusesAnExistingServerBlockWithoutForce(t *testing.T) {
	h := siteFixture(t)
	h.conf = "foreign\n"
	_, _, err := runCmdE(t, newSiteAddCmd(), nil, true, siteAddArgs...)
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("err = %v", err)
	}
	if h.has("base64") != 0 || h.has("certbot") != 0 {
		t.Errorf("wrote something: %v", h.order())
	}
	// With --force it goes ahead, copying the old file aside first.
	h2 := siteFixture(t)
	h2.conf = "foreign\n"
	runCmd(t, newSiteAddCmd(), nil, true, append([]string{"--force"}, siteAddArgs...)...)
	eqOrder(t, h2.order()[:5], []string{"probe", "mkdir", "restorecon", "cp", "write"})
}

func TestSiteAddDryRunNeverDialsOrRecords(t *testing.T) {
	siteFixture(t)
	noDial(t)
	stdout, _ := runCmd(t, newSiteAddCmd(), nil, true, append([]string{"--dry-run"}, siteAddArgs...)...)
	env, data := parseEnvelope(t, stdout)
	if env.Action != "site add" || !strings.Contains(data, `"dryRun":true`) {
		t.Errorf("data = %s", data)
	}
	if !strings.Contains(data, "certbot certonly") || !strings.Contains(data, "gzip_types") {
		t.Errorf("plan lacks the certbot command or the server block: %s", data)
	}
	store, _ := fleet.Load()
	if store.FindSite("example.com") != nil {
		t.Error("dry run recorded the site")
	}
}

func TestSiteCommandsAreRegistered(t *testing.T) {
	for _, path := range [][]string{{"site", "add"}, {"site", "deploy"}} {
		cmd, _, err := newRoot().Find(path)
		if err != nil || cmd.Name() != path[1] {
			t.Errorf("%v not registered: %v", path, err)
		}
	}
}

// siteDir makes a built export with one file in it.
func siteDir(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, "index.html"), []byte("<h1>hi</h1>"), 0o600); err != nil {
		t.Fatal(err)
	}
	return d
}

func recordSite(t *testing.T) {
	t.Helper()
	store, _ := fleet.Load()
	store.UpsertSite(fleet.Site{Domain: "example.com", VM: "web1", Root: "/var/www/example.com"})
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
}

func withRsync(t *testing.T, ran *[]*exec.Cmd) {
	t.Helper()
	prevLook, prevRun, prevKH := lookPath, runLocal, knownHostsFileFor
	lookPath = func(string) (string, error) { return "/usr/bin/rsync", nil }
	runLocal = func(c *exec.Cmd) (string, error) { *ran = append(*ran, c); return "", nil }
	knownHostsFileFor = func(*fleet.VM) (string, func(), error) { return "/tmp/kh", func() {}, nil }
	t.Cleanup(func() { lookPath, runLocal, knownHostsFileFor = prevLook, prevRun, prevKH })
}

func TestSiteDeployRsyncsTheContentsThenReloads(t *testing.T) {
	h := siteFixture(t)
	recordSite(t)
	var ran []*exec.Cmd
	withRsync(t, &ran)
	dir := siteDir(t)

	stdout, _ := runCmd(t, newSiteDeployCmd(), nil, true, "example.com", dir)
	if len(ran) != 1 {
		t.Fatalf("%d local commands, want one rsync", len(ran))
	}
	args := ran[0].Args
	if args[0] != "rsync" {
		t.Errorf("ran %v", args)
	}
	// The release code's rsync: sudo on the far side, contents of dir (trailing slash) into the webroot.
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--rsync-path=sudo -n rsync") || !strings.Contains(joined, "StrictHostKeyChecking=yes") {
		t.Errorf("not the release rsync: %s", joined)
	}
	if args[len(args)-2] != dir+"/" || args[len(args)-1] != "opc@10.0.0.2:/var/www/example.com/" {
		t.Errorf("source and destination = %q %q", args[len(args)-2], args[len(args)-1])
	}
	eqOrder(t, h.order(), []string{"probe", "restorecon", "nginx -t", "reload"})
	env, _ := parseEnvelope(t, stdout)
	if env.Action != "site deploy" {
		t.Errorf("action = %q", env.Action)
	}
}

func TestSiteDeployRefusesBeforeAnyTransfer(t *testing.T) {
	siteFixture(t)
	recordSite(t)
	noDial(t)
	var ran []*exec.Cmd
	withRsync(t, &ran)

	empty := t.TempDir()
	cases := map[string][]string{
		"empty dir":    {"example.com", empty},
		"missing dir":  {"example.com", filepath.Join(empty, "nope")},
		"bad domain":   {"a;b.com", siteDir(t)},
		"unknown site": {"other.com", siteDir(t)},
	}
	for name, args := range cases {
		if _, _, err := runCmdE(t, newSiteDeployCmd(), nil, true, args...); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if len(ran) != 0 {
		t.Errorf("rsync ran %d times", len(ran))
	}
}

func TestSiteDeployRefusesATamperedRoot(t *testing.T) {
	siteFixture(t)
	store, _ := fleet.Load()
	store.UpsertSite(fleet.Site{Domain: "example.com", VM: "web1", Root: "/"})
	_ = store.Save()
	var ran []*exec.Cmd
	withRsync(t, &ran)
	if _, _, err := runCmdE(t, newSiteDeployCmd(), nil, true, "example.com", siteDir(t)); err == nil || len(ran) != 0 {
		t.Errorf("err = %v, rsync ran %d times", err, len(ran))
	}
}

func TestSiteDeployNginxTestFailureReportsTheStep(t *testing.T) {
	h := siteFixture(t)
	h.fail = "nginx -t"
	recordSite(t)
	var ran []*exec.Cmd
	withRsync(t, &ran)
	stdout, _, err := runCmdE(t, newSiteDeployCmd(), nil, true, "example.com", siteDir(t))
	var env struct {
		OK   bool           `json:"ok"`
		Data map[string]any `json:"data"`
	}
	if jerr := json.Unmarshal([]byte(stdout), &env); jerr != nil || env.OK {
		t.Fatalf("want one failure envelope: %v\n%s", jerr, stdout)
	}
	if err == nil || !strings.Contains(err.Error(), `"nginx -t"`) {
		t.Fatalf("err = %v\n%s", err, stdout)
	}
	if h.has("systemctl reload") != 0 {
		t.Error("nginx was reloaded after nginx -t failed")
	}
}

func TestSiteDeployDryRunNeverTransfers(t *testing.T) {
	siteFixture(t)
	recordSite(t)
	noDial(t)
	var ran []*exec.Cmd
	withRsync(t, &ran)
	stdout, _ := runCmd(t, newSiteDeployCmd(), nil, true, "--dry-run", "example.com", siteDir(t))
	if len(ran) != 0 {
		t.Error("dry run ran rsync")
	}
	if !strings.Contains(stdout, "--rsync-path=sudo -n rsync") {
		t.Errorf("plan lacks the rsync: %s", stdout)
	}
}

// The deploy path uses the release code's transport choice, so the tar fallback must work too.
func TestSiteDeployFallsBackToTarWithoutRsync(t *testing.T) {
	var _ release.Transport = release.TransportTar
	h := siteFixture(t)
	recordSite(t)
	prev := lookPath
	lookPath = func(string) (string, error) { return "", fmt.Errorf("not found") }
	t.Cleanup(func() { lookPath = prev })
	stdout, _, _ := runCmdE(t, newSiteDeployCmd(), nil, true, "--dry-run", "example.com", siteDir(t))
	if !strings.Contains(stdout, `"transport": "tar"`) {
		t.Errorf("transport not tar: %s", stdout)
	}
	_ = h
}

func TestSiteAddKeepsFleetChangesMadeDuringTheRun(t *testing.T) {
	h := siteFixture(t)
	h.onRun = func(cmd string) {
		if !strings.Contains(cmd, "certbot") {
			return
		}
		// Another baryovm command registers a VM while the slow remote steps run.
		s, err := fleet.Load()
		if err != nil {
			t.Error(err)
			return
		}
		s.VMs = append(s.VMs, fleet.VM{Name: "web2", Host: "10.0.0.3", User: "opc", KeyPath: "/keys/id"})
		if err := s.Save(); err != nil {
			t.Error(err)
		}
	}
	runCmd(t, newSiteAddCmd(), nil, true, siteAddArgs...)
	s, err := fleet.Load()
	if err != nil {
		t.Fatal(err)
	}
	if s.Find("web2") == nil || s.FindSite("example.com") == nil {
		t.Errorf("web2 = %v, site = %v: one write clobbered the other", s.Find("web2"), s.FindSite("example.com"))
	}
}
