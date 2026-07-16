// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package bootstrap prepares a fresh VM to run containers: it installs Docker
// Engine idempotently over SSH. Works on Debian/Ubuntu and RHEL/Oracle Linux
// via the official get.docker.com convenience script.
package bootstrap

import (
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/sshx"
)

const dockerScript = `set -e
if command -v docker >/dev/null 2>&1; then
  echo "ALREADY_INSTALLED"
else
  curl -fsSL https://get.docker.com | sudo sh
  sudo usermod -aG docker "$(whoami)" || true
fi
sudo systemctl enable --now docker >/dev/null 2>&1 || true
docker --version 2>/dev/null || sudo docker --version
`

// Report describes the outcome of EnsureDocker.
type Report struct {
	DockerVersion string `json:"dockerVersion"`
	AlreadyReady  bool   `json:"alreadyReady"`
}

// EnsureDocker installs Docker if it is missing and returns the running version.
func EnsureDocker(c *sshx.Client) (Report, error) {
	out, err := c.Run(dockerScript)
	if err != nil {
		return Report{}, err
	}
	r := Report{AlreadyReady: strings.Contains(out, "ALREADY_INSTALLED")}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	r.DockerVersion = strings.TrimSpace(lines[len(lines)-1])
	return r, nil
}
