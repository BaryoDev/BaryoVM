// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package recipe

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/BaryoDev/BaryoVM/internal/compose"
	"github.com/BaryoDev/BaryoVM/internal/envfile"
	"github.com/BaryoDev/BaryoVM/internal/secretref"
	"github.com/BaryoDev/BaryoVM/internal/sshx"
	"github.com/BaryoDev/BaryoVM/internal/verify"
)

// DefaultComposeFile is the file a recipe deploy writes in the stack directory.
const DefaultComposeFile = "docker-compose.recipe.yml"

// ComposeYAML renders the recipe's services as a compose file.
//
// Every value is written as a JSON string, which is also a YAML string, so nothing in it can be
// read as structure. The values were already checked by Validate; this is the second fence.
func (r *Recipe) ComposeYAML() (string, error) {
	var b strings.Builder
	q := func(s string) string {
		j, _ := json.Marshal(s)
		return string(j)
	}
	b.WriteString("# written by baryovm from recipe " + q(r.Name) + "; edit the recipe, not this file\n")
	b.WriteString("services:\n")
	for _, s := range r.Services {
		if s.Image == "" {
			return "", fmt.Errorf("service %q builds from source, which a recipe release does not do yet: give it an image", s.Name)
		}
		b.WriteString("  " + q(s.Name) + ":\n")
		b.WriteString("    image: " + q(s.Image) + "\n")
		b.WriteString("    restart: unless-stopped\n")
		if len(s.Ports) > 0 {
			b.WriteString("    ports:\n")
			for _, p := range s.Ports {
				b.WriteString("      - " + q(p) + "\n")
			}
		}
		if len(r.Inputs) > 0 {
			b.WriteString("    env_file:\n      - \".env\"\n")
		}
	}
	return b.String(), nil
}

// ResolveInputs turns the operator's bindings (input name to a reference or a literal) into values.
//
// A secret input takes a reference only (env:, file:), never a literal, so the value is not in
// shell history or in a file that gets committed. A binding for an input the recipe does not
// declare is refused. A missing required input is refused before any value is read.
func (r *Recipe) ResolveInputs(bind map[string]string, res secretref.Resolver) (map[string]string, error) {
	declared := map[string]Input{}
	for _, in := range r.Inputs {
		declared[in.Name] = in
	}
	var stray []string
	for name := range bind {
		if _, ok := declared[name]; !ok {
			stray = append(stray, name)
		}
	}
	if len(stray) > 0 {
		sort.Strings(stray)
		return nil, fmt.Errorf("recipe %q does not declare input(s): %s (it declares: %s)",
			r.Name, strings.Join(stray, ", "), strings.Join(orNone(r.InputNames()), ", "))
	}
	have := map[string]string{}
	for name := range bind {
		have[name] = ""
	}
	if missing := Missing(r.Inputs, have); len(missing) > 0 {
		return nil, errors.New(ExplainMissing(missing))
	}
	out := map[string]string{}
	for _, in := range r.Inputs {
		raw, bound := bind[in.Name]
		if !bound {
			out[in.Name] = in.Default
			continue
		}
		v := raw
		if secretref.IsRef(raw) {
			got, err := res.Resolve(raw)
			if err != nil {
				return nil, fmt.Errorf("input %s: %w", in.Name, err)
			}
			v = got
		} else if in.Secret {
			return nil, fmt.Errorf("input %s is secret: bind it to a reference such as env:NAME or file:PATH, not a literal", in.Name)
		}
		if err := envfile.ValidValue(v); err != nil {
			return nil, fmt.Errorf("input %s: %w", in.Name, err)
		}
		if v == "" && in.IsRequired() {
			return nil, fmt.Errorf("input %s resolved to an empty value", in.Name)
		}
		out[in.Name] = v
	}
	return out, nil
}

// InputRunner is a Runner that can send a value on stdin, so an input value never appears in a
// command line.
type InputRunner = envfile.Runner

// DeployInput is one recipe deploy.
type DeployInput struct {
	Recipe *Recipe
	Dir    string // stack directory on the VM
	// File is the compose file name written in Dir. Empty means DefaultComposeFile.
	File string
	Sudo bool
	// Env holds the resolved input values, written to Dir/.env.
	Env map[string]string
	// Clock times the per-service health wait. A zero Clock uses the real one.
	Clock verify.Clock
}

