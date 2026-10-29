package health

import (
	"errors"
	"strings"
	"testing"
)

type fakeRunner struct {
	cmds     []string
	handlers []struct{ match, out string }
	errOn    string
}

func (f *fakeRunner) on(match, out string) *fakeRunner {
	f.handlers = append(f.handlers, struct{ match, out string }{match, out})
	return f
}

func (f *fakeRunner) Run(cmd string) (string, error) {
	f.cmds = append(f.cmds, cmd)
	if f.errOn != "" && strings.Contains(cmd, f.errOn) {
		return "", errors.New("boom")
	}
	for _, h := range f.handlers {
		if strings.Contains(cmd, h.match) {
			return h.out, nil
		}
	}
	return "", nil
}

func check(rep Report, name string) Check {
	for _, c := range rep.Checks {
		if c.Name == name {
			return c
		}
	}
	return Check{}
}

func TestParseDiskAndVerdict(t *testing.T) {
	d, ok := ParseDisk("DISK|/|41000000|37000000|91\n")
	if !ok || d.Percent != 91 || d.Mount != "/" {
		t.Fatalf("got %+v %v", d, ok)
	}
	if _, ok := ParseDisk("garbage"); ok {
		t.Fatal("garbage parsed")
	}
	cases := map[int]string{50: OK, 80: Warn, 89: Warn, 90: Fail, 100: Fail}
	for pct, want := range cases {
		if got := DiskCheck("disk-root", "/", Disk{Percent: pct}, nil, DefaultOptions()).Verdict; got != want {
			t.Errorf("%d%%: got %s want %s", pct, got, want)
		}
	}
}

func TestDiskCmdQuotesPath(t *testing.T) {
	cmd := DiskCmd("/var/lib/it's")
	if !strings.Contains(cmd, `'/var/lib/it'\''s'`) {
		t.Fatalf("path not quoted: %s", cmd)
	}
}

func TestDockerRootAndDF(t *testing.T) {
	if d, s := ParseDockerRoot("DOCKERROOT|/var/lib/docker\n"); d != "/var/lib/docker" || s != "ok" {
		t.Fatalf("%q %q", d, s)
	}
	if _, s := ParseDockerRoot("DOCKER|0\n"); s != "none" {
		t.Fatal(s)
	}
	if _, s := ParseDockerRoot("DOCKER|denied\n"); s != "denied" {
		t.Fatal(s)
	}
	got := ParseDockerDF("DF|Images|3.2GB|1.1GB (34%)\nDF|Local Volumes|900MB|0B (0%)\n")
	if len(got) != 2 || got[0] != "images 3.2GB (1.1GB reclaimable)" || got[1] != "local volumes 900MB" {
		t.Fatalf("%v", got)
	}
	if !strings.Contains(DockerDFCmd(), "sudo -n docker system df") {
		t.Fatal("no sudo -n")
	}
}

func TestUpdatesSplitAndVerdict(t *testing.T) {
	u := ParseUpdates("ALL|446\nSEC|31\n")
	c := UpdatesCheck("dnf", u, DefaultOptions())
	if c.Verdict != Fail || c.Metrics["security"] != 31 || c.Metrics["other"] != 415 {
		t.Fatalf("%+v", c)
	}
	if c := UpdatesCheck("dnf", ParseUpdates("ALL|30\nSEC|0\n"), DefaultOptions()); c.Verdict != Warn {
		t.Fatalf("%+v", c)
	}
	if c := UpdatesCheck("dnf", ParseUpdates("ALL|3\nSEC|0\n"), DefaultOptions()); c.Verdict != OK {
		t.Fatalf("%+v", c)
	}
	// A failed package manager is unknown, never "0 pending".
	if c := UpdatesCheck("dnf", ParseUpdates("ERR|dnf check-update failed\n"), DefaultOptions()); c.Verdict != Unknown {
		t.Fatalf("%+v", c)
	}
	if c := UpdatesCheck("dnf", ParseUpdates(""), DefaultOptions()); c.Verdict != Unknown {
		t.Fatalf("%+v", c)
	}
	if c := UpdatesCheck("apt", ParseUpdates("ALL|1\nSEC|0\n"), DefaultOptions()); !strings.Contains(c.Detail, "cached") {
		t.Fatalf("apt cache caveat missing: %s", c.Detail)
	}
}

