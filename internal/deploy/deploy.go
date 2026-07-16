// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package deploy runs a container image on a VM over SSH — the "deploy" in
// provision -> bootstrap -> deploy. It replaces any existing container of the
// same name so redeploys are idempotent.
package deploy

import (
	"sort"
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/sshx"
)

// Spec describes a container to run.
type Spec struct {
	Name    string            // container name (stable across redeploys)
	Image   string            // e.g. arnelirobles/barako-cms:latest
	Ports   []string          // "hostPort:containerPort", optionally "ip:host:container"
	Env     map[string]string // environment variables
	Restart string            // docker restart policy; default "unless-stopped"
	Pull    bool              // pull the image before running
}

// Report describes a completed deploy.
type Report struct {
	Container string `json:"container"`
	Image     string `json:"image"`
	ID        string `json:"id"`
}

// Run pulls (optionally), removes any same-named container, and starts the new one.
func Run(c *sshx.Client, spec Spec) (Report, error) {
	if spec.Restart == "" {
		spec.Restart = "unless-stopped"
	}
	if spec.Pull {
		if _, err := c.Run("docker pull " + sshx.Quote(spec.Image)); err != nil {
			return Report{}, err
		}
	}
	// Replace any existing container with the same name (ignore "no such container").
	_, _ = c.Run("docker rm -f " + sshx.Quote(spec.Name))

	var b strings.Builder
	b.WriteString("docker run -d --name " + sshx.Quote(spec.Name) + " --restart " + spec.Restart)
	for _, p := range spec.Ports {
		b.WriteString(" -p " + sshx.Quote(p))
	}
	for _, k := range sortedKeys(spec.Env) {
		b.WriteString(" -e " + sshx.Quote(k+"="+spec.Env[k]))
	}
	b.WriteString(" " + sshx.Quote(spec.Image))

	out, err := c.Run(b.String())
	if err != nil {
		return Report{}, err
	}
	return Report{Container: spec.Name, Image: spec.Image, ID: strings.TrimSpace(out)}, nil
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
