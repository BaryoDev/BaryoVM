// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package compose drives `docker compose` on a VM over SSH — the day-to-day
// path for real workloads (barakoCMS, BaryoClub) that run as compose stacks
// rather than single containers.
package compose

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/sshx"
)

// Stack points at a remote compose project.
type Stack struct {
	Dir  string // project directory, e.g. /opt/barakocms
	File string // compose file name; empty uses compose's default
	// Sudo runs compose through sudo.
	//
	// Needed more often than it sounds. A project whose .env holds the database password is
	// reasonably kept root-owned and mode 600, and compose reads that file — so every command fails
	// with "permission denied" for an ordinary SSH user, even though Docker itself is reachable.
	// Loosening the file to fix the tool would be the wrong trade.
	Sudo bool
}

func (s Stack) base() string {
	b := "cd " + sshx.Quote(s.Dir) + " && "
	if s.Sudo {
		// -n so a host that would prompt for a password fails loudly instead of hanging a
		// non-interactive session until it times out.
		b += "sudo -n "
	}
	b += "docker compose"
	if s.File != "" {
		b += " -f " + sshx.Quote(s.File)
	}
	return b
}

// docker returns a plain `docker` invocation (not compose), honouring the same sudo choice.
func (s Stack) docker() string {
	if s.Sudo {
		return "sudo -n docker"
	}
	return "docker"
}

// UpOptions controls a deploy.
type UpOptions struct {
	Services      []string
	Pull          bool
	ForceRecreate bool
	NoDeps        bool
}

// Up brings the stack (or selected services) up in the background, optionally
// pulling first. Mirrors the manual `compose up -d --force-recreate` flow.
func Up(c *sshx.Client, s Stack, o UpOptions) (string, error) {
	var out strings.Builder
	if o.Pull {
		p, err := Pull(c, s, o.Services)
		out.WriteString(p)
		if err != nil {
			return out.String(), err
		}
	}
	cmd := s.base() + " up -d"
	if o.ForceRecreate {
		cmd += " --force-recreate"
	}
	if o.NoDeps {
		cmd += " --no-deps"
	}
	cmd += services(o.Services)
	r, err := c.Run(cmd)
	out.WriteString(r)
	return out.String(), err
}

// Pull fetches the latest images for the stack (or selected services).
func Pull(c *sshx.Client, s Stack, svcs []string) (string, error) {
	return c.Run(s.PullCmd(svcs))
}

// PullCmd is the command Pull runs. Exposed so it can be asserted without a host.
func (s Stack) PullCmd(svcs []string) string { return s.base() + " pull" + services(svcs) }

// PullUpdatable is Pull for the update path, tolerating images that cannot be pulled.
//
// Real stacks mix registry images with ones built on the host — BaryoClub runs postgres from Docker
// Hub alongside a locally built api and web. A plain pull fails the whole command on the first
// local-only image, which would make those stacks permanently un-updatable. Skipping them is right:
// an image with no registry to check cannot have a newer version to find.
func PullUpdatable(c *sshx.Client, s Stack, svcs []string) (string, error) {
	return c.Run(s.PullUpdatableCmd(svcs))
}

// PullUpdatableCmd is the command PullUpdatable runs.
func (s Stack) PullUpdatableCmd(svcs []string) string {
	return s.base() + " pull --ignore-pull-failures" + services(svcs)
}

// Ps lists the stack's containers.
func Ps(c *sshx.Client, s Stack) (string, error) {
	return c.Run(s.base() + " ps")
}

// Image describes one service's deployed-versus-declared state.
type Image struct {
	Service string // compose service name
	Ref     string // declared in the compose file, e.g. ghcr.io/baryodev/barako-cms:playground
	ID      string // what Ref resolves to on this host right now — the target
	Running string // what the container is actually running — may lag behind Ref after a pull
}

// Stale reports that the container is running something other than what its reference now points at.
// This, rather than "did the pull change anything", is what an update should act on: it catches a tag
// moved by a pull and a tag moved by a local rebuild alike, and it stays true until the container is
// actually recreated, so an interrupted update is still visible as pending afterwards.
func (i Image) Stale() bool {
	return i.ID != "" && i.Running != "" && i.ID != i.Running
}

