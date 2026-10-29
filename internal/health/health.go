// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package health answers the questions an owner should ask a machine every month: how full is the
// disk, how many security updates are waiting, does it need a reboot, which containers are
// restart-looping, and will it patch itself.
//
// It is read only. Every command here reads state and writes nothing on the host, so it is safe to
// run against a VM you do not want to disturb. (dnf check-update may refresh its own metadata cache;
// that is the package manager's cache, not host configuration.)
//
// Each check has its own command builder and its own parser, and the checks are independent: a
// check that cannot be answered (no docker, no sudo, an unknown package manager) is reported as
// "unknown" or "skip" with the reason, and the others still run. The reason this is a package of
// pure functions is the same as everywhere else in this repo: the verdicts can be tested without a
// host.
//
// The thresholds are in Options and are deliberately blunt. Any pending security update is a fail,
// because the point of a monthly look is that nobody else is going to say so.
package health

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/sshx"
)

// Verdicts. Skip means the check does not apply to this host (no Docker, say) and never counts
// against it.
const (
	OK      = "ok"
	Warn    = "warn"
	Fail    = "fail"
	Unknown = "unknown"
	Skip    = "skip"
)

// Check names, stable because the portfolio view (#27) and scripts key on them.
const (
	CheckDiskRoot   = "disk-root"
	CheckDiskDocker = "disk-docker"
	CheckUpdates    = "updates"
	CheckReboot     = "reboot"
	CheckContainers = "containers"
	CheckUnattended = "unattended-updates"
)

// Options holds the thresholds. The zero Options takes the defaults; once any field is set the
// struct is used as given, so a count of 0 turns that warning off.
type Options struct {
	// DiskWarn and DiskFail are percent used.
	DiskWarn int `json:"diskWarn"`
	DiskFail int `json:"diskFail"`
	// OtherUpdatesWarn is how many non-security updates make the updates check a warning.
	OtherUpdatesWarn int `json:"maxUpdates"`
	// RestartWarn is the restart count at which a running container is called out.
	RestartWarn int `json:"maxRestarts"`
}

// Defaults are the thresholds used when no flag changes them.
func Defaults() Options {
	return Options{DiskWarn: 80, DiskFail: 90, OtherUpdatesWarn: 25, RestartWarn: 5}
}

func (o Options) withDefaults() Options {
	if o == (Options{}) {
		return Defaults()
	}
	return o
}

// Validate refuses thresholds that cannot mean anything: percentages outside 1 to 100, a warn level
// that is not below the fail level, or a negative count.
func (o Options) Validate() error {
	if o.DiskWarn < 1 || o.DiskWarn > 100 || o.DiskFail < 1 || o.DiskFail > 100 {
		return fmt.Errorf("disk thresholds are percentages from 1 to 100: got --disk-warn %d and --disk-fail %d", o.DiskWarn, o.DiskFail)
	}
	if o.DiskWarn >= o.DiskFail {
		return fmt.Errorf("--disk-warn (%d) must be below --disk-fail (%d)", o.DiskWarn, o.DiskFail)
	}
	if o.OtherUpdatesWarn < 0 {
		return fmt.Errorf("--max-updates cannot be negative: got %d (0 turns the warning off)", o.OtherUpdatesWarn)
	}
	if o.RestartWarn < 0 {
		return fmt.Errorf("--max-restarts cannot be negative: got %d (0 turns the warning off)", o.RestartWarn)
	}
	return nil
}

// Check is one line of the report.
type Check struct {
	Name    string `json:"name"`
	Verdict string `json:"verdict"`
	Detail  string `json:"detail"`
	// Items are the specifics behind the verdict, such as the containers at fault.
	Items []string `json:"items,omitempty"`
	// Metrics are the numbers, for a consumer that does not want to parse Detail.
	Metrics map[string]int `json:"metrics,omitempty"`
}

// Report is the outcome for one VM.
type Report struct {
	VM             string  `json:"vm"`
	Verdict        string  `json:"verdict"`
	PackageManager string  `json:"packageManager,omitempty"`
	Checks         []Check `json:"checks"`
	// Error is set when the VM could not be checked at all, such as an unreachable host.
	Error string `json:"error,omitempty"`
	// Thresholds are the limits the verdicts were judged against.
	Thresholds Options `json:"thresholds"`
}

// Failed reports whether anything in the report is a fail.
func (r Report) Failed() bool { return r.Verdict == Fail }

