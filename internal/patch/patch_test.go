// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package patch

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/BaryoDev/BaryoVM/internal/sshx"
)

// fakeRunner answers by matching a substring of the command, first match wins. A handler may
// be a func so a test can change its answer between calls.
type fakeRunner struct {
	cmds     []string
	handlers []handler
}

type handler struct {
	match string
	fn    func(n int) (string, error)
	n     int
}

func (f *fakeRunner) on(match string, fn func(n int) (string, error)) *fakeRunner {
	f.handlers = append(f.handlers, handler{match: match, fn: fn})
	return f
}

func (f *fakeRunner) onOut(match, out string) *fakeRunner {
	return f.on(match, func(int) (string, error) { return out, nil })
}

func (f *fakeRunner) Run(cmd string) (string, error) {
	f.cmds = append(f.cmds, cmd)
	for i := range f.handlers {
		h := &f.handlers[i]
		if strings.Contains(cmd, h.match) {
			h.n++
			return h.fn(h.n)
		}
	}
	return "", nil
}

func (f *fakeRunner) ran(sub string) bool {
	for _, c := range f.cmds {
		if strings.Contains(c, sub) {
			return true
		}
	}
	return false
}

func (f *fakeRunner) index(sub string) int {
	for i, c := range f.cmds {
		if strings.Contains(c, sub) {
			return i
		}
	}
	return -1
}

const snapBefore = `KERNEL|5.14.0-1
INSTALLED|5.14.0-1
BOOTID|boot-a
PM|dnf
DOCKER|1
CTR|/web|running|healthy
CTR|/db|running|healthy
CTR|/sick|running|unhealthy
CTR|/old|exited|
NGINX|1|active
NGINXT|1
`

func snap(web, db, sick, nginx string) string {
	return `KERNEL|5.14.0-1
INSTALLED|5.14.0-2
BOOTID|boot-a
PM|dnf
DOCKER|1
CTR|/web|` + web + `
CTR|/db|` + db + `
CTR|/sick|` + sick + `
CTR|/old|exited|
NGINX|1|` + nginx + `
NGINXT|1
`
}

func fast() Options {
	return Options{
		Sleep: func(time.Duration) {}, PollAttempts: 3, HealthAttempts: 3, RebootAttempts: 3,
		Unit: "u1", Now: func() time.Time { return time.Unix(1, 0) },
	}
}

const unitDone = "ActiveState=active\nSubState=exited\nResult=success\n"

// base wires the answers every run needs; a test adds or overrides after it.
func base(after string) *fakeRunner {
	f := &fakeRunner{}
	n := 0
	f.on("uname -r", func(int) (string, error) {
		n++
		if n == 1 {
			return snapBefore, nil
		}
		return after, nil
	})
	f.onOut("check-update", "PKG|kernel.x86_64|5.14.0-2\nPKG|curl.x86_64|8.0\n")
	f.onOut("systemctl show", unitDone)
	return f
}

func TestDryRunListsPendingAndChangesNothing(t *testing.T) {
	f := base(snap("running|healthy", "running|healthy", "running|unhealthy", "active"))
	f.onOut("needs-restarting", "REBOOT|yes\n")
	o := fast()
	o.DryRun = true
	rep, err := Run(f, o)
	if err != nil {
		t.Fatal(err)
	}
	if rep.PackageCount != 2 || len(rep.Pending) != 2 || rep.RebootRequired != "yes" {
		t.Fatalf("report: %+v", rep)
	}
	for _, bad := range []string{"systemd-run --unit", "reboot", "update -y"} {
		if f.ran(bad) && bad != "reboot" {
			t.Fatalf("dry run ran %q: %v", bad, f.cmds)
		}
	}
	if f.ran("systemctl reboot") {
		t.Fatal("dry run rebooted")
	}
}

func TestUpdateRunsDetachedAndIsPolled(t *testing.T) {
	f := base(snap("running|healthy", "running|healthy", "running|unhealthy", "active"))
	f.onOut("needs-restarting", "REBOOT|no\n")
	rep, err := Run(f, fast())
	if err != nil {
		t.Fatal(err)
	}
	up := f.index("systemd-run --unit='u1' --remain-after-exit dnf update -y")
	poll := f.index("systemctl show")
	if up < 0 || poll < 0 || up > poll {
		t.Fatalf("update must start before it is polled: %v", f.cmds)
	}
	if !strings.Contains(f.cmds[up], "sudo -n") {
		t.Fatalf("update ran without sudo: %s", f.cmds[up])
	}
	if rep.NewKernel != "5.14.0-2" || rep.OldKernel != "5.14.0-1" || rep.Rebooted {
		t.Fatalf("report: %+v", rep)
	}
}

func TestAptUsesAptAndEnvironment(t *testing.T) {
	c := UpdateCmd("apt", "u1")
	if !strings.Contains(c, "DEBIAN_FRONTEND=noninteractive") || !strings.Contains(c, "apt-get -y upgrade") {
		t.Fatal(c)
	}
}

