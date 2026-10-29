// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package deploy runs a container image on a VM over SSH, the "deploy" in
// provision -> bootstrap -> deploy. It replaces any existing container of the
// same name so redeploys are idempotent.
package deploy

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/sshx"
)

// Spec describes a container to run.
type Spec struct {
	Name    string            // container name (stable across redeploys)
	Image   string            // e.g. example/app:latest
	Ports   []string          // "hostPort:containerPort", optionally "ip:host:container"
	Env     map[string]string // environment variables; sent on stdin to an owner-only file, never on argv (#93)
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
	if err := Validate(spec); err != nil {
		return Report{}, err
	}
	if spec.Pull {
		if _, err := c.Run(PullCmd(spec)); err != nil {
			return Report{}, err
		}
	}
	// Replace any existing container with the same name (ignore "no such container").
	_, _ = c.Run(RemoveCmd(spec))

	var out string
	var err error
	if len(spec.Env) > 0 {
		out, err = c.RunInput(runCmd(spec), EnvInput(spec))
		if err != nil {
			// The remote stderr is docker's, and docker can quote an env line back. The values are
			// known here, so they are removed before the error goes anywhere.
			return Report{}, fmt.Errorf("%s", redact(err.Error(), spec.Env))
		}
	} else if out, err = c.Run(runCmd(spec)); err != nil {
		return Report{}, err
	}
	return Report{Container: spec.Name, Image: spec.Image, ID: strings.TrimSpace(out)}, nil
}

// PullCmd is the image pull Run does when Pull is set.
func PullCmd(spec Spec) string { return "docker pull " + sshx.Quote(spec.Image) }

// RemoveCmd is the removal of any same-named container Run does before starting the new one.
func RemoveCmd(spec Spec) string { return "docker rm -f " + sshx.Quote(spec.Name) }

// Commands lists, in order, what Run would send to the VM, without running any of it.
func Commands(spec Spec) []string {
	var cmds []string
	if spec.Pull {
		cmds = append(cmds, PullCmd(spec))
	}
	return append(cmds, RemoveCmd(spec), runCmd(spec))
}

var envKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Validate refuses an environment that cannot be written as an env file. It runs before any remote
// call. The messages name the key, never the value.
func Validate(spec Spec) error {
	for k, v := range spec.Env {
		if !envKeyRe.MatchString(k) {
			return fmt.Errorf("invalid environment key %q: use letters, digits and underscores, not starting with a digit", k)
		}
		if strings.ContainsAny(v, "\n\r\x00") {
			return fmt.Errorf("environment %s: the value has a line break or NUL, which an env file cannot hold", k)
		}
	}
	return nil
}

// EnvInput is the env file Run sends on stdin: one KEY=value line per variable, sorted. docker's
// --env-file reads values literally, so no quoting is added.
func EnvInput(spec Spec) string {
	var b strings.Builder
	for _, k := range sortedKeys(spec.Env) {
		b.WriteString(k + "=" + spec.Env[k] + "\n")
	}
	return b.String()
}

func redact(msg string, env map[string]string) string {
	for _, v := range env {
		if v != "" {
			msg = strings.ReplaceAll(msg, v, "***")
		}
	}
	return msg
}

// runCmd builds the `docker run` line for a spec, so the command can be pinned without a host.
//
// With an environment the line is a small script: mktemp makes a 0600 file, stdin is copied into it,
// docker reads it with --env-file, and the trap removes it however the script ends. The values are
// in stdin, so they are not in this string and not in `ps` on the VM (#93). It runs under sh so the
// trap does not depend on the login shell.
func runCmd(spec Spec) string {
	if spec.Restart == "" {
		spec.Restart = "unless-stopped"
	}
	var b strings.Builder
	b.WriteString("docker run -d --name " + sshx.Quote(spec.Name) + " --restart " + spec.Restart)
	for _, p := range spec.Ports {
		b.WriteString(" -p " + sshx.Quote(p))
	}
	if len(spec.Env) > 0 {
		b.WriteString(` --env-file "$f"`)
	}
	b.WriteString(" " + sshx.Quote(spec.Image))
	if len(spec.Env) == 0 {
		return b.String()
	}
	script := `f=$(mktemp) && trap 'rm -f "$f"' EXIT && cat > "$f" && ` + b.String()
	return "sh -c " + sshx.Quote(script)
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
