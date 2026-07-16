// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package compose drives `docker compose` on a VM over SSH — the day-to-day
// path for real workloads (barakoCMS, BaryoClub) that run as compose stacks
// rather than single containers.
package compose

import (
	"strconv"
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/sshx"
)

// Stack points at a remote compose project.
type Stack struct {
	Dir  string // project directory, e.g. /opt/barakocms
	File string // compose file name; empty uses compose's default
}

func (s Stack) base() string {
	b := "cd " + sshx.Quote(s.Dir) + " && docker compose"
	if s.File != "" {
		b += " -f " + sshx.Quote(s.File)
	}
	return b
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
	return c.Run(s.base() + " pull" + services(svcs))
}

// Ps lists the stack's containers.
func Ps(c *sshx.Client, s Stack) (string, error) {
	return c.Run(s.base() + " ps")
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