// Worst folds verdicts into one. Fail beats warn beats unknown beats ok; skip is ignored.
func Worst(verdicts ...string) string {
	rank := map[string]int{OK: 1, Unknown: 2, Warn: 3, Fail: 4}
	worst := ""
	for _, v := range verdicts {
		if rank[v] > rank[worst] {
			worst = v
		}
	}
	if worst == "" {
		return OK
	}
	return worst
}

// Unreachable is the report for a VM that could not be checked.
func Unreachable(vm string, err error) Report {
	return Report{VM: vm, Verdict: Fail, Error: err.Error(), Checks: []Check{}}
}

// Skipped is the report for a VM this command has no form for.
func Skipped(vm, why string) Report {
	return Report{VM: vm, Verdict: Skip, Error: why, Checks: []Check{}}
}

// ---- command builders. Each prints tagged lines and always exits 0. ----

// PMCmd prints PM|dnf, PM|yum, PM|apt or PM|none.
func PMCmd() string {
	return `if command -v dnf >/dev/null 2>&1; then echo "PM|dnf"
elif command -v yum >/dev/null 2>&1; then echo "PM|yum"
elif command -v apt-get >/dev/null 2>&1; then echo "PM|apt"
else echo "PM|none"; fi
exit 0
`
}

// ParsePM reads PMCmd's output. Anything unrecognised is "none".
func ParsePM(out string) string {
	for _, line := range lines(out) {
		if v, ok := strings.CutPrefix(line, "PM|"); ok && (v == "dnf" || v == "yum" || v == "apt") {
			return v
		}
	}
	return "none"
}

// DiskCmd prints DISK|mount|sizeKB|usedKB|percent for the filesystem holding path.
func DiskCmd(path string) string {
	return "df -P -k " + sshx.Quote(path) + ` 2>/dev/null | awk 'NR==2{gsub("%","",$5); print "DISK|"$6"|"$2"|"$3"|"$5}'; exit 0`
}

// Disk is one filesystem.
type Disk struct {
	Mount   string
	SizeKB  int64
	UsedKB  int64
	Percent int
}

// ParseDisk reads DiskCmd's output.
func ParseDisk(out string) (Disk, bool) {
	for _, line := range lines(out) {
		p := strings.Split(line, "|")
		if len(p) == 5 && p[0] == "DISK" {
			size, e1 := strconv.ParseInt(p[2], 10, 64)
			used, e2 := strconv.ParseInt(p[3], 10, 64)
			pct, e3 := strconv.Atoi(p[4])
			if e1 != nil || e2 != nil || e3 != nil {
				return Disk{}, false
			}
			return Disk{Mount: p[1], SizeKB: size, UsedKB: used, Percent: pct}, true
		}
	}
	return Disk{}, false
}

// DockerRootCmd prints DOCKERROOT|<dir>, or DOCKER|0 when docker is missing and DOCKER|denied when
// the user cannot talk to it.
func DockerRootCmd() string {
	return `if ! command -v docker >/dev/null 2>&1; then echo "DOCKER|0"
elif ! sudo -n docker info >/dev/null 2>&1; then echo "DOCKER|denied"
else echo "DOCKERROOT|$(sudo -n docker info --format ` + sshx.Quote("{{.DockerRootDir}}") + ` 2>/dev/null)"; fi
exit 0
`
}

// ParseDockerRoot reads DockerRootCmd's output. state is "ok" with a dir, "none" or "denied".
func ParseDockerRoot(out string) (dir, state string) {
	for _, line := range lines(out) {
		switch {
		case line == "DOCKER|denied":
			return "", "denied"
		case strings.HasPrefix(line, "DOCKERROOT|"):
			if d := strings.TrimPrefix(line, "DOCKERROOT|"); d != "" {
				return d, "ok"
			}
		}
	}
	return "", "none"
}

// DockerDFCmd prints DF|type|size|reclaimable for images, containers, volumes and build cache.
func DockerDFCmd() string {
	return "sudo -n docker system df --format " + sshx.Quote("DF|{{.Type}}|{{.Size}}|{{.Reclaimable}}") + " 2>/dev/null; exit 0"
}

// ParseDockerDF turns DockerDFCmd's output into "images 3GB (1GB reclaimable)" phrases.
func ParseDockerDF(out string) []string {
	var parts []string
	for _, line := range lines(out) {
		p := strings.SplitN(line, "|", 4)
		if len(p) == 4 && p[0] == "DF" {
			rec := strings.TrimSpace(strings.SplitN(p[3], "(", 2)[0])
			s := strings.ToLower(p[1]) + " " + p[2]
			if rec != "" && rec != "0B" {
				s += " (" + rec + " reclaimable)"
			}
			parts = append(parts, s)
		}
	}
	return parts
}