// DeployResult says what a deploy did and what it undid.
type DeployResult struct {
	Services    []string `json:"services"`             // services brought up, in order
	Failed      string   `json:"failed,omitempty"`     // the service that failed
	RolledBack  []string `json:"rolledBack,omitempty"` // services returned to the previous recipe
	Removed     []string `json:"removed,omitempty"`    // services that did not exist before and were removed
	FirstDeploy bool     `json:"firstDeploy,omitempty"`
}

func (in DeployInput) file() string {
	if in.File == "" {
		return DefaultComposeFile
	}
	return in.File
}

func (in DeployInput) path(name string) string {
	return strings.TrimSuffix(in.Dir, "/") + "/" + name
}

// PrepareCmd creates the stack directory and an owner-only .env if there is none.
func (in DeployInput) PrepareCmd() string {
	env := in.path(".env")
	script := "set -e\numask 022\nmkdir -p " + sshx.Quote(in.Dir) + "\numask 077\n[ -e " + sshx.Quote(env) + " ] || : > " + sshx.Quote(env)
	return sshx.SudoShell(in.Sudo, script)
}

// WriteFileCmd writes content to Dir/name. The content travels as base64 inside the command, so
// no character in it can end the quoting. It never carries a secret: .env goes through envfile.
func (in DeployInput) WriteFileCmd(name, content string) string {
	b := base64.StdEncoding.EncodeToString([]byte(content))
	script := "printf %s " + sshx.Quote(b) + " | base64 -d > " + sshx.Quote(in.path(name))
	return sshx.SudoShell(in.Sudo, script)
}

// SnapshotCmd keeps the compose file now in place as <file>.prev and prints "had" when there was one
// and "none" when this is the first deploy.
func (in DeployInput) SnapshotCmd() string {
	f, p := sshx.Quote(in.path(in.file())), sshx.Quote(in.path(in.file()+".prev"))
	script := "if [ -f " + f + " ]; then cp -p " + f + " " + p + " && echo had; else rm -f " + p + "; echo none; fi"
	return sshx.SudoShell(in.Sudo, script)
}

// PromoteCmd moves the new file over the live one.
func (in DeployInput) PromoteCmd() string {
	script := "mv -f " + sshx.Quote(in.path(in.file()+".new")) + " " + sshx.Quote(in.path(in.file()))
	return sshx.SudoShell(in.Sudo, script)
}

// RestoreCmd puts the snapshot back as the live file.
func (in DeployInput) RestoreCmd() string {
	script := "cp -p " + sshx.Quote(in.path(in.file()+".prev")) + " " + sshx.Quote(in.path(in.file()))
	return sshx.SudoShell(in.Sudo, script)
}

// DropCmd removes the live and new files, for a first deploy that failed.
func (in DeployInput) DropCmd() string {
	script := "rm -f " + sshx.Quote(in.path(in.file())) + " " + sshx.Quote(in.path(in.file()+".new"))
	return sshx.SudoShell(in.Sudo, script)
}

func (in DeployInput) stack(file string) compose.Stack {
	return compose.Stack{Dir: in.Dir, File: file, Sudo: in.Sudo}
}

// HealthCmd probes a service's health URL from the VM, where a loopback URL means the right host.
func HealthCmd(url string) string {
	return "curl -fsS --max-time 5 -o /dev/null " + sshx.Quote(url)
}

