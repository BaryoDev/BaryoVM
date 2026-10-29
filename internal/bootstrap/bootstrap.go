// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package bootstrap prepares a fresh VM to run containers: it installs the named
// runtime (Docker Engine or Podman) idempotently over SSH. It detects the package
// manager (apt-get, dnf, yum, apk, pacman) instead of assuming one, and starts
// the service through systemd only where systemd is running.
package bootstrap

import (
	"fmt"
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/ctr"
	"github.com/BaryoDev/BaryoVM/internal/sshx"
)

// The script is assembled from fixed pieces. The runtime picks between them, it is never
// interpolated into a command, so nothing from a flag or a file reaches the remote shell as code.
const scriptHead = `set -e
`

// detectPM sets PM to the first package manager found, or stops with a message that names the
// ones this script knows. apt-get comes first: a Debian derivative can also carry a stray dnf.
const detectPM = `if command -v apt-get >/dev/null 2>&1; then PM=apt-get
elif command -v dnf >/dev/null 2>&1; then PM=dnf
elif command -v yum >/dev/null 2>&1; then PM=yum
elif command -v apk >/dev/null 2>&1; then PM=apk
elif command -v pacman >/dev/null 2>&1; then PM=pacman
else
  echo "no supported package manager found (apt-get, dnf, yum, apk, pacman): install %[1]s by hand" >&2
  exit 1
fi
echo "PACKAGE_MANAGER=$PM"
`

// dockerInstall keeps get.docker.com where it is supported (apt-get, dnf, yum), which is what the
// script did before it looked at the package manager. Alpine and Arch ship docker in the
// distribution repository.
const dockerInstall = `case "$PM" in
  apt-get|dnf|yum)
    curl -fsSL https://get.docker.com | sudo -n sh
    ;;
  apk)
    sudo -n apk add --no-cache docker
    ;;
  pacman)
    sudo -n pacman -Sy --noconfirm --needed docker
    ;;
esac
sudo -n usermod -aG docker "$(whoami)" || true
`

// podmanInstall is the distribution package on every supported package manager.
const podmanInstall = `case "$PM" in
  apt-get)
    sudo -n apt-get update
    sudo -n apt-get install -y podman
    ;;
  dnf|yum)
    sudo -n "$PM" install -y podman
    ;;
  apk)
    sudo -n apk add --no-cache podman
    ;;
  pacman)
    sudo -n pacman -Sy --noconfirm --needed podman
    ;;
esac
`

// The service step runs only where systemd is running (/run/systemd/system exists). A host without
// it, Alpine on OpenRC or a container, says so in the output and carries on, rather than failing
// the call and having the failure swallowed. A real systemctl failure on a systemd host now stops
// the script.
const dockerSystemd = `if [ -d /run/systemd/system ]; then
  sudo -n systemctl enable --now docker
else
  echo "NO_SYSTEMD: skipped systemctl, start the docker service with the host's own init"
fi
`

// Podman has no daemon, so there is nothing to start for it. The socket is what a compose provider
// talks to, so it is enabled where systemd exists.
//
// A systemd host whose podman package ships no podman.socket unit gets a note and carries on: the
// unit check is the only thing guarded, so a real failure to enable an existing unit still stops
// the script.
const podmanSystemd = `if [ -d /run/systemd/system ]; then
  if systemctl cat podman.socket >/dev/null 2>&1; then
    sudo -n systemctl enable --now podman.socket
  else
    echo "NO_SOCKET: this host has no podman.socket unit, skipped enabling it; a compose provider needs the socket started by hand"
  fi
else
  echo "NO_SYSTEMD: skipped systemctl, podman needs no daemon but a compose provider needs its socket started by the host's own init"
fi
`

// Script is the script EnsureRuntime runs on the VM for runtime ("" is docker). A runtime outside
// the fixed set is refused here, before any script text exists.
func Script(runtime string) (string, error) {
	if err := ctr.Valid(runtime); err != nil {
		return "", err
	}
	bin := ctr.Bin(runtime)
	install, systemd, version := dockerInstall, dockerSystemd, "docker --version 2>/dev/null || sudo -n docker --version\n"
	if bin == ctr.Podman {
		install, systemd, version = podmanInstall, podmanSystemd, "podman --version\n"
	}
	var b strings.Builder
	b.WriteString(scriptHead)
	fmt.Fprintf(&b, "if command -v %s >/dev/null 2>&1; then\n  echo \"ALREADY_INSTALLED\"\nelse\n", bin)
	b.WriteString(indent(fmt.Sprintf(detectPM, bin)))
	b.WriteString(indent(install))
	b.WriteString("fi\n")
	b.WriteString(systemd)
	b.WriteString(version)
	return b.String(), nil
}

func indent(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = "  " + l
	}
	return strings.Join(lines, "\n") + "\n"
}

// Report describes the outcome of EnsureRuntime.
type Report struct {
	Runtime       string `json:"runtime"`
	DockerVersion string `json:"dockerVersion"` // the runtime's version line, whichever runtime it is
	AlreadyReady  bool   `json:"alreadyReady"`
	// PackageManager is the one the install used. Empty when the runtime was already there.
	PackageManager string `json:"packageManager,omitempty"`
	// NoSystemd is true when the host has no systemd and the service step was skipped.
	NoSystemd bool `json:"noSystemd,omitempty"`
	// Notes are lines the script printed that the reader should see, such as the skipped systemctl.
	Notes []string `json:"notes,omitempty"`
}

// EnsureRuntime installs the runtime if it is missing and returns the running version.
func EnsureRuntime(c sshx.Runner, runtime string) (Report, error) {
	script, err := Script(runtime)
	if err != nil {
		return Report{}, err
	}
	out, err := c.Run(script)
	if err != nil {
		return Report{}, err
	}
	return parse(ctr.Bin(runtime), out), nil
}

// EnsureDocker is EnsureRuntime for docker.
func EnsureDocker(c sshx.Runner) (Report, error) { return EnsureRuntime(c, ctr.Docker) }

func parse(runtime, out string) Report {
	r := Report{Runtime: runtime, AlreadyReady: strings.Contains(out, "ALREADY_INSTALLED")}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for _, l := range lines {
		l = strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(l, "PACKAGE_MANAGER="):
			r.PackageManager = strings.TrimPrefix(l, "PACKAGE_MANAGER=")
		case strings.HasPrefix(l, "NO_SOCKET:"):
			r.Notes = append(r.Notes, l)
		case strings.HasPrefix(l, "NO_SYSTEMD:"):
			r.NoSystemd = true
			r.Notes = append(r.Notes, l)
		}
	}
	r.DockerVersion = strings.TrimSpace(lines[len(lines)-1])
	return r
}