// UpdatesCmd counts pending updates, all and security, for the package manager pm. It prints ALL|n
// and SEC|n, or ERR|why when the package manager itself failed, so a broken sudo is not reported as
// zero updates. It returns "" for a package manager it does not know.
//
// apt reads the host's cached index and does not refresh it: refreshing needs root and changes the
// host, and this command changes nothing. The detail says so.
func UpdatesCmd(pm string) string {
	switch pm {
	case "dnf", "yum":
		q := sshx.Quote(pm)
		count := `awk 'NF>=3 && $1 ~ /\./ {n++} END{print n+0}'`
		// check-update exits 100 when updates exist, 0 when none, anything else is an error.
		return `all=$(sudo -n ` + q + ` -q check-update 2>/dev/null); rc=$?
if [ $rc -ne 0 ] && [ $rc -ne 100 ]; then echo "ERR|` + pm + ` check-update failed (is passwordless sudo set up?)"; exit 0; fi
echo "ALL|$(printf '%s\n' "$all" | ` + count + `)"
sec=$(sudo -n ` + q + ` -q check-update --security 2>/dev/null); rc=$?
if [ $rc -ne 0 ] && [ $rc -ne 100 ]; then echo "ERR|` + pm + ` check-update --security failed"; exit 0; fi
echo "SEC|$(printf '%s\n' "$sec" | ` + count + `)"
exit 0
`
	case "apt":
		return `apt list --upgradable 2>/dev/null | awk '/upgradable/ {n++; if ($0 ~ /-security/) s++} END{print "ALL|"n+0; print "SEC|"s+0}'
exit 0
`
	}
	return ""
}

// Updates is the count of pending updates.
type Updates struct {
	All, Security int
	Err           string
	Have          bool
}

// ParseUpdates reads UpdatesCmd's output.
func ParseUpdates(out string) Updates {
	var u Updates
	for _, line := range lines(out) {
		k, v, ok := strings.Cut(line, "|")
		if !ok {
			continue
		}
		switch k {
		case "ALL":
			u.All, _ = strconv.Atoi(v)
			u.Have = true
		case "SEC":
			u.Security, _ = strconv.Atoi(v)
		case "ERR":
			u.Err = v
		}
	}
	return u
}

// RebootCmd prints REBOOT|yes, no or unknown, and UPTIME|seconds.
func RebootCmd() string {
	return `if command -v needs-restarting >/dev/null 2>&1; then
  sudo -n needs-restarting -r >/dev/null 2>&1; rc=$?
  if [ $rc -eq 1 ]; then echo "REBOOT|yes"; elif [ $rc -eq 0 ]; then echo "REBOOT|no"; else echo "REBOOT|unknown"; fi
elif [ -f /var/run/reboot-required ]; then echo "REBOOT|yes"
elif command -v apt-get >/dev/null 2>&1; then echo "REBOOT|no"
else echo "REBOOT|unknown"; fi
echo "UPTIME|$(cut -d. -f1 /proc/uptime 2>/dev/null)"
exit 0
`
}

// ParseReboot reads RebootCmd's output. uptime is in seconds, -1 when not reported.
func ParseReboot(out string) (required string, uptime int64) {
	required, uptime = "unknown", -1
	for _, line := range lines(out) {
		if v, ok := strings.CutPrefix(line, "REBOOT|"); ok && (v == "yes" || v == "no") {
			required = v
		}
		if v, ok := strings.CutPrefix(line, "UPTIME|"); ok {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				uptime = n
			}
		}
	}
	return required, uptime
}

// ContainersCmd prints DOCKER|0, DOCKER|denied, or DOCKER|1 followed by one CTR line per container:
// CTR|name|status|exitCode|restartCount|restartPolicy|health.
func ContainersCmd() string {
	return `if ! command -v docker >/dev/null 2>&1; then echo "DOCKER|0"; exit 0; fi
if ! sudo -n docker info >/dev/null 2>&1; then echo "DOCKER|denied"; exit 0; fi
echo "DOCKER|1"
ids=$(sudo -n docker ps -aq)
if [ -n "$ids" ]; then
  sudo -n docker inspect --format ` + sshx.Quote("CTR|{{.Name}}|{{.State.Status}}|{{.State.ExitCode}}|{{.RestartCount}}|{{.HostConfig.RestartPolicy.Name}}|{{if .State.Health}}{{.State.Health.Status}}{{end}}") + ` $ids
fi
exit 0
`
}

