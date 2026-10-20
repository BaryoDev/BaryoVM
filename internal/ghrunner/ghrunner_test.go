package ghrunner

import (
	"encoding/json"
	"errors"
	"fmt"
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
	closed  bool
}

func (f *fakeRemote) Run(cmd string) (string, error) {
	f.calls = append(f.calls, call{kind: "run", cmd: cmd})
	if strings.Contains(cmd, `echo "arch=`) {
		return f.probe, nil
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
	sum := strings.Index(s, "sha256sum -c")
	tar := strings.Index(s, "tar -xzf")
	if sum < 0 || tar < 0 || sum > tar {
		t.Fatalf("digest check must come before tar (sha256sum at %d, tar at %d):\n%s", sum, tar, s)
	}
	if !strings.Contains(s, "set -eu") {
		t.Error("a failed digest check must stop the script")
	}
}

func TestServiceScriptRestoreconBeforeStartAndCarriesLimits(t *testing.T) {
	o := validOpts(t)
	s := BuildService(o)
	rc := strings.Index(s, "restorecon")
	st := strings.Index(s, "systemctl restart")
	if rc < 0 || st < 0 || rc > st {
		t.Fatalf("restorecon must run before the unit starts:\n%s", s)
	}
	if !strings.Contains(s, "getenforce") {
		t.Error("restorecon must be skipped when getenforce is absent")
	}
	if !strings.Contains(s, sshx.Quote("3G")) || !strings.Contains(s, sshx.Quote("150%")) {
		t.Errorf("drop-in does not carry the limits:\n%s", s)
	}
	if !strings.Contains(s, "MemoryMax=") || !strings.Contains(s, "CPUQuota=") {
		t.Errorf("drop-in is missing MemoryMax or CPUQuota:\n%s", s)
	}
	want := "cd " + sshx.Quote(o.Dir()) + " && sudo -n ./svc.sh install github-runner"
	if !strings.Contains(s, want) {
		t.Errorf("svc.sh must run as %q:\n%s", want, s)
	}
	if !strings.Contains(s, "cat "+sshx.Quote(o.Dir())+"/.service") {
		t.Error("the unit name must be read from .service")
	}
	if strings.Index(s, "daemon-reload") > st {
		t.Error("daemon-reload must come before the restart")
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
	if o.Dir() != "/opt/github-runner/acme-app" {
		t.Errorf("dir = %s", o.Dir())
	}
}

func TestScriptsQuoteValuesEvenIfValidationWereBypassed(t *testing.T) {
	o := Options{VM: "w", Repo: "a/b'; id #", Name: "n$(id)", Version: "1.2.3';id", MemoryMax: "4G'", CPUQuota: "1%';id"}
	d := sshx.Quote(o.Dir())
	if !strings.Contains(BuildInstall(o, "x64", testDigest), d) {
		t.Error("install: dir not quoted")
	}
	if !strings.Contains(BuildService(o), d) || !strings.Contains(BuildService(o), sshx.Quote(o.MemoryMax)) {
		t.Error("service: values not quoted")
	}
	if !strings.Contains(BuildInstall(o, "x64", testDigest), sshx.Quote("https://github.com/actions/runner/releases/download/v1.2.3';id/actions-runner-linux-x64-1.2.3';id.tar.gz")) {
		t.Error("install: url not quoted")
	}
	if !strings.Contains(BuildRemoveDelete(o), d) {
		t.Error("delete: dir not quoted")
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
