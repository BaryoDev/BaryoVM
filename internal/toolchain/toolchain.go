// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package toolchain keeps BaryoVM non-dev-friendly: when a local command-line
// tool it needs is missing, it downloads and installs it (showing a spinner)
// instead of erroring out. Cloud APIs use in-process Go SDKs, so this is only
// for genuinely-external binaries BaryoVM shells out to (e.g. docker for local
// image builds).
package toolchain

import (
	"fmt"
	"os/exec"
	"runtime"

	"github.com/BaryoDev/BaryoVM/internal/ui"
)

// installer installs one tool on the current OS. It returns an error only if
// the install genuinely fails; a nil error means the tool should now be on PATH.
type installer func() error

// registry maps a tool name to how we install it when it is absent.
var registry = map[string]installer{
	"aws":    installAws,
	"docker": installDocker,
}

// EnsureCLI guarantees a tool is available, installing it if missing, and
// returns its resolved path. It never errors just because the tool was absent —
// only if the install itself fails or the tool is unknown.
func EnsureCLI(name string) (string, error) {
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	inst, ok := registry[name]
	if !ok {
		return "", fmt.Errorf("%s is not installed and BaryoVM has no installer for it", name)
	}
	err := ui.Step(fmt.Sprintf("%s not found — downloading and installing it", name), inst)
	if err != nil {
		return "", fmt.Errorf("install %s: %w", name, err)
	}
	p, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("%s installed but not found on PATH; open a new shell and retry", name)
	}
	return p, nil
}

// run executes a local command, surfacing combined output on failure.
func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %v: %w: %s", name, args, err, string(out))
	}
	return nil
}

func hasBrew() bool { _, err := exec.LookPath("brew"); return err == nil }

func installAws() error {
	switch runtime.GOOS {
	case "darwin":
		if hasBrew() {
			return run("brew", "install", "awscli")
		}
		return run("/bin/sh", "-c",
			`curl -fsSL "https://awscli.amazonaws.com/AWSCLIV2.pkg" -o /tmp/AWSCLIV2.pkg && sudo installer -pkg /tmp/AWSCLIV2.pkg -target /`)
	case "linux":
		return run("/bin/sh", "-c",
			`curl -fsSL "https://awscli.amazonaws.com/awscli-exe-linux-$(uname -m).zip" -o /tmp/awscliv2.zip && `+
				`cd /tmp && unzip -oq awscliv2.zip && sudo ./aws/install --update`)
	default:
		return fmt.Errorf("automatic aws install is not supported on %s", runtime.GOOS)
	}
}

func installDocker() error {
	switch runtime.GOOS {
	case "darwin":
		if hasBrew() {
			return run("brew", "install", "--cask", "docker")
		}
		return fmt.Errorf("install Docker Desktop from https://docker.com/products/docker-desktop")
	case "linux":
		return run("/bin/sh", "-c", `curl -fsSL https://get.docker.com | sudo sh`)
	default:
		return fmt.Errorf("automatic docker install is not supported on %s", runtime.GOOS)
	}
}