// Container is one container as ContainersCmd saw it.
type Container struct {
	Name     string
	Status   string
	ExitCode int
	Restarts int
	Policy   string
	Health   string
}

// ParseContainers reads ContainersCmd's output. state is "ok", "none" (no docker) or "denied".
func ParseContainers(out string) (cs []Container, state string) {
	state = "none"
	for _, line := range lines(out) {
		switch {
		case line == "DOCKER|1":
			state = "ok"
		case line == "DOCKER|denied":
			state = "denied"
		case strings.HasPrefix(line, "CTR|"):
			p := strings.SplitN(strings.TrimPrefix(line, "CTR|"), "|", 6)
			if len(p) != 6 {
				continue
			}
			code, _ := strconv.Atoi(p[2])
			rc, _ := strconv.Atoi(p[3])
			cs = append(cs, Container{Name: strings.TrimPrefix(p[0], "/"), Status: p[1], ExitCode: code, Restarts: rc, Policy: p[4], Health: p[5]})
		}
	}
	return cs, state
}

// UnattendedCmd prints UNATTENDED|yes|how, UNATTENDED|no|how or UNATTENDED|unknown|why.
//
// On the Debian family it is the APT::Periodic::Unattended-Upgrade setting. On the RHEL family it is
// dnf-automatic-install.timer, or dnf-automatic.timer with apply_updates = yes: the plain timer
// with apply_updates off only downloads, which is not "enabled" in the sense an owner means.
func UnattendedCmd() string {
	return `if command -v apt-get >/dev/null 2>&1; then
  if grep -hs '^[[:space:]]*APT::Periodic::Unattended-Upgrade' /etc/apt/apt.conf.d/* 2>/dev/null | tail -n1 | grep -q '"1"'; then
    echo "UNATTENDED|yes|unattended-upgrades"
  else echo "UNATTENDED|no|unattended-upgrades is not enabled"; fi
elif command -v dnf >/dev/null 2>&1 || command -v yum >/dev/null 2>&1; then
  if systemctl is-enabled dnf-automatic-install.timer >/dev/null 2>&1; then
    echo "UNATTENDED|yes|dnf-automatic-install.timer"
  elif systemctl is-enabled dnf-automatic.timer >/dev/null 2>&1 && grep -qs '^[[:space:]]*apply_updates[[:space:]]*=[[:space:]]*yes' /etc/dnf/automatic.conf; then
    echo "UNATTENDED|yes|dnf-automatic.timer with apply_updates = yes"
  else echo "UNATTENDED|no|no dnf-automatic timer applies updates"; fi
else echo "UNATTENDED|unknown|no supported package manager"; fi
exit 0
`
}

// ParseUnattended reads UnattendedCmd's output. state is yes, no or unknown.
func ParseUnattended(out string) (state, how string) {
	for _, line := range lines(out) {
		if rest, ok := strings.CutPrefix(line, "UNATTENDED|"); ok {
			s, h, _ := strings.Cut(rest, "|")
			if s == "yes" || s == "no" || s == "unknown" {
				return s, h
			}
		}
	}
	return "unknown", "the host gave no answer"
}

// ---- verdicts. Pure: parsed values in, a Check out. ----

// DiskCheck judges one filesystem. extra is appended to the detail (docker's image and volume sizes).
func DiskCheck(name, label string, d Disk, extra []string, o Options) Check {
	o = o.withDefaults()
	v := OK
	switch {
	case d.Percent >= o.DiskFail:
		v = Fail
	case d.Percent >= o.DiskWarn:
		v = Warn
	}
	detail := fmt.Sprintf("%s is %d%% full (%s of %s, mounted at %s)", label, d.Percent, human(d.UsedKB), human(d.SizeKB), d.Mount)
	if len(extra) > 0 {
		detail += "; docker holds " + strings.Join(extra, ", ")
	}
	return Check{Name: name, Verdict: v, Detail: detail, Metrics: map[string]int{"usedPercent": d.Percent}}
}

