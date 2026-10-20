package ghrunner

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/BaryoDev/BaryoVM/internal/sshx"
)

const testDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
const secretToken = "AABBCCSECRETTOKEN123"

func opts() Options {
	return Options{VM: "web", Repo: "acme/app", Version: "2.320.0", MemoryMax: "3G", CPUQuota: "150%", Labels: []string{"docker", "gpu"}}
}

func validOpts(t *testing.T) Options {
	t.Helper()
	o := opts()
	if err := o.Validate(); err != nil {
		t.Fatal(err)
	}
	return o
}

type call struct {
	kind  string // run or input
	cmd   string
	stdin string
}

type fakeRemote struct {
	calls   []call
	probe   string
	inputEr error
	runEr   func(cmd string) error
	closed  bool
}

func (f *fakeRemote) Run(cmd string) (string, error) {
	f.calls = append(f.calls, call{kind: "run", cmd: cmd})
	if strings.Contains(cmd, `echo "arch=`) {
		return f.probe, nil
	}
	if f.runEr != nil {
		return "", f.runEr(cmd)
	}
	return "", nil
}

func (f *fakeRemote) RunInput(cmd, stdin string) (string, error) {
	f.calls = append(f.calls, call{kind: "input", cmd: cmd, stdin: stdin})
	return "", f.inputEr
}

func (f *fakeRemote) Close() error { f.closed = true; return nil }

// ran skips the probe, which names config.sh in its own test -x check.
func (f *fakeRemote) ran(sub string) bool {
	for _, c := range f.calls {
		if strings.Contains(c.cmd, `echo "arch=`) {
			continue
		}
		if strings.Contains(c.cmd, sub) {
			return true
		}
	}
	return false
}

type fakeGH struct {
	private    bool
	visibility string
	release    Release
	events     *[]string
	tokenErr   error
}

func (g *fakeGH) Visibility(string) (string, error) {
	if g.visibility != "" {
		return g.visibility, nil
	}
	if g.private {
		return "private", nil
	}
	return "public", nil
}
func (g *fakeGH) Release(v string) (Release, error) {
	return g.release, nil
}
func (g *fakeGH) RegistrationToken(string) (string, error) {
	if g.events != nil {
		*g.events = append(*g.events, "token")
	}
	return secretToken, g.tokenErr
}
func (g *fakeGH) RemoveToken(string) (string, error) {
	return secretToken, g.tokenErr
}

func goodRelease() Release {
	return Release{Version: "2.320.0", Assets: []Asset{
		{Name: "actions-runner-linux-x64-2.320.0.tar.gz", Digest: "sha256:" + testDigest},
		{Name: "actions-runner-linux-arm64-2.320.0.tar.gz", Digest: "sha256:" + testDigest},
	}}
}

func dialer(f *fakeRemote, dials *int) Dial {
	return func() (Remote, error) { *dials++; return f, nil }
}

const freshProbe = "arch=x86_64\nbinary=0\nconfigured=0\nunit=\nactive=0\n"

func TestInstallScriptQuotesEveryValueAndHasNoToken(t *testing.T) {
	o := validOpts(t)
	s := BuildInstall(o, "x64", testDigest)
	for _, want := range []string{
		sshx.Quote(o.Dir()),
		sshx.Quote("https://github.com/actions/runner/releases/download/v2.320.0/actions-runner-linux-x64-2.320.0.tar.gz"),
		sshx.Quote(testDigest),
	} {
		if !strings.Contains(s, want) {
			t.Errorf("install script is missing the quoted value %s:\n%s", want, s)
		}
	}
	if strings.Contains(s, "TOKEN") || strings.Contains(s, secretToken) {
		t.Errorf("install script mentions a token:\n%s", s)
	}
}

