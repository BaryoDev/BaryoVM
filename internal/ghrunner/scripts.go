// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package ghrunner

import (
	"fmt"
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/sshx"
)

// RunnerUser owns the runner and everything it executes. It is in the docker group, which is
// root-equivalent on the VM (see SECURITY.md), so the runner is only ever installed for a private
// repo.
const RunnerUser = "github-runner"

// Root is the parent of every runner directory.
const Root = "/opt/github-runner"

// limitsDropIn is the file name inside the unit's .d directory.
const limitsDropIn = "baryovm-limits.conf"

// installedMarker is the last thing BuildInstall writes. The probe trusts it rather than
// config.sh, because config.sh exists as soon as tar finishes and installdependencies.sh runs
// after that: a failure there would otherwise leave a runner no rerun ever repairs.
const installedMarker = ".baryovm-installed"

// sudo prefixes a command this code built itself, through the one helper that writes the prefix.
func sudo(cmd string) string { return sshx.Sudo(true, cmd) }

// asRunner runs inner as the runner user. The shell is sh -c so the script can cd and read stdin
// inside the user's own process, which is how the token stays off every command line.
func asRunner(inner string) string {
	return sudo("-u " + RunnerUser + " sh -c " + sshx.Quote(inner))
}

// unitGuard refuses a unit name that is empty or holds anything but systemd-safe characters.
// .service is written by root-run svc.sh, but the name then feeds paths under /etc/systemd and
// root commands, so it is checked rather than trusted.
const unitGuard = `case "$unit" in ""|*[!A-Za-z0-9_.@-]*) echo "bad unit name in .service: remove the runner and install it again" >&2; exit 1;; esac`

// Dir is where one repo's runner lives. owner__repo, lowercased (GitHub names are case
// insensitive): a single dash could not tell a-b/c from a/b-c.
func (o Options) Dir() string {
	return Root + "/" + strings.ToLower(strings.ReplaceAll(o.Repo, "/", "__"))
}

// BuildProbe reports the state of the VM as key=value lines: arch, whether the runner is fully
// unpacked, whether it is registered (.runner), the unit name from .service, and whether that unit
// is active.
//
// sudo -n test, not test: the runner dir belongs to another user and a stricter umask on the VM
// would otherwise read as "not installed" and trigger a second download.
func BuildProbe(o Options) string {
	d := sshx.Quote(o.Dir())
	test := sudo("test")
	return fmt.Sprintf(`echo "arch=$(uname -m)"
if %[2]s -f %[1]s/%[3]s; then echo binary=1; else echo binary=0; fi
if %[2]s -f %[1]s/.runner; then echo configured=1; else echo configured=0; fi
unit=$(%[4]s %[1]s/.service 2>/dev/null || true)
echo "unit=$unit"
if [ -n "$unit" ] && systemctl is-active --quiet "$unit"; then echo active=1; else echo active=0; fi
`, d, test, installedMarker, sudo("cat"))
}

// BuildInstall unpacks the runner for arch. It carries no token: nothing in it is secret, so the
// whole script can be echoed in an error.
//
// The digest check sits between download and tar on purpose: a tarball that fails it is never
// extracted, and a failed check stops the script (set -e) before anything is written to the dir.
func BuildInstall(o Options, arch, sha256 string) string {
	d := sshx.Quote(o.Dir())
	u := sshx.Quote(RunnerUser)
	url := fmt.Sprintf("https://github.com/actions/runner/releases/download/v%[1]s/actions-runner-linux-%[2]s-%[1]s.tar.gz", o.Version, arch)
	var b strings.Builder
	line := func(s string) { b.WriteString(s + "\n") }
	line("set -eu")
	line(`getent group docker >/dev/null || { echo "no docker group on this VM: run baryovm vm bootstrap first" >&2; exit 1; }`)
	line("id " + u + " >/dev/null 2>&1 || " + sudo("useradd --create-home --shell /bin/bash "+u))
	line(sudo("usermod -aG docker " + u))
	line("tmp=$(mktemp)")
	line(`trap 'rm -f "$tmp"' EXIT`)
	line(`curl -fsSL --connect-timeout 15 --max-time 600 --retry 2 -o "$tmp" ` + sshx.Quote(url))
	line(`printf '%s  %s\n' ` + sshx.Quote(sha256) + ` "$tmp" | sha256sum -c - >/dev/null`)
	// Explicit modes: sudo combines the caller's umask with its own, so mkdir under a 077 umask
	// leaves Root 0700 and github-runner cannot reach its own directory.
	line(sudo("install -d -m 0755 " + sshx.Quote(Root)))
	line(sudo("install -d -m 0755 -o " + u + " -g " + u + " " + d))
	line(sudo(`tar -xzf "$tmp" -C ` + d))
	line(sudo("chown -R " + u + ":" + u + " " + d))
	line("if command -v getenforce >/dev/null 2>&1; then " + sudo("restorecon -R "+d) + "; fi")
	line("cd " + d + " && " + sudo("./bin/installdependencies.sh"))
	line(sudo("touch " + d + "/" + installedMarker))
	return b.String()
}