func TestUpdatesCmdPerPlatform(t *testing.T) {
	dnf := UpdatesCmd("dnf")
	for _, want := range []string{"sudo -n 'dnf' -q check-update", "--security", "ERR|"} {
		if !strings.Contains(dnf, want) {
			t.Errorf("dnf cmd missing %q", want)
		}
	}
	if !strings.Contains(UpdatesCmd("yum"), "sudo -n 'yum'") {
		t.Error("yum cmd")
	}
	apt := UpdatesCmd("apt")
	if !strings.Contains(apt, "apt list --upgradable") || !strings.Contains(apt, "-security") {
		t.Error("apt cmd")
	}
	if strings.Contains(apt, "apt-get update") {
		t.Error("read only check must not refresh the index")
	}
	if UpdatesCmd("pacman") != "" {
		t.Error("unknown pm should give no command")
	}
}

func TestPM(t *testing.T) {
	for in, want := range map[string]string{"PM|dnf\n": "dnf", "PM|apt\n": "apt", "PM|none\n": "none", "": "none", "PM|rm\n": "none"} {
		if got := ParsePM(in); got != want {
			t.Errorf("%q: %s", in, got)
		}
	}
}

func TestRebootAndUptime(t *testing.T) {
	req, up := ParseReboot("REBOOT|yes\nUPTIME|14601600\n")
	if req != "yes" || up != 14601600 {
		t.Fatalf("%s %d", req, up)
	}
	c := RebootCheck(req, up)
	if c.Verdict != Warn || c.Metrics["uptimeDays"] != 169 || !strings.Contains(c.Detail, "169 days") {
		t.Fatalf("%+v", c)
	}
	if c := RebootCheck("no", 100); c.Verdict != OK {
		t.Fatal(c)
	}
	req, up = ParseReboot("")
	if req != "unknown" || up != -1 || RebootCheck(req, up).Verdict != Unknown {
		t.Fatal("empty output should be unknown")
	}
	cmd := RebootCmd()
	for _, want := range []string{"needs-restarting -r", "/var/run/reboot-required", "sudo -n"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("reboot cmd missing %q", want)
		}
	}
}

func TestContainers(t *testing.T) {
	out := strings.Join([]string{
		"DOCKER|1",
		"CTR|/web|running|0|0|always|healthy",
		"CTR|/loop|restarting|1|42|always|",
		"CTR|/crashed|exited|137|0|no|",
		"CTR|/migrate|exited|0|0|no|",
		"CTR|/stopped|exited|0|0|always|",
		"CTR|/sick|running|0|0|always|unhealthy",
		"CTR|/flappy|running|0|9|always|",
	}, "\n")
	cs, state := ParseContainers(out)
	if state != "ok" || len(cs) != 7 || cs[0].Name != "web" {
		t.Fatalf("%v %s", cs, state)
	}
	c := ContainersCheck(cs, state, DefaultOptions())
	if c.Verdict != Fail {
		t.Fatalf("%+v", c)
	}
	joined := strings.Join(c.Items, "\n")
	for _, want := range []string{"loop is restarting", "crashed exited with code 137", "stopped exited but its restart policy is always", "sick is unhealthy", "flappy has restarted 9 times"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %s", want, joined)
		}
	}
	if strings.Contains(joined, "migrate") || strings.Contains(joined, "web") {
		t.Errorf("one-shot exit 0 and healthy container must not be listed: %s", joined)
	}

	cs, state = ParseContainers("DOCKER|1\nCTR|/a|running|0|0|always|healthy\n")
	if c := ContainersCheck(cs, state, DefaultOptions()); c.Verdict != OK {
		t.Fatalf("%+v", c)
	}
	cs, state = ParseContainers("DOCKER|1\nCTR|/a|running|0|7|always|\n")
	if c := ContainersCheck(cs, state, DefaultOptions()); c.Verdict != Warn {
		t.Fatalf("%+v", c)
	}
	cs, state = ParseContainers("DOCKER|0\n")
	if c := ContainersCheck(cs, state, DefaultOptions()); c.Verdict != Skip {
		t.Fatalf("%+v", c)
	}
	cs, state = ParseContainers("DOCKER|denied\n")
	if c := ContainersCheck(cs, state, DefaultOptions()); c.Verdict != Unknown {
		t.Fatalf("%+v", c)
	}
	if !strings.Contains(ContainersCmd(), "sudo -n docker inspect") {
		t.Fatal("containers cmd needs sudo -n")
	}
}