func TestNoScriptEnablesXtrace(t *testing.T) {
	o := validOpts(t)
	for name, s := range map[string]string{
		"probe":   BuildProbe(o),
		"install": BuildInstall(o, "x64", testDigest),
		"config":  BuildConfig(o, "x64"),
		"service": BuildService(o),
		"start":   BuildStart(o),
		"stop":    BuildRemoveStop(o),
		"rmcfg":   BuildRemoveConfig(o, false),
		"rmloc":   BuildRemoveConfig(o, true),
		"delete":  BuildRemoveDelete(o),
	} {
		if strings.Contains(s, "set -x") || strings.Contains(s, "set -eux") || strings.Contains(s, "-o xtrace") {
			t.Errorf("%s script enables tracing, which would print the token:\n%s", name, s)
		}
	}
}

func TestInstallChecksDigestBeforeTar(t *testing.T) {
	s := BuildInstall(validOpts(t), "x64", testDigest)
	check := "printf '%s  %s\\n' " + sshx.Quote(testDigest) + ` "$tmp" | sha256sum -c - >/dev/null`
	ci := strings.Index(s, check)
	tar := strings.Index(s, "tar -xzf")
	if ci < 0 || tar < 0 || ci > tar {
		t.Fatalf("exact digest check must come before tar (check at %d, tar at %d):\n%s", ci, tar, s)
	}
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, "sha256sum") && (strings.Contains(l, "||") || strings.Contains(l, "; true")) {
			t.Errorf("the digest check must not be allowed to fail: %s", l)
		}
	}
	if !strings.Contains(s, "set -eu") {
		t.Error("a failed digest check must stop the script")
	}
}

func TestInstallTimesOutCurlAndLabelsSELinuxAndMarksCompletion(t *testing.T) {
	s := BuildInstall(validOpts(t), "x64", testDigest)
	if !strings.Contains(s, "curl -fsSL --connect-timeout 15 --max-time 600 --retry 2") {
		t.Errorf("curl has no timeouts:\n%s", s)
	}
	if strings.Index(s, "restorecon") < strings.Index(s, "tar -xzf") || !strings.Contains(s, "getenforce") {
		t.Errorf("restorecon must follow tar and be skipped without getenforce:\n%s", s)
	}
	lines := strings.Split(strings.TrimSpace(s), "\n")
	last := lines[len(lines)-1]
	if !strings.Contains(last, "touch") || !strings.HasSuffix(last, "/.baryovm-installed") {
		t.Errorf("the marker must be the last step, got %q", last)
	}
	if strings.Index(s, "installdependencies.sh") > strings.Index(s, ".baryovm-installed") {
		t.Error("the marker must come after installdependencies.sh")
	}
}

func TestProbeTrustsTheMarkerNotConfigSh(t *testing.T) {
	p := BuildProbe(validOpts(t))
	if !strings.Contains(p, ".baryovm-installed") || strings.Contains(p, "config.sh") {
		t.Errorf("binary=1 must depend on the marker only:\n%s", p)
	}
}

func TestServiceScriptRestoreconBeforeStartAndCarriesLimits(t *testing.T) {
	o := validOpts(t)
	s := BuildService(o)
	rc := strings.Index(s, "restorecon")
	dr := strings.Index(s, "systemctl daemon-reload")
	st := strings.Index(s, "systemctl restart")
	if rc < 0 || dr < 0 || st < 0 || rc > st || dr > st {
		t.Fatalf("restorecon and daemon-reload must run before the restart:\n%s", s)
	}
	if !strings.Contains(s, "getenforce") {
		t.Error("restorecon must be skipped when getenforce is absent")
	}
	if !strings.Contains(s, "\nMemoryMax=3G\nCPUQuota=150%\n") {
		t.Errorf("drop-in does not carry the exact limits:\n%s", s)
	}
	if !strings.Contains(s, `systemctl is-active --quiet "$unit" ||`) || !strings.Contains(s, "exit 1") {
		t.Errorf("the active check must fail the script:\n%s", s)
	}
	want := "cd " + sshx.Quote(o.Dir()) + " && sudo -n ./svc.sh install github-runner"
	if !strings.Contains(s, want) {
		t.Errorf("svc.sh must run as %q:\n%s", want, s)
	}
	if !strings.Contains(s, "cat "+sshx.Quote(o.Dir())+"/.service") {
		t.Error("the unit name must be read from .service")
	}
}

