// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package patch brings a VM's OS packages up to date and proves the stack came back.
//
// Run takes a snapshot first (kernel, containers with their health, nginx), runs the update as a
// detached systemd unit so a dropped SSH session cannot kill dnf halfway through a transaction,
// asks the host whether it needs a reboot, reboots only when told to, and then compares a second
// snapshot with the first.
//
// The reboot decision comes from the host: needs-restarting -r on the RHEL family and
// /var/run/reboot-required on the Debian family. A list of package names that "usually" need a
// reboot goes stale the first time a distro renames one.
//
// A container that was already unhealthy before the update is not a failure. The check is a
// regression check: it fails on what the update broke, not on what was already broken.
package patch

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BaryoDev/BaryoVM/internal/sshx"
)

// Options controls one patch run.
type Options struct {
	// DryRun lists pending updates and changes nothing.
	DryRun bool
	// Reboot reboots without asking when the host says it needs one.
	Reboot bool
	// Confirm is asked when a reboot is needed and Reboot is false. Nil means no.
	Confirm func(Report) bool
	// Reconnect opens a new session after a reboot. Required for a reboot to happen.
	Reconnect func() (sshx.Runner, error)

	// Unit is the systemd unit name for the detached update. Defaults to a timestamped name.
	Unit string
	// PollAttempts x PollDelay is the deadline for the update itself.
	PollAttempts int
	PollDelay    time.Duration
	// RebootAttempts x RebootDelay is how long to wait for SSH after a reboot.
	RebootAttempts int
	RebootDelay    time.Duration
	// HealthAttempts x HealthDelay is how long containers get to come back.
	HealthAttempts int
	HealthDelay    time.Duration

	Sleep func(time.Duration)
	Now   func() time.Time
}

func (o Options) withDefaults() Options {
	if o.PollAttempts <= 0 {
		o.PollAttempts = 360
	}
	if o.PollDelay <= 0 {
		o.PollDelay = 10 * time.Second
	}
	if o.RebootAttempts <= 0 {
		o.RebootAttempts = 60
	}
	if o.RebootDelay <= 0 {
		o.RebootDelay = 5 * time.Second
	}
	if o.HealthAttempts <= 0 {
		o.HealthAttempts = 30
	}
	if o.HealthDelay <= 0 {
		o.HealthDelay = 5 * time.Second
	}
	if o.Sleep == nil {
		o.Sleep = time.Sleep
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Unit == "" {
		o.Unit = "baryovm-patch-" + strconv.FormatInt(o.Now().Unix(), 10)
	}
	return o
}

// Pkg is one pending update.
type Pkg struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Container is one container as the snapshot saw it.
type Container struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Health string `json:"health,omitempty"`
}

// Snapshot is the host state compared before and after.
type Snapshot struct {
	Kernel          string
	InstalledKernel string
	BootID          string
	PM              string
	HasDocker       bool
	Containers      []Container
	NginxInstalled  bool
	NginxActive     string
	NginxTestOK     bool
}

// Report is the outcome of a run.
type Report struct {
	DryRun         bool   `json:"dryRun,omitempty"`
	PackageManager string `json:"packageManager"`
	Pending        []Pkg  `json:"pending,omitempty"`
	PackageCount   int    `json:"packageCount"`
	OldKernel      string `json:"oldKernel"`
	NewKernel      string `json:"newKernel"`
	// RebootRequired is "yes", "no" or "unknown", as the host reports it.
	RebootRequired string `json:"rebootRequired"`
	Rebooted       bool   `json:"rebooted"`
	// FailedContainers are containers that were running before and are not healthy now.
	FailedContainers []string `json:"failedContainers,omitempty"`
	NginxOK          bool     `json:"nginxOk"`
	Notes            []string `json:"notes,omitempty"`
}

// Problem is returned by Run when the update finished but the stack did not come back clean.
type Problem struct{ Items []string }

func (p *Problem) Error() string {
	return "the stack did not come back clean: " + strings.Join(p.Items, "; ")
}