func TestUnattended(t *testing.T) {
	s, how := ParseUnattended("UNATTENDED|no|no dnf-automatic timer applies updates\n")
	if s != "no" || UnattendedCheck(s, how).Verdict != Warn {
		t.Fatal(s)
	}
	s, how = ParseUnattended("UNATTENDED|yes|unattended-upgrades\n")
	if UnattendedCheck(s, how).Verdict != OK {
		t.Fatal(s)
	}
	s, how = ParseUnattended("")
	if UnattendedCheck(s, how).Verdict != Unknown {
		t.Fatal(s)
	}
	cmd := UnattendedCmd()
	for _, want := range []string{"APT::Periodic::Unattended-Upgrade", "dnf-automatic-install.timer", "dnf-automatic.timer", "apply_updates"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("missing %q", want)
		}
	}
}

func TestWorst(t *testing.T) {
	if Worst(OK, Warn, Unknown) != Warn || Worst(OK, Fail, Warn) != Fail || Worst(Skip) != OK || Worst(OK, Unknown) != Unknown {
		t.Fatal("worst")
	}
}

func rhelRunner() *fakeRunner {
	return (&fakeRunner{}).
		on("echo \"PM|dnf\"", "PM|dnf\n").
		on("df -P -k '/var/lib/docker'", "DISK|/var|100000000|60000000|60\n").
		on("df -P -k '/'", "DISK|/|41000000|37000000|91\n").
		on("DockerRootDir", "DOCKERROOT|/var/lib/docker\n").
		on("system df", "DF|Images|3GB|1GB (33%)\n").
		on("check-update", "ALL|446\nSEC|31\n").
		on("needs-restarting", "REBOOT|yes\nUPTIME|14601600\n").
		on("docker inspect", "DOCKER|1\nCTR|/loop|restarting|1|9|always|\n").
		on("UNATTENDED", "UNATTENDED|no|no dnf-automatic timer applies updates\n")
}

func TestRunWholeReport(t *testing.T) {
	r := rhelRunner()
	rep, _ := Run(r, "oracle", DefaultOptions())
	if rep.VM != "oracle" || rep.PackageManager != "dnf" || rep.Verdict != Fail || !rep.Failed() {
		t.Fatalf("%+v", rep)
	}
	want := map[string]string{
		CheckDiskRoot: Fail, CheckDiskDocker: OK, CheckUpdates: Fail,
		CheckReboot: Warn, CheckContainers: Fail, CheckUnattended: Warn,
	}
	if len(rep.Checks) != len(want) {
		t.Fatalf("checks: %+v", rep.Checks)
	}
	for name, v := range want {
		if got := check(rep, name).Verdict; got != v {
			t.Errorf("%s: got %s want %s", name, got, v)
		}
	}
	if !strings.Contains(check(rep, CheckDiskDocker).Detail, "images 3GB (1GB reclaimable)") {
		t.Errorf("docker df missing: %s", check(rep, CheckDiskDocker).Detail)
	}
}

