// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package toolchain keeps BaryoVM non-dev-friendly: when a local command-line
// tool it needs is missing, it downloads and installs it (showing a spinner)
// instead of erroring out. Cloud APIs use in-process Go SDKs, so this is only
// for genuinely-external binaries BaryoVM shells out to: docker for local image
// builds, rsync for the release sync.
package toolchain

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"

	"github.com/BaryoDev/BaryoVM/internal/ui"
)

// installer installs one tool on the current OS. It returns an error only if
// the install genuinely fails; a nil error means the tool should now be on PATH.
type installer func() error

// registry maps a tool name to how we install it when it is absent.
var registry = map[string]installer{
	"docker": installDocker,
	"rsync":  installRsync,
}

// EnsureCLI guarantees a tool is available, installing it if missing, and
// returns its resolved path. It never errors just because the tool was absent,
// only if the install itself fails or the tool is unknown.
func EnsureCLI(name string) (string, error) {
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	inst, ok := registry[name]
	if !ok {
		return "", fmt.Errorf("%s is not installed and BaryoVM has no installer for it", name)
	}
	err := ui.Step(fmt.Sprintf("%s not found: downloading and installing it", name), inst)
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

func installDocker() error {
	switch runtime.GOOS {
	case "darwin":
		if hasBrew() {
			return run("brew", "install", "--cask", "docker")
		}
		return fmt.Errorf("install Docker Desktop from https://docker.com/products/docker-desktop")
	case "linux":
		// Fetch, then run the script through rootRun rather than piping into `sudo sh`.
		// The pipe bypassed the one place that knows about -n and about already being
		// root, so it hung on a host wanting a sudo password (with no TTY to answer it
		// under -o json) and failed needlessly in a root container with no sudo
		// installed, two functions away from an installer that handles both.
		f, err := os.CreateTemp("", "get-docker-*.sh")
		if err != nil {
			return err
		}
		defer os.Remove(f.Name())
		out, err := exec.Command("curl", "-fsSL", "https://get.docker.com").Output()
		if err != nil {
			f.Close()
			return fmt.Errorf("fetching the docker install script: %w", err)
		}
		if _, err := f.Write(out); err != nil {
			f.Close()
			return err
		}
		f.Close()
		return rootRun("/bin/sh", f.Name())
	default:
		return fmt.Errorf("automatic docker install is not supported on %s", runtime.GOOS)
	}
}

func installRsync() error {
	switch runtime.GOOS {
	case "darwin":
		if hasBrew() {
			return run("brew", "install", "rsync")
		}
		return fmt.Errorf("install Homebrew from https://brew.sh, then run `brew install rsync`")
	case "linux":
		return installLinuxPackage("rsync")
	default:
		return fmt.Errorf("automatic rsync install is not supported on %s: run BaryoVM inside WSL, or install the rsync package in Git Bash", runtime.GOOS)
	}
}

// installLinuxPackage installs one package with whichever package manager the
// distro has. apt-get gets an `update` first, because install fails on an image
// whose index was never fetched.
func installLinuxPackage(pkg string) error {
	managers := []struct {
		bin  string
		runs [][]string
	}{
		{"apt-get", [][]string{{"update"}, {"install", "-y", pkg}}},
		{"dnf", [][]string{{"install", "-y", pkg}}},
		{"yum", [][]string{{"install", "-y", pkg}}},
		{"zypper", [][]string{{"--non-interactive", "install", pkg}}},
		{"pacman", [][]string{{"-Sy", "--noconfirm", pkg}}},
		{"apk", [][]string{{"add", "--no-cache", pkg}}},
	}
	for _, m := range managers {
		if _, err := exec.LookPath(m.bin); err != nil {
			continue
		}
		for _, args := range m.runs {
			if err := rootRun(m.bin, args...); err != nil {
				return err
			}
		}
		return nil
	}
	return fmt.Errorf("no supported package manager found (apt-get, dnf, yum, zypper, pacman, apk): install %s yourself", pkg)
}

// rootRun runs a package manager as root, skipping sudo when we already are it:
// a container image that has no sudo installed is a normal place to land here.
func rootRun(name string, args ...string) error {
	if os.Geteuid() == 0 {
		return run(name, args...)
	}
	// sudo -n, per CLAUDE.md: under -o json there is no TTY to answer a password
	// prompt, so a bare sudo hangs where -n fails immediately and legibly.
	return run("sudo", append([]string{"-n", name}, args...)...)
}
