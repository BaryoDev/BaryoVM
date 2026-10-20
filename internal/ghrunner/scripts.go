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

// Dir is where one repo's runner lives.
func (o Options) Dir() string {
	return Root + "/" + strings.ReplaceAll(o.Repo, "/", "-")
}

// BuildProbe reports the state of the VM as key=value lines: arch, whether the runner is unpacked,
// whether it is registered (.runner), the unit name from .service, and whether that unit is active.
//
// sudo -n test, not test: the runner dir belongs to another user and a stricter umask on the VM
// would otherwise read as "not installed" and trigger a second download.
func BuildProbe(o Options) string {
	d := sshx.Quote(o.Dir())
	return fmt.Sprintf(`echo "arch=$(uname -m)"
if sudo -n test -x %[1]s/config.sh; then echo binary=1; else echo binary=0; fi
if sudo -n test -f %[1]s/.runner; then echo configured=1; else echo configured=0; fi
unit=$(sudo -n cat %[1]s/.service 2>/dev/null || true)
echo "unit=$unit"
if [ -n "$unit" ] && systemctl is-active --quiet "$unit"; then echo active=1; else echo active=0; fi
`, d)
}

// BuildInstall unpacks the runner for arch. It carries no token: nothing in it is secret, so the
// whole script can be echoed in an error.
//
// The digest check sits between download and tar on purpose: a tarball that fails it is never
// extracted, and a failed check stops the script (set -e) before anything is written to the dir.
func BuildInstall(o Options, arch, sha256 string) string {
	d := sshx.Quote(o.Dir())
	url := fmt.Sprintf("https://github.com/actions/runner/releases/download/v%[1]s/actions-runner-linux-%[2]s-%[1]s.tar.gz", o.Version, arch)
	return fmt.Sprintf(`set -eu
getent group docker >/dev/null || { echo "no docker group on this VM: run baryovm vm bootstrap first" >&2; exit 1; }
id %[1]s >/dev/null 2>&1 || sudo -n useradd --create-home --shell /bin/bash %[1]s
sudo -n usermod -aG docker %[1]s
tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT
curl -fsSL -o "$tmp" %[2]s
printf '%%s  %%s\n' %[3]s "$tmp" | sha256sum -c - >/dev/null
sudo -n mkdir -p %[4]s
sudo -n tar -xzf "$tmp" -C %[4]s
sudo -n chown -R %[1]s:%[1]s %[4]s
if command -v getenforce >/dev/null 2>&1; then sudo -n restorecon -R %[4]s; fi
cd %[4]s && sudo -n ./bin/installdependencies.sh
`, sshx.Quote(RunnerUser), sshx.Quote(url), sshx.Quote(sha256), d)
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
	return "sudo -n -u " + RunnerUser + " sh -c " + sshx.Quote(inner)
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
	return fmt.Sprintf(`set -eu
if ! sudo -n test -f %[1]s/.service; then ( cd %[1]s && sudo -n ./svc.sh install %[2]s ); fi
unit=$(sudo -n cat %[1]s/.service)
dropin="/etc/systemd/system/${unit}.d"
sudo -n mkdir -p "$dropin"
printf '[Service]\nMemoryMax=%%s\nCPUQuota=%%s\n' %[3]s %[4]s | sudo -n tee "$dropin/%[5]s" >/dev/null
if command -v getenforce >/dev/null 2>&1; then sudo -n restorecon -R %[1]s; fi
sudo -n systemctl daemon-reload
sudo -n systemctl restart "$unit"
sleep 2
systemctl is-active --quiet "$unit" || { echo "unit $unit is not active after restart: check journalctl -u $unit" >&2; exit 1; }
`, d, RunnerUser, sshx.Quote(o.MemoryMax), sshx.Quote(o.CPUQuota), limitsDropIn)
}

// BuildStart is the short path for a runner that is configured and has its unit but is stopped.
func BuildStart(o Options) string {
	d := sshx.Quote(o.Dir())
	return fmt.Sprintf(`set -eu
unit=$(sudo -n cat %[1]s/.service)
sudo -n systemctl start "$unit"
sleep 2
systemctl is-active --quiet "$unit" || { echo "unit $unit is not active after start: check journalctl -u $unit" >&2; exit 1; }
`, d)
}

// BuildRemoveStop stops and uninstalls the service. Both steps are allowed to fail, since a rerun
// after a crash may find the unit already gone; the drop-in goes first because it is named after
// the unit and svc.sh uninstall removes the unit file that .service points at.
func BuildRemoveStop(o Options) string {
	d := sshx.Quote(o.Dir())
	return fmt.Sprintf(`if sudo -n test -f %[1]s/.service; then
  unit=$(sudo -n cat %[1]s/.service)
  ( cd %[1]s && sudo -n ./svc.sh stop ) || true
  ( cd %[1]s && sudo -n ./svc.sh uninstall ) || true
  sudo -n rm -rf "/etc/systemd/system/${unit}.d"
  sudo -n systemctl daemon-reload || true
fi
`, d)
}

// BuildRemoveConfig unregisters the runner. With local it only drops the local config, which is
// what is left when no remove token could be fetched.
func BuildRemoveConfig(o Options, local bool) string {
	if local {
		inner := fmt.Sprintf("cd %s && exec ./config.sh remove --local", sshx.Quote(o.Dir()))
		return "sudo -n -u " + RunnerUser + " sh -c " + sshx.Quote(inner)
	}
	inner := fmt.Sprintf(`cd %s && IFS= read -r T && export ACTIONS_RUNNER_INPUT_TOKEN="$T" && exec ./config.sh remove`, sshx.Quote(o.Dir()))
	return "sudo -n -u " + RunnerUser + " sh -c " + sshx.Quote(inner)
}

// BuildRemoveDelete deletes the runner dir. Dir is built from a validated repo, never empty, and
// always under Root, so this cannot be pointed at anything else.
func BuildRemoveDelete(o Options) string {
	return "sudo -n rm -rf " + sshx.Quote(o.Dir())
}
