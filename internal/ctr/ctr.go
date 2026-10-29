// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package ctr names the container runtime a stack runs on. It is a fixed set, not a free string:
// the runtime is interpolated into remote commands as a bare word, so the only safe value is one
// from this list. Every engine package that builds a `docker ...` command reads the binary from
// here.
package ctr

import "fmt"

const (
	Docker = "docker"
	Podman = "podman"

	// Unsupported is what Bin returns for a runtime outside the set.
	Unsupported = "unsupported-container-runtime"
)

// Valid refuses anything outside the fixed set. The empty string is the default (docker) and is
// accepted.
func Valid(r string) error {
	switch r {
	case "", Docker, Podman:
		return nil
	}
	return fmt.Errorf("runtime %q is not supported: use %q or %q", r, Docker, Podman)
}

// Bin returns the binary name for a runtime. An empty runtime is docker. A value outside the set
// becomes a fixed word that is no command on any host, so a hand-edited fleet.json or remote.json
// fails loudly on the VM (command not found) and never runs docker by surprise or reaches a shell.
// Callers still validate at the edge with Valid; this is the backstop.
func Bin(r string) string {
	switch r {
	case "", Docker:
		return Docker
	case Podman:
		return Podman
	}
	return Unsupported
}

// Compose returns the compose invocation for a runtime: `docker compose` or `podman compose`.
func Compose(r string) string { return Bin(r) + " compose" }