// Images reports, for each service that declares an image, the reference from the compose file and
// the id that reference resolves to right now.
//
// The reference has to come from the compose file, not from `ps` or `compose images`. Those report
// what the container is actually running, which on a host that resolved a tag to a digest is a bare
// sha256 with a Repository of "sha256" — useless as a rollback target, since `docker tag` needs a
// name. The id is what makes rollback possible at all: once a pull moves the tag, the previous image
// survives on the host as an untagged id and nothing else points at it.
func Images(c *sshx.Client, s Stack, svcs []string) ([]Image, error) {
	// The Go template over `config` avoids depending on a JSON shape that differs between compose
	// versions, and skips build-only services, which have no image to pull or roll back.
	out, err := c.Run(s.ConfigCmd())
	if err != nil {
		return nil, err
	}
	refs, err := parseConfigImages(out)
	if err != nil {
		return nil, err
	}

	wanted := map[string]bool{}
	for _, s := range svcs {
		if s != "" {
			wanted[s] = true
		}
	}

	running, err := runningImages(c, s, svcs)
	if err != nil {
		return nil, err
	}

	var imgs []Image
	for _, svc := range sortedKeys(refs) {
		if len(wanted) > 0 && !wanted[svc] {
			continue
		}
		ref := refs[svc]
		img := Image{Service: svc, Ref: ref, Running: running[svc]}
		if id, err := c.Run(s.docker() + " image inspect --format '{{.Id}}' " + sshx.Quote(ref)); err == nil {
			img.ID = strings.TrimSpace(id)
		}
		// A reference with no local image cannot be compared or rolled back to; it is recorded so
		// the caller can report it rather than silently dropping the service.
		imgs = append(imgs, img)
	}
	return imgs, nil
}

// ConfigCmd asks compose for the resolved project. This, not `ps`, is where an image *reference*
// comes from — see Images.
func (s Stack) ConfigCmd() string { return s.base() + " config --format json" }

// runningImages maps service -> the image id its container is actually running.
func runningImages(c *sshx.Client, s Stack, svcs []string) (map[string]string, error) {
	out, err := c.Run(s.base() + ` ps -a --format '{{.Service}}\t{{.Image}}'` + services(svcs))
	if err != nil {
		return nil, err
	}
	running := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "\t", 2)
		if len(parts) != 2 {
			continue
		}
		svc, img := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		if svc == "" || img == "" {
			continue
		}
		// Depending on the compose version this is either a bare image id or a reference; resolve a
		// reference so both sides of the comparison are ids.
		if strings.HasPrefix(img, "sha256:") {
			running[svc] = img
			continue
		}
		if id, err := c.Run(s.docker() + " image inspect --format '{{.Id}}' " + sshx.Quote(img)); err == nil {
			running[svc] = strings.TrimSpace(id)
		}
	}
	return running, nil
}

// parseConfigImages pulls service -> image out of `docker compose config --format json`.
func parseConfigImages(jsonOut string) (map[string]string, error) {
	var cfg struct {
		Services map[string]struct {
			Image string `json:"image"`
		} `json:"services"`
	}
	if err := json.Unmarshal([]byte(jsonOut), &cfg); err != nil {
		return nil, fmt.Errorf("reading compose config: %w", err)
	}
	refs := map[string]string{}
	for name, svc := range cfg.Services {
		if svc.Image != "" {
			refs[name] = svc.Image
		}
	}
	return refs, nil
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Retag points a reference back at a specific image id, so `up -d` recreates from it. This is how an
// update is undone: the tag has already moved to the new image, and the old one survives only as an
// id until the next prune.
func Retag(c *sshx.Client, s Stack, id, ref string) (string, error) {
	return c.Run(s.docker() + " tag " + sshx.Quote(id) + " " + sshx.Quote(ref))
}

// Logs returns recent logs for the stack (or selected services).
func Logs(c *sshx.Client, s Stack, svcs []string, tail int) (string, error) {
	cmd := s.base() + " logs --no-color"
	if tail > 0 {
		cmd += " --tail " + strconv.Itoa(tail)
	}
	return c.Run(cmd + services(svcs))
}

func services(svcs []string) string {
	var b strings.Builder
	for _, s := range svcs {
		if s == "" {
			continue
		}
		b.WriteString(" " + sshx.Quote(s))
	}
	return b.String()
}