// SnapshotCmd gathers the snapshot. It always exits 0.
func SnapshotCmd() string {
	return `echo "KERNEL|$(uname -r)"
echo "INSTALLED|$(ls /lib/modules 2>/dev/null | sort -V | tail -n1)"
echo "BOOTID|$(cat /proc/sys/kernel/random/boot_id 2>/dev/null)"
if command -v dnf >/dev/null 2>&1; then echo "PM|dnf"
elif command -v yum >/dev/null 2>&1; then echo "PM|yum"
elif command -v apt-get >/dev/null 2>&1; then echo "PM|apt"
else echo "PM|none"; fi
if command -v docker >/dev/null 2>&1 && sudo -n docker info >/dev/null 2>&1; then
  echo "DOCKER|1"
  ids=$(sudo -n docker ps -aq)
  if [ -n "$ids" ]; then
    sudo -n docker inspect --format 'CTR|{{.Name}}|{{.State.Status}}|{{if .State.Health}}{{.State.Health.Status}}{{end}}' $ids
  fi
else
  echo "DOCKER|0"
fi
if command -v nginx >/dev/null 2>&1; then
  echo "NGINX|1|$(systemctl is-active nginx 2>/dev/null)"
  if sudo -n nginx -t >/dev/null 2>&1; then echo "NGINXT|1"; else echo "NGINXT|0"; fi
else
  echo "NGINX|0|"
fi
exit 0
`
}

// ParseSnapshot reads SnapshotCmd's output.
func ParseSnapshot(out string) Snapshot {
	var s Snapshot
	for _, line := range strings.Split(out, "\n") {
		kind, rest, ok := strings.Cut(strings.TrimRight(line, "\r"), "|")
		if !ok {
			continue
		}
		switch kind {
		case "KERNEL":
			s.Kernel = rest
		case "INSTALLED":
			s.InstalledKernel = rest
		case "BOOTID":
			s.BootID = rest
		case "PM":
			s.PM = rest
		case "DOCKER":
			s.HasDocker = rest == "1"
		case "CTR":
			p := strings.SplitN(rest, "|", 3)
			if len(p) == 3 {
				s.Containers = append(s.Containers, Container{Name: strings.TrimPrefix(p[0], "/"), State: p[1], Health: p[2]})
			}
		case "NGINX":
			p := strings.SplitN(rest, "|", 2)
			s.NginxInstalled = p[0] == "1"
			if len(p) == 2 {
				s.NginxActive = p[1]
			}
		case "NGINXT":
			s.NginxTestOK = rest == "1"
		}
	}
	return s
}

// PendingCmd lists pending updates as PKG|name|version lines. It always exits 0.
func PendingCmd(pm string) string {
	if pm == "apt" {
		return `sudo -n apt-get update -qq >/dev/null 2>&1
apt-get -s upgrade 2>/dev/null | awk '/^Inst /{print "PKG|"$2"|"$3}'
exit 0
`
	}
	// check-update exits 100 when updates exist, so the exit code is ignored and the list is read.
	return "sudo -n " + pm + ` -q check-update 2>/dev/null | awk 'NF>=3 && $1 ~ /\./ {print "PKG|"$1"|"$2}'
exit 0
`
}

// ParsePending reads PendingCmd's output.
func ParsePending(out string) []Pkg {
	var pkgs []Pkg
	for _, line := range strings.Split(out, "\n") {
		p := strings.Split(strings.TrimSpace(line), "|")
		if len(p) == 3 && p[0] == "PKG" {
			pkgs = append(pkgs, Pkg{Name: p[1], Version: strings.Trim(p[2], "[(")})
		}
	}
	return pkgs
}

// UpdateCmd starts the update as a detached transient unit and returns at once.
func UpdateCmd(pm, unit string) string {
	base := "sudo -n systemd-run --unit=" + sshx.Quote(unit) + " --remain-after-exit "
	if pm == "apt" {
		return base + "--setenv=DEBIAN_FRONTEND=noninteractive sh -c " + sshx.Quote("apt-get update -qq && apt-get -y upgrade")
	}
	return base + pm + " update -y"
}

// PollCmd reads the unit's state. It always exits 0.
func PollCmd(unit string) string {
	return "systemctl show " + sshx.Quote(unit) + " -p ActiveState -p SubState -p Result 2>&1; exit 0"
}

// LogCmd returns the end of the unit's journal, for the error message.
func LogCmd(unit string) string {
	return "sudo -n journalctl -u " + sshx.Quote(unit) + " -n 15 --no-pager 2>&1; exit 0"
}

// CleanupCmd removes the finished unit.
func CleanupCmd(unit string) string {
	u := sshx.Quote(unit)
	return "sudo -n systemctl stop " + u + " >/dev/null 2>&1; sudo -n systemctl reset-failed " + u + " >/dev/null 2>&1; exit 0"
}