// UpdatesCheck judges the pending updates. Any security update is a fail.
func UpdatesCheck(pm string, u Updates, o Options) Check {
	o = o.withDefaults()
	if u.Err != "" || !u.Have {
		why := u.Err
		if why == "" {
			why = "the host gave no answer"
		}
		return Check{Name: CheckUpdates, Verdict: Unknown, Detail: why}
	}
	other := u.All - u.Security
	if other < 0 {
		other = 0
	}
	v := OK
	switch {
	case u.Security > 0:
		v = Fail
	case o.OtherUpdatesWarn > 0 && other >= o.OtherUpdatesWarn:
		v = Warn
	}
	detail := fmt.Sprintf("%d security and %d other updates pending", u.Security, other)
	if pm == "apt" {
		detail += " (from the host's cached package index, which this read-only check does not refresh)"
	}
	return Check{Name: CheckUpdates, Verdict: v, Detail: detail, Metrics: map[string]int{"security": u.Security, "other": other}}
}

// RebootCheck judges the reboot state. A pending reboot is a warning.
func RebootCheck(required string, uptime int64) Check {
	up := ""
	m := map[string]int{}
	if uptime >= 0 {
		days := int(uptime / 86400)
		up = fmt.Sprintf(", up %d days", days)
		m["uptimeDays"] = days
	}
	switch required {
	case "yes":
		return Check{Name: CheckReboot, Verdict: Warn, Detail: "a reboot is required" + up, Metrics: m}
	case "no":
		return Check{Name: CheckReboot, Verdict: OK, Detail: "no reboot required" + up, Metrics: m}
	}
	return Check{Name: CheckReboot, Verdict: Unknown, Detail: "the host could not say whether a reboot is required" + up, Metrics: m}
}

// ContainersCheck judges the containers. A restarting, dead or non-zero-exit container and an
// unhealthy one are fails. A container that exited 0 is fine unless its restart policy says it
// should be running (always), because a one-shot job that finished is not a problem. A high restart
// count on a running container is a warning.
func ContainersCheck(cs []Container, state string, o Options) Check {
	o = o.withDefaults()
	switch state {
	case "none":
		return Check{Name: CheckContainers, Verdict: Skip, Detail: "docker is not installed"}
	case "denied":
		return Check{Name: CheckContainers, Verdict: Unknown, Detail: "could not talk to docker: this user needs `sudo -n docker` to work"}
	}
	var bad, warn []string
	for _, c := range cs {
		switch {
		case c.Status == "restarting":
			bad = append(bad, fmt.Sprintf("%s is restarting (restarted %d times)", c.Name, c.Restarts))
		case c.Status == "dead":
			bad = append(bad, c.Name+" is dead")
		case c.Status == "exited" && c.ExitCode != 0:
			bad = append(bad, fmt.Sprintf("%s exited with code %d", c.Name, c.ExitCode))
		case c.Status == "exited" && c.Policy == "always":
			bad = append(bad, c.Name+" exited but its restart policy is always")
		case c.Health == "unhealthy":
			bad = append(bad, c.Name+" is unhealthy")
		case c.Status == "running" && o.RestartWarn > 0 && c.Restarts >= o.RestartWarn:
			warn = append(warn, fmt.Sprintf("%s has restarted %d times", c.Name, c.Restarts))
		}
	}
	sort.Strings(bad)
	sort.Strings(warn)
	m := map[string]int{"total": len(cs), "problems": len(bad), "restartWarnings": len(warn)}
	switch {
	case len(bad) > 0:
		return Check{Name: CheckContainers, Verdict: Fail, Detail: fmt.Sprintf("%d of %d containers have a problem", len(bad), len(cs)), Items: append(bad, warn...), Metrics: m}
	case len(warn) > 0:
		return Check{Name: CheckContainers, Verdict: Warn, Detail: fmt.Sprintf("%d containers, %d with many restarts", len(cs), len(warn)), Items: warn, Metrics: m}
	}
	return Check{Name: CheckContainers, Verdict: OK, Detail: fmt.Sprintf("%d containers, none restarting or exited unexpectedly", len(cs)), Metrics: m}
}

// UnattendedCheck judges the automatic update setting. Off is a warning.
func UnattendedCheck(state, how string) Check {
	switch state {
	case "yes":
		return Check{Name: CheckUnattended, Verdict: OK, Detail: "automatic security updates are on (" + how + ")"}
	case "no":
		return Check{Name: CheckUnattended, Verdict: Warn, Detail: "automatic security updates are off: " + how}
	}
	return Check{Name: CheckUnattended, Verdict: Unknown, Detail: how}
}