func TestUnitIsQuoted(t *testing.T) {
	c := UpdateCmd("dnf", "x; rm -rf /")
	if !strings.Contains(c, "'x; rm -rf /'") {
		t.Fatal(c)
	}
}

func TestPollDeadline(t *testing.T) {
	f := base("")
	f.handlers = nil
	f.onOut("uname -r", snapBefore)
	f.onOut("check-update", "PKG|a.x86_64|1\n")
	f.onOut("systemctl show", "ActiveState=active\nSubState=running\nResult=success\n")
	_, err := Run(f, fast())
	if err == nil || !strings.Contains(err.Error(), "did not finish") {
		t.Fatalf("want deadline error, got %v", err)
	}
	var p *Problem
	if errors.As(err, &p) {
		t.Fatal("a deadline is not a stack problem")
	}
}

func TestFailedUnitReportsLog(t *testing.T) {
	f := base("")
	f.handlers = nil
	f.onOut("uname -r", snapBefore)
	f.onOut("check-update", "PKG|a.x86_64|1\n")
	f.onOut("systemctl show", "ActiveState=failed\nSubState=failed\nResult=exit-code\n")
	f.onOut("journalctl", "Error: no space left\n")
	_, err := Run(f, fast())
	if err == nil || !strings.Contains(err.Error(), "no space left") {
		t.Fatalf("got %v", err)
	}
}