func TestUnitNameFromServiceFileIsValidatedBeforeRootUse(t *testing.T) {
	o := validOpts(t)
	cases := []struct{ name, script, firstUse string }{
		{"service", BuildService(o), `mkdir -p "$dropin"`},
		{"start", BuildStart(o), "systemctl start"},
		{"stop", BuildRemoveStop(o), "./svc.sh stop"},
	}
	for _, c := range cases {
		g := strings.Index(c.script, `*[!A-Za-z0-9_.@-]*) echo`)
		u := strings.Index(c.script, c.firstUse)
		if g < 0 || u < 0 || g > u {
			t.Errorf("%s: the unit guard must come before %q (guard at %d, use at %d):\n%s", c.name, c.firstUse, g, u, c.script)
		}
	}
}

func TestConfigRunsAsRunnerUserAndReadsTokenFromStdin(t *testing.T) {
	o := validOpts(t)
	c := BuildConfig(o, "x64")
	if !strings.HasPrefix(c, "sudo -n -u github-runner sh -c ") {
		t.Fatalf("config.sh must run under sudo -n -u github-runner: %s", c)
	}
	for _, want := range []string{"read -r T", "ACTIONS_RUNNER_INPUT_TOKEN", "--replace", "--unattended", "--labels"} {
		if !strings.Contains(c, want) {
			t.Errorf("config command is missing %q: %s", want, c)
		}
	}
	if strings.Contains(c, "--token") {
		t.Errorf("the token must never be a flag: %s", c)
	}
}

func TestLabelsAreDeduplicatedAgainstBuiltIns(t *testing.T) {
	o := Options{Labels: []string{"docker", "Docker", "self-hosted", "linux", "X64", "gpu"}}
	got := strings.Join(o.ExtraLabels("x64"), ",")
	if got != "docker,gpu" {
		t.Errorf("got %q, want docker,gpu", got)
	}
	o.Labels = nil
	if strings.Contains(BuildConfig(Options{VM: "w", Repo: "a/b", Name: "n"}, "x64"), "--labels") {
		t.Error("no labels should mean no --labels flag")
	}
}

func TestValidationRefusesInjection(t *testing.T) {
	evil := []string{"a'b", "a b", "a;b", "$(touch x)", "a`id`", "a\nb", "a&b", "a|b", "a\"b"}
	for _, e := range evil {
		bad := map[string]Options{
			"repo":    {VM: "w", Repo: "acme/" + e},
			"owner":   {VM: "w", Repo: e + "/app"},
			"name":    {VM: "w", Repo: "a/b", Name: e},
			"label":   {VM: "w", Repo: "a/b", Labels: []string{e}},
			"version": {VM: "w", Repo: "a/b", Version: e},
			"memory":  {VM: "w", Repo: "a/b", MemoryMax: e},
			"cpu":     {VM: "w", Repo: "a/b", CPUQuota: e},
		}
		for field, o := range bad {
			if err := o.Validate(); err == nil {
				t.Errorf("%s %q was accepted", field, e)
			}
		}
	}
	for _, o := range []Options{
		{VM: "w", Repo: "a/b", Version: "1.2"},
		{VM: "w", Repo: "a/b", Version: "v1.2.3"},
		{VM: "w", Repo: "a/b", Name: strings.Repeat("a", 65)},
		{VM: "w", Repo: "a/b", MemoryMax: "0"},
		{VM: "w", Repo: "a/b", CPUQuota: "200"},
		{VM: "w", Repo: "../b"},
		{VM: "w", Repo: "a/b/c"},
		{VM: "w", Repo: "a/b", Name: "-rf"},
		{VM: "w", Repo: "a/b", Name: ".hidden"},
		{VM: "w", Repo: "a/b", Labels: []string{"-x"}},
	} {
		if err := o.Validate(); err == nil {
			t.Errorf("%+v was accepted", o)
		}
	}
}