// Deploy brings the recipe's services up one at a time, in declaration order. It writes the new
// compose file beside the live one and pulls every image first, so a registry failure changes
// nothing. After that each service is started and, when it has a healthUrl, waited on. If one
// fails, every service touched so far (that one included) goes back to the previous recipe.
//
// What rolls back: the compose file, and the containers of the services this deploy touched. Services
// that did not exist before are removed. What does not: the .env values, volume contents, and
// anything a service did to its own data on start.
func Deploy(c sshx.Runner, in DeployInput) (*DeployResult, error) {
	r := in.Recipe
	res := &DeployResult{}
	if len(r.Services) == 0 {
		return res, errors.New("the recipe has no services, so there is nothing to bring up")
	}
	yaml, err := r.ComposeYAML()
	if err != nil {
		return res, err
	}
	run := func(cmd string) (string, error) { return c.Run(cmd) }

	if _, err := run(in.PrepareCmd()); err != nil {
		return res, fmt.Errorf("prepare %s: %w", in.Dir, err)
	}
	if len(in.Env) > 0 {
		ir, ok := c.(envfile.Runner)
		if !ok {
			return res, errors.New("this connection cannot send input to the VM, so the recipe inputs cannot be written")
		}
		keys := make([]string, 0, len(in.Env))
		for k := range in.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if in.Env[k] == "" {
				continue
			}
			if err := envfile.Set(ir, in.path(".env"), k, in.Env[k], in.Sudo); err != nil {
				return res, fmt.Errorf("write input %s: %w", k, err)
			}
		}
	}

	names := r.ServiceNames()
	newFile := in.file() + ".new"
	if _, err := run(in.WriteFileCmd(newFile, yaml)); err != nil {
		return res, fmt.Errorf("write the compose file: %w", err)
	}
	if _, err := compose.Pull(c, in.stack(newFile), names); err != nil {
		_, _ = run(in.DropNewCmd())
		return res, fmt.Errorf("pull images (nothing was changed): %w", err)
	}

	snap, err := run(in.SnapshotCmd())
	if err != nil {
		return res, fmt.Errorf("keep the previous compose file: %w", err)
	}
	res.FirstDeploy = !strings.Contains(snap, "had")
	prev := map[string]bool{}
	if !res.FirstDeploy {
		out, err := run(in.stack(in.file()).ConfigServicesCmd())
		if err != nil {
			return res, fmt.Errorf("read the services now deployed: %w", err)
		}
		for _, l := range strings.Fields(out) {
			prev[l] = true
		}
	}
	if _, err := run(in.PromoteCmd()); err != nil {
		return res, fmt.Errorf("put the new compose file in place: %w", err)
	}

	for _, name := range names {
		res.Services = append(res.Services, name)
		_, err := compose.Up(c, in.stack(in.file()), compose.UpOptions{Services: []string{name}, NoDeps: true})
		if err == nil {
			err = in.waitHealthy(c, name)
		}
		if err != nil {
			res.Failed = name
			rbErr := in.rollback(c, res, prev)
			if rbErr != nil {
				return res, fmt.Errorf("service %s failed: %v; rolling back also failed: %w", name, err, rbErr)
			}
			return res, fmt.Errorf("service %s failed and the release was rolled back (%s): %w",
				name, rollbackSummary(res), err)
		}
	}
	return res, nil
}

// DropNewCmd removes only the staged file.
func (in DeployInput) DropNewCmd() string {
	return sshx.SudoShell(in.Sudo, "rm -f "+sshx.Quote(in.path(in.file()+".new")))
}

func rollbackSummary(res *DeployResult) string {
	parts := []string{}
	if len(res.RolledBack) > 0 {
		parts = append(parts, "restored "+strings.Join(res.RolledBack, ", "))
	}
	if len(res.Removed) > 0 {
		parts = append(parts, "removed "+strings.Join(res.Removed, ", "))
	}
	if len(parts) == 0 {
		return "nothing to restore"
	}
	return strings.Join(parts, "; ")
}

func (in DeployInput) waitHealthy(c sshx.Runner, name string) error {
	var url string
	for _, s := range in.Recipe.Services {
		if s.Name == name {
			url = s.HealthURL
		}
	}
	if url == "" {
		return nil
	}
	clk := in.Clock
	if clk.Now == nil || clk.Sleep == nil {
		clk = verify.RealClock()
	}
	steps := []verify.Step{verify.ExitStep("health "+name, HealthCmd(url))}
	out := verify.Retry(verify.Budget{Attempts: 5, Delay: 3 * time.Second}, clk, func() (bool, string) {
		return verify.Pass(c, steps)
	})
	return out.Err()
}

// rollback returns every touched service to the previous recipe. Services the previous compose file
// did not have are removed first, while the new file still names them; then the old file is put
// back and the services it had are started again from it.
func (in DeployInput) rollback(c sshx.Runner, res *DeployResult, prev map[string]bool) error {
	cs := in.stack(in.file())
	var errs []string
	var restore []string
	for _, name := range res.Services {
		if res.FirstDeploy || !prev[name] {
			if _, err := c.Run(cs.RemoveServiceCmd(name)); err != nil {
				errs = append(errs, "remove "+name+": "+err.Error())
				continue
			}
			res.Removed = append(res.Removed, name)
			continue
		}
		restore = append(restore, name)
	}
	if res.FirstDeploy {
		if _, err := c.Run(in.DropCmd()); err != nil {
			errs = append(errs, "drop compose file: "+err.Error())
		}
	} else {
		if _, err := c.Run(in.RestoreCmd()); err != nil {
			errs = append(errs, "restore compose file: "+err.Error())
			return errors.New(strings.Join(errs, "; "))
		}
		for _, name := range restore {
			if _, err := compose.Up(c, cs, compose.UpOptions{Services: []string{name}, NoDeps: true}); err != nil {
				errs = append(errs, "restart "+name+": "+err.Error())
				continue
			}
			res.RolledBack = append(res.RolledBack, name)
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}