// BuildConfig registers the runner. The token is not in the string: the script reads it from stdin
// inside the runner user's shell and exports it there, so it is never on any command line. sudo
// passes its own stdin to the child, and nothing else in this one-command session reads it.
//
// ACTIONS_RUNNER_INPUT_TOKEN is how config.sh takes --token from the environment; passing the token
// as a flag would put it in argv.
func BuildConfig(o Options, arch string) string {
	args := []string{
		"--unattended",
		"--url " + sshx.Quote("https://github.com/"+o.Repo),
		"--name " + sshx.Quote(o.Name),
		"--replace",
	}
	if l := o.ExtraLabels(arch); len(l) > 0 {
		args = append(args, "--labels "+sshx.Quote(strings.Join(l, ",")))
	}
	inner := fmt.Sprintf(`cd %s && IFS= read -r T && export ACTIONS_RUNNER_INPUT_TOKEN="$T" && exec ./config.sh %s`,
		sshx.Quote(o.Dir()), strings.Join(args, " "))
	return asRunner(inner)
}

// BuildService installs the unit if there is none, applies the limits as a drop-in, restarts and
// checks the unit is active.
//
// The unit name comes out of .service, never built by hand: svc.sh derives it from the repo and
// runner name and the scheme has changed between runner releases. `svc.sh install` fails with
// "exists" on a second run, so it only runs when .service is absent.
//
// restorecon comes before the restart because svc.sh only labels the unit file; the runner dir
// needs it too or systemd fails with 203/EXEC on SELinux hosts.
func BuildService(o Options) string {
	d := sshx.Quote(o.Dir())
	dropin := fmt.Sprintf("[Service]\nMemoryMax=%s\nCPUQuota=%s\n", o.MemoryMax, o.CPUQuota)
	return fmt.Sprintf(`set -eu
if ! %[1]s -f %[2]s/.service; then ( cd %[2]s && %[3]s ); fi
unit=$(%[4]s %[2]s/.service)
%[5]s
dropin="/etc/systemd/system/${unit}.d"
%[6]s "$dropin"
printf '%%s' %[7]s | %[8]s "$dropin/%[9]s" >/dev/null
if command -v getenforce >/dev/null 2>&1; then %[10]s; fi
%[11]s
%[12]s
sleep 2
systemctl is-active --quiet "$unit" || { echo "unit $unit is not active after restart: check journalctl -u $unit" >&2; exit 1; }
`,
		sudo("test"), d, sudo("./svc.sh install "+RunnerUser), sudo("cat"), unitGuard,
		sudo("mkdir -p"), sshx.Quote(dropin), sudo("tee"), limitsDropIn,
		sudo("restorecon -R "+d), sudo("systemctl daemon-reload"), sudo(`systemctl restart "$unit"`))
}

// BuildStart is the short path for a runner that is configured and has its unit but is stopped.
func BuildStart(o Options) string {
	d := sshx.Quote(o.Dir())
	return fmt.Sprintf(`set -eu
unit=$(%[2]s %[1]s/.service)
%[3]s
%[4]s
sleep 2
systemctl is-active --quiet "$unit" || { echo "unit $unit is not active after start: check journalctl -u $unit" >&2; exit 1; }
`, d, sudo("cat"), unitGuard, sudo(`systemctl start "$unit"`))
}

// BuildRemoveStop stops and uninstalls the service. Both steps are allowed to fail, since a rerun
// after a crash may find the unit already gone; the drop-in goes first because it is named after
// the unit and svc.sh uninstall removes the unit file that .service points at.
func BuildRemoveStop(o Options) string {
	d := sshx.Quote(o.Dir())
	return fmt.Sprintf(`if %[2]s -f %[1]s/.service; then
  unit=$(%[3]s %[1]s/.service)
  %[4]s
  ( cd %[1]s && %[5]s ) || true
  ( cd %[1]s && %[6]s ) || true
  %[7]s "/etc/systemd/system/${unit}.d"
  %[8]s || true
fi
`, d, sudo("test"), sudo("cat"), unitGuard,
		sudo("./svc.sh stop"), sudo("./svc.sh uninstall"), sudo("rm -rf"), sudo("systemctl daemon-reload"))
}

// BuildRemoveConfig unregisters the runner. With local it only drops the local config, which is
// what is left when no remove token could be fetched.
func BuildRemoveConfig(o Options, local bool) string {
	if local {
		return asRunner(fmt.Sprintf("cd %s && exec ./config.sh remove --local", sshx.Quote(o.Dir())))
	}
	return asRunner(fmt.Sprintf(`cd %s && IFS= read -r T && export ACTIONS_RUNNER_INPUT_TOKEN="$T" && exec ./config.sh remove`, sshx.Quote(o.Dir())))
}

// BuildRemoveDelete deletes the runner dir. Dir is built from a validated repo, never empty, and
// always under Root, so this cannot be pointed at anything else.
func BuildRemoveDelete(o Options) string {
	return sudo("rm -rf " + sshx.Quote(o.Dir()))
}