func TestValidationDefaults(t *testing.T) {
	o := Options{VM: "web", Repo: "acme/app"}
	if err := o.Validate(); err != nil {
		t.Fatal(err)
	}
	if o.Name != "web-app" || o.MemoryMax != "4G" || o.CPUQuota != "200%" {
		t.Errorf("defaults wrong: %+v", o)
	}
	if o.Dir() != "/opt/github-runner/acme__app" {
		t.Errorf("dir = %s", o.Dir())
	}
}

func TestScriptsQuoteValuesEvenIfValidationWereBypassed(t *testing.T) {
	o := Options{VM: "w", Repo: "a/b'; id #", Name: "n$(id)", Version: "1.2.3';id", MemoryMax: "4G'", CPUQuota: "1%';id"}
	d := sshx.Quote(o.Dir())
	if !strings.Contains(BuildInstall(o, "x64", testDigest), d) {
		t.Error("install: dir not quoted")
	}
	if !strings.Contains(BuildService(o), d) || !strings.Contains(BuildService(o), sshx.Quote("[Service]\nMemoryMax=4G'\nCPUQuota=1%';id\n")) {
		t.Error("service: values not quoted")
	}
	if !strings.Contains(BuildInstall(o, "x64", testDigest), sshx.Quote("https://github.com/actions/runner/releases/download/v1.2.3';id/actions-runner-linux-x64-1.2.3';id.tar.gz")) {
		t.Error("install: url not quoted")
	}
	if !strings.Contains(BuildRemoveDelete(o), d) {
		t.Error("delete: dir not quoted")
	}
	cfg := BuildConfig(o, "x64")
	if !strings.Contains(cfg, `--name '\''n$(id)'\''`) {
		t.Errorf("config: name not quoted inside the sh -c script: %s", cfg)
	}
	if !strings.Contains(BuildProbe(o), d) {
		t.Error("probe: dir not quoted")
	}
}

