// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package release runs a config-driven release: rsync local source to a VM,
// build images there, then compose up. Everything is described by a JSON
// manifest so the pipeline is generic, with no app-specific logic in the CLI.
package release

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

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
	Sync       []string `json:"sync"`              // paths under localRoot to rsync; a trailing "/" syncs CONTENTS
	Exclude    []string `json:"exclude,omitempty"` // rsync excludes (bin, obj, node_modules, .next, .git…)
	Builds     []Build  `json:"builds"`            // images to build after syncing

	// Sudo runs the REMOTE rsync as root. Needed when remoteRoot is a root-owned path such as
	// a webroot; without it rsync fails on the first write and the deploy looks like an SSH
	// problem rather than a permissions one.
	Sudo bool `json:"sudo,omitempty"`

	// NoCompose skips `docker compose up` at the end. A static site is files served by the
	// host's own web server, so there is no compose file to bring up and the default behaviour
	// would fail after the sync had already succeeded.
	NoCompose bool `json:"noCompose,omitempty"`

	// PostDeploy runs on the VM after syncing and building. This is where a static site
	// restores its SELinux context and reloads the web server. Commands run in order and the
	// release stops at the first failure.
	PostDeploy []string `json:"postDeploy,omitempty"`

	// Verify runs on the VM after PostDeploy and decides whether the release worked. Every command
	// must exit zero.
	//
	// Without this, and without a healthUrl on the stack, a release reports success having asked
	// the running site nothing. That is how a deploy can serve the wrong page and still be called
	// done: the sync landed, compose came up, and no one looked. A repository that already has a
	// check script should point at it here rather than have this reimplement one.
	Verify []string `json:"verify,omitempty"`

	// VerifyAttempts and VerifyDelaySeconds bound how long to wait for a stack to come back before
	// calling the release failed. A service that restarts is normal; one that never answers is not.
	VerifyAttempts     int `json:"verifyAttempts,omitempty"`
	VerifyDelaySeconds int `json:"verifyDelaySeconds,omitempty"`
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
// with --delete scoped to that subdir, so the compose dir (with its .env) is never
// in the sync list, so secrets can't be wiped.
func (m *Manifest) RsyncCmd(sub, user, host, key string) *exec.Cmd {
	ssh := fmt.Sprintf("ssh -i %s -o StrictHostKeyChecking=accept-new -o BatchMode=yes", key)
	args := []string{"-az", "--delete", "-e", ssh}
	if m.Sudo {
		// The remote end, not the local one. rsync runs a copy of itself on the far side and
		// that is the process which needs to write into a root-owned destination.
		//
		// -n so sudo never prompts. Without it, a host lacking passwordless sudo writes a
		// password prompt into rsync's data channel, which corrupts the protocol stream: the
		// transfer hangs or dies with an opaque protocol error instead of saying what is wrong.
		// With -n it fails immediately and legibly.
		args = append(args, "--rsync-path=sudo -n rsync")
	}
	for _, e := range m.Exclude {
		args = append(args, "--exclude", e)
	}
	// rsync's own trailing-slash rule, preserved rather than invented: "out" copies the
	// directory INTO the destination (dest/out/...), "out/" copies its CONTENTS to the
	// destination root. filepath.Join strips the slash, so it has to be re-applied.
	//
	// This matters more than it reads. A static site wants its build output AT the webroot,
	// so it needs the trailing form; getting it wrong combines with --delete to empty the
	// webroot and put the site one directory down, which is a live outage rather than a typo.
	src := filepath.Join(expand(m.LocalRoot), sub)
	if strings.HasSuffix(sub, "/") {
		src += "/"
	}
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
	// simple insertion sort; arg sets are tiny
	for i := 1; i < len(ks); i++ {
		for j := i; j > 0 && ks[j-1] > ks[j]; j-- {
			ks[j-1], ks[j] = ks[j], ks[j-1]
		}
	}
	return ks
}

// VerifyPlan is what a release should run to satisfy itself that the deploy worked, and how long to
// keep trying.
type VerifyPlan struct {
	// Commands run on the VM, in order. Empty means nothing was configured.
	Commands []string
	// Attempts is how many times the whole plan may be retried before the release is failed.
	Attempts int
	// Delay is how long to wait between attempts.
	Delay time.Duration
}

// Verification builds the plan for a release. The manifest's own verify commands win; otherwise a
// stack healthUrl becomes a probe.
//
// The probe runs on the VM rather than locally on purpose. A healthUrl is usually loopback
// (http://127.0.0.1:5005/health), which resolves to the wrong machine from a laptop: the request
// would either fail or, worse, reach something local and pass. Curl is asked for the status code
// only, with --fail so a 500 is an error rather than a body.
func (m *Manifest) Verification(healthURL string) VerifyPlan {
	p := VerifyPlan{
		Attempts: m.VerifyAttempts,
		Delay:    time.Duration(m.VerifyDelaySeconds) * time.Second,
	}
	if p.Attempts <= 0 {
		p.Attempts = 5
	}
	if p.Delay <= 0 {
		p.Delay = 3 * time.Second
	}

	if len(m.Verify) > 0 {
		p.Commands = append(p.Commands, m.Verify...)
		return p
	}

	if strings.TrimSpace(healthURL) != "" {
		p.Commands = append(p.Commands,
			fmt.Sprintf("curl --fail --silent --show-error --max-time 10 -o /dev/null %s",
				sshx.Quote(healthURL)))
	}
	return p
}