// RebootCheckCmd asks the host whether it needs a reboot. It prints REBOOT|yes, no or unknown.
func RebootCheckCmd() string {
	return `if command -v needs-restarting >/dev/null 2>&1; then
  sudo -n needs-restarting -r >/dev/null 2>&1; rc=$?
  if [ $rc -eq 1 ]; then echo "REBOOT|yes"; elif [ $rc -eq 0 ]; then echo "REBOOT|no"; else echo "REBOOT|unknown"; fi
elif [ -f /var/run/reboot-required ]; then echo "REBOOT|yes"
elif command -v apt-get >/dev/null 2>&1; then echo "REBOOT|no"
else echo "REBOOT|unknown"; fi
exit 0
`
}

// ParseReboot reads RebootCheckCmd's output.
func ParseReboot(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "REBOOT|"); ok && (v == "yes" || v == "no") {
			return v
		}
	}
	return "unknown"
}

// RebootCmd schedules the reboot two seconds out so the SSH call returns cleanly.
func RebootCmd() string { return "sudo -n systemd-run --on-active=2s systemctl reboot" }

// ParseUnit reports whether the unit is still running, and whether it ended well.
func ParseUnit(out string) (running, ok bool, state string) {
	kv := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if k, v, found := strings.Cut(strings.TrimSpace(line), "="); found {
			kv[k] = v
		}
	}
	state = kv["ActiveState"] + "/" + kv["SubState"]
	switch kv["ActiveState"] {
	case "activating", "reloading":
		return true, false, state
	case "active":
		if kv["SubState"] == "running" || kv["SubState"] == "start" {
			return true, false, state
		}
		return false, kv["Result"] == "success", state
	}
	return false, false, state
}

// Run patches the VM. On a clean run it returns the report and nil. When the update ran but the
// stack did not come back clean it returns the report and a *Problem, so the caller can still
// print the report and exit non-zero.
func Run(r sshx.Runner, o Options) (Report, error) {
	o = o.withDefaults()
	rep := Report{DryRun: o.DryRun}

	if _, err := r.Run("sudo -n true"); err != nil {
		return rep, errors.New("passwordless sudo is required: this user needs `sudo -n` to work on the VM")
	}
	out, err := r.Run(SnapshotCmd())
	if err != nil {
		return rep, err
	}
	before := ParseSnapshot(out)
	rep.PackageManager = before.PM
	rep.OldKernel = before.Kernel
	rep.NewKernel = before.Kernel
	if before.PM == "none" || before.PM == "" {
		return rep, errors.New("no supported package manager found: vm patch needs dnf, yum or apt-get on the VM")
	}

	out, err = r.Run(PendingCmd(before.PM))
	if err != nil {
		return rep, err
	}
	rep.Pending = ParsePending(out)
	rep.PackageCount = len(rep.Pending)

	if o.DryRun {
		out, err = r.Run(RebootCheckCmd())
		if err != nil {
			return rep, err
		}
		rep.RebootRequired = ParseReboot(out)
		rep.NewKernel = before.InstalledKernel
		rep.Notes = append(rep.Notes, "whether a reboot follows is decided by the host after the install; reboot is currently required: "+rep.RebootRequired)
		return rep, nil
	}
	if rep.PackageCount == 0 {
		out, err = r.Run(RebootCheckCmd())
		if err != nil {
			return rep, err
		}
		rep.RebootRequired = ParseReboot(out)
		rep.NewKernel = before.InstalledKernel
		rep.NginxOK = true
		rep.Notes = append(rep.Notes, "no updates pending: nothing was installed")
		if rep.RebootRequired != "yes" {
			return rep, nil
		}
	} else if err := runUpdate(r, before.PM, o); err != nil {
		return rep, err
	}

	out, err = r.Run(RebootCheckCmd())
	if err != nil {
		return rep, err
	}
	rep.RebootRequired = ParseReboot(out)
	mid := ParseSnapshot(mustRun(r, SnapshotCmd()))
	rep.NewKernel = firstNonEmpty(mid.InstalledKernel, before.Kernel)

	if rep.RebootRequired == "yes" {
		if o.Reboot || (o.Confirm != nil && o.Confirm(rep)) {
			r2, err := reboot(r, before.BootID, o)
			if err != nil {
				return rep, err
			}
			r = r2
			rep.Rebooted = true
		} else {
			rep.Notes = append(rep.Notes, "a reboot is required and was not done: run vm patch --reboot or reboot the VM yourself")
		}
	}

	after, problems := verify(r, before, o)
	rep.NewKernel = firstNonEmpty(after.Kernel, rep.NewKernel)
	if !rep.Rebooted {
		rep.NewKernel = firstNonEmpty(after.InstalledKernel, rep.NewKernel)
	}
	rep.NginxOK = !before.NginxInstalled || nginxOK(before, after)
	rep.FailedContainers = failedContainers(before, after)
	if len(problems) > 0 {
		return rep, &Problem{Items: problems}
	}
	return rep, nil
}