func TestArchMapping(t *testing.T) {
	for in, want := range map[string]string{"aarch64": "arm64", "arm64": "arm64", "x86_64": "x64", "x86_64\n": "x64"} {
		got, err := Arch(in)
		if err != nil || got != want {
			t.Errorf("Arch(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := Arch("riscv64"); err == nil {
		t.Error("an unknown arch must be refused")
	}
}

func TestPublicRepoRefusedBeforeAnySSHDial(t *testing.T) {
	f := &fakeRemote{probe: freshProbe}
	dials := 0
	_, err := Install(opts(), &fakeGH{private: false, release: goodRelease()}, dialer(f, &dials))
	if err == nil || !strings.Contains(err.Error(), "public repo") {
		t.Fatalf("want a public repo refusal, got %v", err)
	}
	if dials != 0 || len(f.calls) != 0 {
		t.Errorf("a public repo reached the VM: dials=%d calls=%d", dials, len(f.calls))
	}
}

func TestInternalRepoRefusedBeforeAnySSHDial(t *testing.T) {
	f := &fakeRemote{probe: freshProbe}
	dials := 0
	_, err := Install(opts(), &fakeGH{private: true, visibility: "internal", release: goodRelease()}, dialer(f, &dials))
	if err == nil || !strings.Contains(err.Error(), "internal repo") {
		t.Fatalf("want an internal repo refusal, got %v", err)
	}
	if dials != 0 || len(f.calls) != 0 {
		t.Errorf("an internal repo reached the VM: dials=%d calls=%d", dials, len(f.calls))
	}
}

func TestAlreadyConfiguredSkipsDownloadAndConfig(t *testing.T) {
	f := &fakeRemote{probe: "arch=aarch64\nbinary=1\nconfigured=1\nunit=actions.runner.x.service\nactive=1\n"}
	dials := 0
	rep, err := Install(opts(), &fakeGH{private: true, release: goodRelease()}, dialer(f, &dials))
	if err != nil {
		t.Fatal(err)
	}
	if !rep.AlreadyReady {
		t.Error("alreadyReady was not reported")
	}
	if len(f.calls) != 1 {
		t.Errorf("only the probe should have run, got %d calls", len(f.calls))
	}
	if !f.closed {
		t.Error("connection not closed")
	}
}

func TestConfiguredButStoppedGetsStartedNotReinstalled(t *testing.T) {
	f := &fakeRemote{probe: "arch=x86_64\nbinary=1\nconfigured=1\nunit=actions.runner.x.service\nactive=0\n"}
	dials := 0
	rep, err := Install(opts(), &fakeGH{private: true, release: goodRelease()}, dialer(f, &dials))
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Started || rep.AlreadyReady {
		t.Errorf("report = %+v", rep)
	}
	if f.ran("curl") || f.ran("config.sh") || f.ran("svc.sh install") {
		t.Error("a stopped runner must not be downloaded, reconfigured or reinstalled")
	}
	if !f.ran("systemctl start") {
		t.Error("the stopped unit was not started")
	}
}

func TestConfiguredWithoutUnitInstallsServiceOnly(t *testing.T) {
	f := &fakeRemote{probe: "arch=x86_64\nbinary=1\nconfigured=1\nunit=\nactive=0\n"}
	dials := 0
	rep, err := Install(opts(), &fakeGH{private: true, release: goodRelease()}, dialer(f, &dials))
	if err != nil {
		t.Fatal(err)
	}
	if !rep.ServiceInstalled || rep.Configured || rep.Installed {
		t.Errorf("report = %+v", rep)
	}
	if !f.ran("svc.sh install") || f.ran("curl") || f.ran("config.sh") {
		t.Error("only the service should have been installed")
	}
}

func TestFreshInstallOrderAndTokenOnStdinOnly(t *testing.T) {
	f := &fakeRemote{probe: freshProbe}
	dials := 0
	var events []string
	gh := &fakeGH{private: true, release: goodRelease(), events: &events}
	rep, err := Install(opts(), gh, dialer(f, &dials))
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Installed || !rep.Configured || !rep.ServiceInstalled || !rep.Started {
		t.Errorf("report = %+v", rep)
	}
	var kinds []string
	for _, c := range f.calls {
		if strings.Contains(c.cmd, secretToken) {
			t.Errorf("the token is on a command line: %s", c.cmd)
		}
		switch {
		case strings.Contains(c.cmd, `echo "arch=`):
			kinds = append(kinds, "probe")
		case strings.Contains(c.cmd, "curl"):
			kinds = append(kinds, "install")
		case c.kind == "input":
			kinds = append(kinds, "config")
			if c.stdin != secretToken+"\n" {
				t.Errorf("stdin = %q", c.stdin)
			}
		case strings.Contains(c.cmd, "svc.sh install"):
			kinds = append(kinds, "service")
		}
	}
	if got := strings.Join(kinds, ","); got != "probe,install,config,service" {
		t.Errorf("order = %s", got)
	}
	if len(events) != 1 {
		t.Errorf("the token should be fetched once, got %v", events)
	}
}

func TestTokenOnRemoteStderrIsRedactedFromErrorAndJSON(t *testing.T) {
	f := &fakeRemote{
		probe:   freshProbe,
		inputEr: fmt.Errorf("remote `sudo -n -u github-runner sh -c ...`: exit status 1: invalid token %s", secretToken),
	}
	dials := 0
	rep, err := Install(opts(), &fakeGH{private: true, release: goodRelease()}, dialer(f, &dials))
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), secretToken) {
		t.Errorf("token leaked in the error: %v", err)
	}
	if !strings.Contains(err.Error(), "[redacted]") {
		t.Errorf("redaction marker missing: %v", err)
	}
	b, _ := json.Marshal(map[string]any{"data": rep, "error": err.Error()})
	if strings.Contains(string(b), secretToken) {
		t.Errorf("token leaked in the JSON: %s", b)
	}
}

func TestMissingDigestExtractsNothing(t *testing.T) {
	rel := Release{Version: "2.320.0", Assets: []Asset{{Name: "actions-runner-linux-x64-2.320.0.tar.gz"}}}
	f := &fakeRemote{probe: freshProbe}
	dials := 0
	_, err := Install(opts(), &fakeGH{private: true, release: rel}, dialer(f, &dials))
	if err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("want a digest error, got %v", err)
	}
	if f.ran("tar ") || f.ran("curl") {
		t.Error("nothing may be downloaded or extracted without a digest")
	}
	for _, c := range f.calls {
		if c.kind == "input" {
			t.Error("no token may be sent after a failed digest check")
		}
	}
}