func TestRebootDecisionComesFromTheHost(t *testing.T) {
	c := RebootCheckCmd()
	if !strings.Contains(c, "needs-restarting -r") || !strings.Contains(c, "/var/run/reboot-required") {
		t.Fatal(c)
	}
	for in, want := range map[string]string{"REBOOT|yes\n": "yes", "REBOOT|no\n": "no", "": "unknown", "REBOOT|x": "unknown"} {
		if got := ParseReboot(in); got != want {
			t.Errorf("ParseReboot(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNoRebootWithoutFlagOrPrompt(t *testing.T) {
	f := base(snap("running|healthy", "running|healthy", "running|unhealthy", "active"))
	f.onOut("needs-restarting", "REBOOT|yes\n")
	rep, err := Run(f, fast())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Rebooted || f.ran("systemctl reboot") {
		t.Fatal("rebooted without being asked")
	}
	if len(rep.Notes) == 0 {
		t.Fatal("expected a note that a reboot is still required")
	}
}

func TestPromptDecidesTheReboot(t *testing.T) {
	for _, answer := range []bool{false, true} {
		f := base(snap("running|healthy", "running|healthy", "running|unhealthy", "active"))
		f.onOut("needs-restarting", "REBOOT|yes\n")
		o := fast()
		asked := 0
		o.Confirm = func(Report) bool { asked++; return answer }
		o.Reconnect = func() (sshx.Runner, error) {
			g := &fakeRunner{}
			g.onOut("uname -r", snap("running|healthy", "running|healthy", "running|unhealthy", "active"))
			g.onOut("boot_id", "boot-b\n")
			return g, nil
		}
		rep, err := Run(f, o)
		if err != nil {
			t.Fatal(err)
		}
		if asked != 1 || rep.Rebooted != answer {
			t.Fatalf("answer %v: asked %d rebooted %v", answer, asked, rep.Rebooted)
		}
	}
}

func TestRebootWaitsForSSHWithBoundedRetryAndNewBootID(t *testing.T) {
	f := base(snap("running|healthy", "running|healthy", "running|unhealthy", "active"))
	f.onOut("needs-restarting", "REBOOT|yes\n")
	o := fast()
	o.Reboot = true
	tries := 0
	o.Reconnect = func() (sshx.Runner, error) {
		tries++
		switch tries {
		case 1:
			return nil, errors.New("connection refused")
		case 2: // still the old host
			g := &fakeRunner{}
			g.onOut("boot_id", "boot-a\n")
			return g, nil
		}
		g := &fakeRunner{}
		g.onOut("uname -r", snap("running|healthy", "running|healthy", "running|unhealthy", "active"))
		g.onOut("boot_id", "boot-b\n")
		return g, nil
	}
	rep, err := Run(f, o)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Rebooted || tries != 3 {
		t.Fatalf("rebooted %v after %d tries", rep.Rebooted, tries)
	}
	if !f.ran("systemctl reboot") {
		t.Fatal("no reboot command")
	}
}

func TestRebootGivesUpWhenSSHNeverReturns(t *testing.T) {
	f := base("")
	f.onOut("needs-restarting", "REBOOT|yes\n")
	o := fast()
	o.Reboot = true
	tries := 0
	o.Reconnect = func() (sshx.Runner, error) { tries++; return nil, errors.New("no route") }
	_, err := Run(f, o)
	if err == nil || !strings.Contains(err.Error(), "did not come back over SSH") || tries != 3 {
		t.Fatalf("err %v tries %d", err, tries)
	}
}

func TestContainerThatDoesNotReturnFailsAndIsNamed(t *testing.T) {
	f := base(snap("exited|", "running|healthy", "running|unhealthy", "active"))
	f.onOut("needs-restarting", "REBOOT|no\n")
	rep, err := Run(f, fast())
	var p *Problem
	if !errors.As(err, &p) {
		t.Fatalf("want a Problem, got %v", err)
	}
	if !strings.Contains(err.Error(), "web") || len(rep.FailedContainers) != 1 || rep.FailedContainers[0] != "web" {
		t.Fatalf("report %+v err %v", rep, err)
	}
}

func TestContainerUnhealthyAfterIsNamed(t *testing.T) {
	f := base(snap("running|healthy", "running|unhealthy", "running|unhealthy", "active"))
	f.onOut("needs-restarting", "REBOOT|no\n")
	rep, err := Run(f, fast())
	if err == nil || len(rep.FailedContainers) != 1 || rep.FailedContainers[0] != "db" {
		t.Fatalf("report %+v err %v", rep, err)
	}
}

func TestContainerAlreadyUnhealthyBeforeIsNotAFailure(t *testing.T) {
	f := base(snap("running|healthy", "running|healthy", "running|unhealthy", "active"))
	f.onOut("needs-restarting", "REBOOT|no\n")
	rep, err := Run(f, fast())
	if err != nil || len(rep.FailedContainers) != 0 {
		t.Fatalf("report %+v err %v", rep, err)
	}
}

func TestStoppedBeforeIsNotAFailure(t *testing.T) {
	// "old" was exited before and stays exited.
	b := ParseSnapshot(snapBefore)
	a := ParseSnapshot(snap("running|healthy", "running|healthy", "running|unhealthy", "active"))
	if p := Compare(b, a); len(p) != 0 {
		t.Fatal(p)
	}
}

func TestContainerSlowToReturnIsRetried(t *testing.T) {
	f := &fakeRunner{}
	n := 0
	f.on("uname -r", func(int) (string, error) {
		n++
		switch n {
		case 1:
			return snapBefore, nil
		case 2, 3: // mid-update snapshot, then first verify
			return snap("exited|", "running|healthy", "running|unhealthy", "active"), nil
		}
		return snap("running|healthy", "running|healthy", "running|unhealthy", "active"), nil
	})
	f.onOut("check-update", "PKG|a.x86_64|1\n")
	f.onOut("systemctl show", unitDone)
	f.onOut("needs-restarting", "REBOOT|no\n")
	if _, err := Run(f, fast()); err != nil {
		t.Fatal(err)
	}
}

func TestNginxDownFails(t *testing.T) {
	f := base(snap("running|healthy", "running|healthy", "running|unhealthy", "failed"))
	f.onOut("needs-restarting", "REBOOT|no\n")
	rep, err := Run(f, fast())
	if err == nil || !strings.Contains(err.Error(), "nginx") || rep.NginxOK {
		t.Fatalf("report %+v err %v", rep, err)
	}
}

func TestNginxConfigBrokenFails(t *testing.T) {
	b := ParseSnapshot(snapBefore)
	a := ParseSnapshot(snap("running|healthy", "running|healthy", "running|unhealthy", "active"))
	a.NginxTestOK = false
	p := Compare(b, a)
	if len(p) != 1 || !strings.Contains(p[0], "nginx -t") {
		t.Fatal(p)
	}
}

func TestNoUpdatesInstallsNothing(t *testing.T) {
	f := &fakeRunner{}
	f.onOut("uname -r", snapBefore)
	f.onOut("needs-restarting", "REBOOT|no\n")
	rep, err := Run(f, fast())
	if err != nil || rep.PackageCount != 0 || f.ran("systemd-run") {
		t.Fatalf("report %+v err %v cmds %v", rep, err, f.cmds)
	}
}

func TestSudoRequired(t *testing.T) {
	f := &fakeRunner{}
	f.on("sudo -n true", func(int) (string, error) { return "", errors.New("exit 1") })
	if _, err := Run(f, fast()); err == nil || !strings.Contains(err.Error(), "sudo") {
		t.Fatalf("got %v", err)
	}
}

func TestParseUnit(t *testing.T) {
	cases := []struct {
		in      string
		running bool
		ok      bool
	}{
		{"ActiveState=active\nSubState=running\nResult=success", true, false},
		{"ActiveState=activating\nSubState=start\nResult=success", true, false},
		{unitDone, false, true},
		{"ActiveState=failed\nSubState=failed\nResult=exit-code", false, false},
		{"ActiveState=inactive\nSubState=dead\nResult=success", false, false},
	}
	for _, c := range cases {
		r, ok, _ := ParseUnit(c.in)
		if r != c.running || ok != c.ok {
			t.Errorf("%q: running %v ok %v", c.in, r, ok)
		}
	}
}

func TestParsePending(t *testing.T) {
	got := ParsePending("PKG|curl|8.0\nnoise\nPKG|bash|5.1\n")
	if len(got) != 2 || got[0].Name != "curl" {
		t.Fatal(got)
	}
}
