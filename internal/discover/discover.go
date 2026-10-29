// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package discover inventories the docker compose projects already running on a VM. It builds the
// commands, parses what they print, and renders the `stack add` line for each project. It never
// prints and never writes: the cli layer decides what to show and what to register.
package discover

import (
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/BaryoDev/BaryoVM/internal/ctr"
	"github.com/BaryoDev/BaryoVM/internal/sshx"
)

// Project is one compose project found on a VM.
type Project struct {
	Name        string   `json:"name"`
	Dir         string   `json:"dir"`
	File        string   `json:"file,omitempty"` // compose file name, set only when it is not a default name
	ConfigFiles []string `json:"configFiles"`
	Status      string   `json:"status,omitempty"`
	Services    int      `json:"services"`
	// PostgresGuess is a container whose image name contains postgres. It is a guess, not a fact:
	// it can name a replica, a different database, or a container that is not the one to back up.
	PostgresGuess string `json:"postgresGuess,omitempty"`
	// Note says why the project could not be inspected or cannot be adopted as it is.
	Note string `json:"note,omitempty"`
	// AddCommand is the `stack add` line for this project. Empty when the project cannot be
	// registered as it is (Note says why).
	AddCommand string `json:"addCommand,omitempty"`
	// State is found, registered, already-registered or skipped. Set by the cli layer.
	State string `json:"state"`
}

// LsCmd lists the running compose projects as JSON.
func LsCmd(sudo bool, runtime string) string {
	return sshx.Sudo(sudo, ctr.Compose(runtime)+" ls --format json")
}

// ConfigCmd prints the resolved config of one project. It names every config file, so a project
// split across an override file reports all its services.
func ConfigCmd(files []string, sudo bool, runtime string) string {
	var b strings.Builder
	b.WriteString(ctr.Compose(runtime))
	for _, f := range files {
		b.WriteString(" -f " + sshx.Quote(f))
	}
	b.WriteString(" config --format json")
	return sshx.Sudo(sudo, b.String())
}

// ParseLs reads the output of `docker compose ls --format json`. Empty output and `[]` both mean no
// projects. A project with no config files is dropped: there is nothing to read or register.
// The error never carries the raw output.
func ParseLs(out string) ([]Project, error) {
	out = strings.TrimSpace(out)
	if out == "" {
		return nil, nil
	}
	var rows []struct {
		Name        string `json:"Name"`
		Status      string `json:"Status"`
		ConfigFiles string `json:"ConfigFiles"`
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		return nil, fmt.Errorf("could not parse `docker compose ls` output as JSON: %v", err)
	}
	var projects []Project
	for _, r := range rows {
		var files []string
		for _, f := range strings.Split(r.ConfigFiles, ",") {
			if f = strings.TrimSpace(f); f != "" {
				files = append(files, f)
			}
		}
		if r.Name == "" || len(files) == 0 {
			continue
		}
		p := Project{Name: r.Name, Status: r.Status, ConfigFiles: files, Dir: path.Dir(files[0])}
		if !defaultComposeName[path.Base(files[0])] {
			p.File = path.Base(files[0])
		}
		if len(files) > 1 {
			p.Note = "uses several compose files, and `stack add` takes one: register it by hand"
		}
		Check(&p)
		projects = append(projects, p)
	}
	sort.SliceStable(projects, func(i, j int) bool { return projects[i].Dir < projects[j].Dir })
	return projects, nil
}

var defaultComposeName = map[string]bool{
	"compose.yaml": true, "compose.yml": true, "docker-compose.yaml": true, "docker-compose.yml": true,
}

// Inspect reads a project's resolved config (the output of ConfigCmd) into p: the service count and
// the postgres guess. The error never carries the raw output, which holds the project's
// environment.
func Inspect(p *Project, configJSON string) error {
	var cfg struct {
		Services map[string]struct {
			Image         string `json:"image"`
			ContainerName string `json:"container_name"`
		} `json:"services"`
	}
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		return fmt.Errorf("could not parse `docker compose config` output as JSON: %v", err)
	}
	p.Services = len(cfg.Services)
	names := make([]string, 0, len(cfg.Services))
	for n := range cfg.Services {
		names = append(names, n)
	}
	sort.Strings(names)
	p.PostgresGuess = ""
	for _, n := range names {
		s := cfg.Services[n]
		if !strings.Contains(strings.ToLower(s.Image), "postgres") {
			continue
		}
		p.PostgresGuess = s.ContainerName
		if p.PostgresGuess == "" {
			p.PostgresGuess = p.Name + "-" + n + "-1"
		}
		break
	}
	Check(p)
	return nil
}

// hasControl reports a control, format or separator character (anything that is not printable, so
// also U+202E, U+200B and U+2028) or invalid UTF-8.
// encoding/json turns invalid bytes into U+FFFD, so that rune counts as invalid too.
func hasControl(s string) bool {
	if !utf8.ValidString(s) {
		return true
	}
	for _, r := range s {
		if r == utf8.RuneError || !unicode.IsPrint(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			return true
		}
	}
	return false
}

// Check marks p skipped when a string that came from the VM holds a control character. Such a value
// could rewrite the terminal or fake a prompt, so the project is never adopted and never printed
// raw. The note names the field, not the value.
func Check(p *Project) {
	field := ""
	switch {
	case hasControl(p.Name):
		field = "name"
	case hasControl(p.Dir):
		field = "directory"
	case hasControl(p.File):
		field = "config file name"
	case hasControl(p.PostgresGuess):
		field = "postgres container"
	default:
		for _, f := range p.ConfigFiles {
			if hasControl(f) {
				field = "config file path"
				break
			}
		}
	}
	if field == "" {
		return
	}
	p.State = "skipped"
	p.AddCommand = ""
	if p.Note != "" {
		p.Note += "; "
	}
	p.Note += "the " + field + " holds a control character, so it is not shown raw and cannot be adopted"
}

// AddCommand is the `stack add` invocation for p on vm, every value quoted for a shell.
func AddCommand(p Project, vm string, sudo bool, runtime string) string {
	parts := []string{"baryovm", "stack", "add", sshx.Quote(p.Name), "--vm", sshx.Quote(vm), "--path", sshx.Quote(p.Dir)}
	if p.File != "" {
		parts = append(parts, "--file", sshx.Quote(p.File))
	}
	if p.PostgresGuess != "" {
		parts = append(parts, "--db-container", sshx.Quote(p.PostgresGuess))
	}
	if sudo {
		parts = append(parts, "--sudo")
	}
	if runtime != "" && runtime != ctr.Docker {
		parts = append(parts, "--runtime", ctr.Bin(runtime))
	}
	return strings.Join(parts, " ")
}