func TestRunChangesNothing(t *testing.T) {
	r := rhelRunner()
	_, _ = Run(r, "oracle", DefaultOptions())
	for _, c := range r.cmds {
		for _, bad := range []string{"update -y", "apt-get upgrade", "dnf upgrade", "yum upgrade", "apt upgrade", "systemctl reboot", "docker rm", "docker stop", "docker restart", "docker prune", "rm ", "apt-get update", "systemd-run"} {
			if strings.Contains(c, bad) {
				t.Errorf("read only check ran %q in: %s", bad, c)
			}
		}
	}
}

func TestRunDegradesPerCheck(t *testing.T) {
	// No docker at all, and the updates command fails over the wire: other checks still answer.
	r := (&fakeRunner{errOn: "check-update"}).
		on("echo \"PM|dnf\"", "PM|dnf\n").
		on("df -P -k '/'", "DISK|/|100|10|10\n").
		on("DockerRootDir", "DOCKER|0\n").
		on("docker inspect", "DOCKER|0\n").
		on("needs-restarting", "REBOOT|no\nUPTIME|100\n").
		on("UNATTENDED", "UNATTENDED|yes|dnf-automatic-install.timer\n")
	rep, _ := Run(r, "vm", DefaultOptions())
	if check(rep, CheckDiskDocker).Verdict != Skip || check(rep, CheckContainers).Verdict != Skip {
		t.Errorf("docker checks should skip: %+v", rep.Checks)
	}
	if check(rep, CheckUpdates).Verdict != Unknown {
		t.Errorf("updates: %+v", check(rep, CheckUpdates))
	}
	if rep.Verdict != Unknown {
		t.Errorf("verdict %s", rep.Verdict)
	}
}

func TestRunNoSupportedPackageManager(t *testing.T) {
	r := (&fakeRunner{}).on("echo \"PM|dnf\"", "PM|none\n").on("df -P -k '/'", "DISK|/|100|10|10\n")
	rep, _ := Run(r, "vm", DefaultOptions())
	if c := check(rep, CheckUpdates); c.Verdict != Unknown || !strings.Contains(c.Detail, "package manager") {
		t.Fatalf("%+v", c)
	}
}

func TestUnreachableAndSkipped(t *testing.T) {
	u := Unreachable("a", errors.New("dial timeout"))
	if !u.Failed() || u.Error != "dial timeout" || u.Checks == nil {
		t.Fatalf("%+v", u)
	}
	s := Skipped("w", "windows")
	if s.Failed() || s.Verdict != Skip {
		t.Fatalf("%+v", s)
	}
}

func TestMaxZeroWarnsOnAny(t *testing.T) {
	o := DefaultOptions()
	o.OtherUpdatesWarn = 0
	if c := UpdatesCheck("dnf", ParseUpdates("ALL|1\nSEC|0\n"), o); c.Verdict != Warn {
		t.Fatalf("1 pending, max 0: %+v", c)
	}
	if c := UpdatesCheck("dnf", ParseUpdates("ALL|0\nSEC|0\n"), o); c.Verdict != OK {
		t.Fatalf("0 pending, max 0: %+v", c)
	}
	o.RestartWarn = 0
	cs, st := ParseContainers("DOCKER|1\nCTR|/a|running|0|1|always|\n")
	if c := ContainersCheck(cs, st, o); c.Verdict != Warn {
		t.Fatalf("%+v", c)
	}
	cs, st = ParseContainers("DOCKER|1\nCTR|/a|running|0|0|always|\n")
	if c := ContainersCheck(cs, st, o); c.Verdict != OK {
		t.Fatalf("%+v", c)
	}
}

func TestRunValidatesOptionsItIsGiven(t *testing.T) {
	r := &fakeRunner{}
	if _, err := Run(r, "vm", Options{}); err == nil {
		t.Fatal("the zero Options must be refused, not defaulted")
	}
	if len(r.cmds) != 0 {
		t.Fatal("sent commands with bad options")
	}
}