func mustRun(r sshx.Runner, cmd string) string {
	out, _ := r.Run(cmd)
	return out
}

func runUpdate(r sshx.Runner, pm string, o Options) error {
	if _, err := r.Run(UpdateCmd(pm, o.Unit)); err != nil {
		return fmt.Errorf("could not start the update: %w", err)
	}
	for i := 0; i < o.PollAttempts; i++ {
		out, err := r.Run(PollCmd(o.Unit))
		if err == nil {
			running, ok, state := ParseUnit(out)
			if !running {
				if !ok {
					logs, _ := r.Run(LogCmd(o.Unit))
					_, _ = r.Run(CleanupCmd(o.Unit))
					return fmt.Errorf("the update failed (unit %s, %s): last log lines: %s", o.Unit, state, strings.TrimSpace(logs))
				}
				_, _ = r.Run(CleanupCmd(o.Unit))
				return nil
			}
		}
		// A poll error is a dropped connection or a busy host. The unit keeps running, so keep polling.
		o.Sleep(o.PollDelay)
	}
	return fmt.Errorf("the update did not finish in %s: it is still running as unit %s, check `journalctl -u %s` on the VM",
		time.Duration(o.PollAttempts)*o.PollDelay, o.Unit, o.Unit)
}

func reboot(r sshx.Runner, oldBoot string, o Options) (sshx.Runner, error) {
	if o.Reconnect == nil {
		return r, errors.New("cannot reboot: no way to reconnect afterwards")
	}
	if _, err := r.Run(RebootCmd()); err != nil {
		return r, fmt.Errorf("could not schedule the reboot: %w", err)
	}
	for i := 0; i < o.RebootAttempts; i++ {
		o.Sleep(o.RebootDelay)
		r2, err := o.Reconnect()
		if err != nil {
			continue
		}
		out, err := r2.Run("cat /proc/sys/kernel/random/boot_id")
		if err == nil && strings.TrimSpace(out) != "" && strings.TrimSpace(out) != oldBoot {
			return r2, nil
		}
		// Reached the old host before it went down, or the new one is not ready: try again.
		if c, ok := r2.(io.Closer); ok {
			_ = c.Close()
		}
	}
	return r, fmt.Errorf("the VM did not come back over SSH within %s: check it from the provider console",
		time.Duration(o.RebootAttempts)*o.RebootDelay)
}

// verify retries until the stack matches the snapshot or the attempts run out.
func verify(r sshx.Runner, before Snapshot, o Options) (Snapshot, []string) {
	var after Snapshot
	var problems []string
	for i := 0; i < o.HealthAttempts; i++ {
		out, err := r.Run(SnapshotCmd())
		if err == nil {
			after = ParseSnapshot(out)
			problems = Compare(before, after)
			if len(problems) == 0 {
				return after, nil
			}
		} else {
			problems = []string{"could not read the host state after the update: " + err.Error()}
		}
		o.Sleep(o.HealthDelay)
	}
	return after, problems
}

func healthy(c Container) bool {
	return c.State == "running" && (c.Health == "" || c.Health == "healthy")
}

func failedContainers(before, after Snapshot) []string {
	now := map[string]Container{}
	for _, c := range after.Containers {
		now[c.Name] = c
	}
	var bad []string
	for _, b := range before.Containers {
		if b.State != "running" {
			continue
		}
		a, ok := now[b.Name]
		switch {
		case !ok, a.State != "running":
			bad = append(bad, b.Name)
		case b.Health == "unhealthy":
			// Already unhealthy before the update: not a regression.
		case !healthy(a):
			bad = append(bad, b.Name)
		}
	}
	sort.Strings(bad)
	return bad
}

func nginxOK(before, after Snapshot) bool {
	if !before.NginxInstalled {
		return true
	}
	if before.NginxActive == "active" && after.NginxActive != "active" {
		return false
	}
	return after.NginxTestOK || !before.NginxTestOK
}

// Compare lists what the update broke. Empty means the stack matches the snapshot.
func Compare(before, after Snapshot) []string {
	var problems []string
	for _, n := range failedContainers(before, after) {
		problems = append(problems, "container "+n+" did not come back healthy")
	}
	if before.NginxInstalled && !nginxOK(before, after) {
		switch {
		case before.NginxActive == "active" && after.NginxActive != "active":
			problems = append(problems, "nginx is "+firstNonEmpty(after.NginxActive, "not running")+" (it was active)")
		default:
			problems = append(problems, "nginx -t fails (it passed before)")
		}
	}
	return problems
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