func TestMalformedDigestIsRefused(t *testing.T) {
	rel := Release{Version: "2.320.0", Assets: []Asset{{Name: "actions-runner-linux-x64-2.320.0.tar.gz", Digest: "sha256:zz; id"}}}
	f := &fakeRemote{probe: freshProbe}
	dials := 0
	if _, err := Install(opts(), &fakeGH{private: true, release: rel}, dialer(f, &dials)); err == nil {
		t.Fatal("a malformed digest was accepted")
	}
	if f.ran("tar ") {
		t.Error("extracted despite a bad digest")
	}
}

func TestUnknownArchStopsBeforeInstall(t *testing.T) {
	f := &fakeRemote{probe: "arch=riscv64\nbinary=0\nconfigured=0\nunit=\nactive=0\n"}
	dials := 0
	if _, err := Install(opts(), &fakeGH{private: true, release: goodRelease()}, dialer(f, &dials)); err == nil {
		t.Fatal("expected an arch error")
	}
	if len(f.calls) != 1 {
		t.Errorf("only the probe should have run, got %d calls", len(f.calls))
	}
}

func TestRemoveUsesTokenOnStdin(t *testing.T) {
	f := &fakeRemote{probe: "arch=x86_64\nbinary=1\nconfigured=1\nunit=u.service\nactive=1\n"}
	dials := 0
	rep, err := Remove(opts(), &fakeGH{private: true}, dialer(f, &dials))
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Removed || rep.LocalOnly {
		t.Errorf("report = %+v", rep)
	}
	var sawInput bool
	for _, c := range f.calls {
		if c.kind == "input" {
			sawInput = true
			if c.stdin != secretToken+"\n" || !strings.Contains(c.cmd, "config.sh remove") || strings.Contains(c.cmd, "--local") {
				t.Errorf("bad remove call: %+v", c)
			}
		}
	}
	if !sawInput {
		t.Error("config.sh remove did not get a token")
	}
	if !f.ran("svc.sh stop") || !f.ran("svc.sh uninstall") || !f.ran("rm -rf") {
		t.Error("stop, uninstall and delete must all run")
	}
}

func TestRemoveFallsBackToLocalWhenNoToken(t *testing.T) {
	f := &fakeRemote{probe: "arch=x86_64\nbinary=1\nconfigured=1\nunit=u.service\nactive=1\n"}
	dials := 0
	rep, err := Remove(opts(), &fakeGH{private: true, tokenErr: errors.New("403")}, dialer(f, &dials))
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Removed || !rep.LocalOnly {
		t.Errorf("report = %+v", rep)
	}
	if !f.ran("remove --local") {
		t.Error("expected config.sh remove --local")
	}
}

func TestRemoveWithNothingInstalledSaysSo(t *testing.T) {
	f := &fakeRemote{probe: "arch=x86_64\nbinary=0\nconfigured=0\nunit=\nactive=0\n"}
	dials := 0
	if _, err := Remove(opts(), &fakeGH{private: true}, dialer(f, &dials)); err == nil {
		t.Fatal("expected an error")
	}
	if len(f.calls) != 1 {
		t.Errorf("nothing but the probe should run, got %d calls", len(f.calls))
	}
}

func TestRedact(t *testing.T) {
	if got := Redact("bad token abc123 and abc123", "abc123"); got != "bad token [redacted] and [redacted]" {
		t.Errorf("got %q", got)
	}
	if got := Redact("keep this", ""); got != "keep this" {
		t.Errorf("empty secret changed the text: %q", got)
	}
}

func TestUnitGuardRejectsBadNamesInAShell(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	for unit, ok := range map[string]bool{"actions.runner.a-b.web.service": true, "x@y.service": true, "": false, "a b": false, "a;rm": false, "$(id)": false, "a/b": false} {
		cmd := exec.Command("sh", "-c", "unit="+sshx.Quote(unit)+"\n"+unitGuard)
		err := cmd.Run()
		if (err == nil) != ok {
			t.Errorf("unit %q: accepted=%v, want %v", unit, err == nil, ok)
		}
	}
}