// Run asks the VM every question and returns the report. It does not return an error: a check that
// cannot be answered is reported as unknown with the reason, and the others still run.
func Run(r sshx.Runner, vm string, o Options) Report {
	o = o.withDefaults()
	rep := Report{VM: vm, Thresholds: o}

	out, perr := r.Run(PMCmd())
	pm := ParsePM(out)
	rep.PackageManager = pm

	// Disk: / and the docker root.
	if out, err := r.Run(DiskCmd("/")); err != nil {
		rep.Checks = append(rep.Checks, Check{Name: CheckDiskRoot, Verdict: Unknown, Detail: err.Error()})
	} else if d, ok := ParseDisk(out); ok {
		rep.Checks = append(rep.Checks, DiskCheck(CheckDiskRoot, "/", d, nil, o))
	} else {
		rep.Checks = append(rep.Checks, Check{Name: CheckDiskRoot, Verdict: Unknown, Detail: "df gave no answer for /"})
	}
	rep.Checks = append(rep.Checks, dockerDisk(r, o))

	// Updates.
	switch cmd := UpdatesCmd(pm); {
	case perr != nil:
		rep.Checks = append(rep.Checks, Check{Name: CheckUpdates, Verdict: Unknown, Detail: perr.Error()})
	case cmd == "":
		rep.Checks = append(rep.Checks, Check{Name: CheckUpdates, Verdict: Unknown, Detail: "no supported package manager (dnf, yum or apt)"})
	default:
		out, err := r.Run(cmd)
		if err != nil {
			rep.Checks = append(rep.Checks, Check{Name: CheckUpdates, Verdict: Unknown, Detail: err.Error()})
		} else {
			rep.Checks = append(rep.Checks, UpdatesCheck(pm, ParseUpdates(out), o))
		}
	}

	// Reboot.
	if out, err := r.Run(RebootCmd()); err != nil {
		rep.Checks = append(rep.Checks, Check{Name: CheckReboot, Verdict: Unknown, Detail: err.Error()})
	} else {
		req, up := ParseReboot(out)
		rep.Checks = append(rep.Checks, RebootCheck(req, up))
	}

	// Containers.
	if out, err := r.Run(ContainersCmd()); err != nil {
		rep.Checks = append(rep.Checks, Check{Name: CheckContainers, Verdict: Unknown, Detail: err.Error()})
	} else {
		cs, state := ParseContainers(out)
		rep.Checks = append(rep.Checks, ContainersCheck(cs, state, o))
	}

	// Unattended updates.
	if out, err := r.Run(UnattendedCmd()); err != nil {
		rep.Checks = append(rep.Checks, Check{Name: CheckUnattended, Verdict: Unknown, Detail: err.Error()})
	} else {
		st, how := ParseUnattended(out)
		rep.Checks = append(rep.Checks, UnattendedCheck(st, how))
	}

	vs := make([]string, len(rep.Checks))
	for i, c := range rep.Checks {
		vs[i] = c.Verdict
	}
	rep.Verdict = Worst(vs...)
	return rep
}

func dockerDisk(r sshx.Runner, o Options) Check {
	out, err := r.Run(DockerRootCmd())
	if err != nil {
		return Check{Name: CheckDiskDocker, Verdict: Unknown, Detail: err.Error()}
	}
	dir, state := ParseDockerRoot(out)
	switch state {
	case "none":
		return Check{Name: CheckDiskDocker, Verdict: Skip, Detail: "docker is not installed"}
	case "denied":
		return Check{Name: CheckDiskDocker, Verdict: Unknown, Detail: "could not talk to docker: this user needs `sudo -n docker` to work"}
	}
	out, err = r.Run(DiskCmd(dir))
	if err != nil {
		return Check{Name: CheckDiskDocker, Verdict: Unknown, Detail: err.Error()}
	}
	d, ok := ParseDisk(out)
	if !ok {
		return Check{Name: CheckDiskDocker, Verdict: Unknown, Detail: "df gave no answer for " + dir}
	}
	df, _ := r.Run(DockerDFCmd())
	return DiskCheck(CheckDiskDocker, "docker root "+dir, d, ParseDockerDF(df), o)
}

func lines(out string) []string {
	var ls []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			ls = append(ls, l)
		}
	}
	return ls
}

func human(kb int64) string {
	const k = 1024.0
	v := float64(kb)
	for _, u := range []string{"KB", "MB", "GB", "TB"} {
		if v < k || u == "TB" {
			return fmt.Sprintf("%.1f%s", v, u)
		}
		v /= k
	}
	return ""
}
