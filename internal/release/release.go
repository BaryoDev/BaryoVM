// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package release runs a config-driven release: rsync local source to a VM,
// build images there, then compose up. Everything is described by a JSON
// manifest so the pipeline is generic — no app-specific logic in the CLI.
package release

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/sshx"
)

// Build describes one image to build on the VM.
type Build struct {
	Image      string            `json:"image"`             // tag, e.g. app:latest
	Dockerfile string            `json:"dockerfile"`        // relative to remoteRoot
	Context    string            `json:"context"`           // build context, relative to remoteRoot
	Args       map[string]string `json:"args,omitempty"`    // --build-arg values
	NoCache    bool              `json:"noCache,omitempty"` // pass --no-cache
}

// Manifest is the whole release recipe. Kept in the project (e.g. baryovm.release.json).
type Manifest struct {
	LocalRoot  string   `json:"localRoot"`         // local source root (~ ok)
	RemoteRoot string   `json:"remoteRoot"`        // remote dest root (absolute)
	Sync       []string `json:"sync"`              // subdirs of localRoot to rsync (e.g. api, web)
	Exclude    []string `json:"exclude,omitempty"` // rsync excludes (bin, obj, node_modules, .next, .git…)
	Builds     []Build  `json:"builds"`            // images to build after syncing
}

// Load reads and validates a manifest file.
func Load(path string) (*Manifest, error) {
	b, err := os.ReadFile(expand(path))
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if m.LocalRoot == "" || m.RemoteRoot == "" {
		return nil, fmt.Errorf("%s: localRoot and remoteRoot are required", path)
	}
	if len(m.Exclude) == 0 {
		m.Exclude = []string{"bin", "obj", "node_modules", ".next", ".git"}
	}
	return &m, nil
}

// RsyncCmd builds the local rsync command for one sync subdir. It syncs
// localRoot/<sub> → user@host:remoteRoot/ (so it lands at remoteRoot/<sub>),
// with --delete scoped to that subdir — the compose dir (with its .env) is never
// in the sync list, so secrets can't be wiped.
func (m *Manifest) RsyncCmd(sub, user, host, key string) *exec.Cmd {
	ssh := fmt.Sprintf("ssh -i %s -o StrictHostKeyChecking=accept-new -o BatchMode=yes", key)
	args := []string{"-az", "--delete", "-e", ssh}
	for _, e := range m.Exclude {
		args = append(args, "--exclude", e)
	}
	src := filepath.Join(expand(m.LocalRoot), sub)
	dst := fmt.Sprintf("%s@%s:%s/", user, host, m.RemoteRoot)
	args = append(args, src, dst)
	return exec.Command("rsync", args...)
}

// BuildCmd is the remote `docker build` command for one image.
func (m *Manifest) BuildCmd(b Build) string {
	q := sshx.Quote
	cmd := "docker build"
	if b.NoCache {
		cmd += " --no-cache"
	}
	cmd += " -f " + q(m.RemoteRoot+"/"+b.Dockerfile)
	// Deterministic arg order.
	for _, k := range sortedKeys(b.Args) {
		cmd += " --build-arg " + q(k+"="+b.Args[k])
	}
	cmd += " -t " + q(b.Image) + " " + q(m.RemoteRoot+"/"+b.Context)
	return cmd
}

func expand(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

func sortedKeys(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	// simple insertion sort — arg sets are tiny
	for i := 1; i < len(ks); i++ {
		for j := i; j > 0 && ks[j-1] > ks[j]; j-- {
			ks[j-1], ks[j] = ks[j], ks[j-1]
		}
	}
	return ks
}