func TestInstallIsRepairedWhenOnlyTheMarkerIsMissing(t *testing.T) {
	// The probe reports binary=0 (no marker) although config.sh is there: a rerun must install again.
	f := &fakeRemote{probe: "arch=x86_64\nbinary=0\nconfigured=0\nunit=\nactive=0\n"}
	dials := 0
	rep, err := Install(opts(), &fakeGH{private: true, release: goodRelease()}, dialer(f, &dials))
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Installed || !f.ran("installdependencies.sh") {
		t.Errorf("an unmarked install was not redone: %+v", rep)
	}
}

func TestRemoveFallsBackToLocalWhenTokenRemoveFails(t *testing.T) {
	f := &fakeRemote{
		probe:   "arch=x86_64\nbinary=1\nconfigured=1\nunit=u.service\nactive=1\n",
		inputEr: fmt.Errorf("remote: exit status 1: bad token %s", secretToken),
	}
	dials := 0
	rep, err := Remove(opts(), &fakeGH{private: true}, dialer(f, &dials))
	if err != nil {
		t.Fatalf("a failed unregister must not strand the runner: %v", err)
	}
	if !rep.Removed || !rep.LocalOnly {
		t.Errorf("report = %+v", rep)
	}
	if !f.ran("remove --local") || !f.ran("rm -rf '/opt") {
		t.Error("expected remove --local and the directory delete")
	}
}

func TestRemoveRedactsTokenFromStderr(t *testing.T) {
	// The unregister failure is swallowed, but a failure after the token was fetched must still
	// be scrubbed: make the local fallback echo the token.
	f := &fakeRemote{
		probe:   "arch=x86_64\nbinary=1\nconfigured=1\nunit=u.service\nactive=1\n",
		inputEr: errors.New("boom"),
		runEr: func(cmd string) error {
			if strings.Contains(cmd, "remove --local") {
				return fmt.Errorf("exit 1: token was %s", secretToken)
			}
			return nil
		},
	}
	dials := 0
	rep, err := Remove(opts(), &fakeGH{private: true}, dialer(f, &dials))
	if err == nil {
		t.Fatal("expected an error")
	}
	b, _ := json.Marshal(map[string]any{"data": rep, "error": err.Error()})
	if strings.Contains(err.Error(), secretToken) || strings.Contains(string(b), secretToken) {
		t.Errorf("token leaked: %v / %s", err, b)
	}
}

func TestExplicitLimitsAreAppliedToAReadyRunner(t *testing.T) {
	ready := "arch=x86_64\nbinary=1\nconfigured=1\nunit=u.service\nactive=1\n"
	o := opts()
	o.ApplyLimits = true
	f := &fakeRemote{probe: ready}
	dials := 0
	rep, err := Install(o, &fakeGH{private: true}, dialer(f, &dials))
	if err != nil {
		t.Fatal(err)
	}
	if !rep.AlreadyReady || !rep.LimitsApplied || !f.ran("MemoryMax=3G") {
		t.Errorf("report = %+v", rep)
	}
	if f.ran("curl") || f.ran("config.sh") {
		t.Error("only the drop-in may change")
	}

	f = &fakeRemote{probe: ready}
	rep, err = Install(opts(), &fakeGH{private: true}, dialer(f, &dials))
	if err != nil || rep.LimitsApplied || len(f.calls) != 1 {
		t.Errorf("without the flag nothing may change: %+v %v calls=%d", rep, err, len(f.calls))
	}

	o2 := o
	f = &fakeRemote{probe: "arch=x86_64\nbinary=1\nconfigured=1\nunit=u.service\nactive=0\n"}
	rep, err = Install(o2, &fakeGH{private: true}, dialer(f, &dials))
	if err != nil || !rep.LimitsApplied || !f.ran("MemoryMax=3G") {
		t.Errorf("a stopped runner with explicit limits: %+v %v", rep, err)
	}
}
