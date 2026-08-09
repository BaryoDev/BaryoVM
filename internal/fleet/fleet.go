// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package fleet is BaryoVM's local inventory of VMs, persisted to
// ~/.baryovm/fleet.json. (The future Control API replaces this with Postgres;
// the CLI keeps a local file so it works standalone.)
package fleet

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BaryoDev/BaryoVM/internal/sshx"
)

// VM is a registered machine BaryoVM can drive over SSH.
type VM struct {
	Name     string `json:"name"`
	Host     string `json:"host"`
	Port     int    `json:"port,omitempty"`
	User     string `json:"user"`
	KeyPath  string `json:"keyPath"`
	Provider string `json:"provider"`     // "ssh" (registered) | "lightsail" | "oci"
	ID       string `json:"id,omitempty"` // cloud instance id, when provisioned
}

// Target converts a VM into an SSH dial target.
func (v VM) Target() sshx.Target {
	return sshx.Target{Host: v.Host, Port: v.Port, User: v.User, KeyPath: v.KeyPath}
}

// Stack is a docker compose project living on one of the fleet's VMs.
type Stack struct {
	Name string `json:"name"`
	VM   string `json:"vm"`             // references a VM by name
	Dir  string `json:"dir"`            // remote project directory, e.g. /opt/barakocms
	File string `json:"file,omitempty"` // compose file name; empty = compose default

	// Backup config (optional) — lets `stack backup/restore` dump this stack's Postgres DB + config.
	DBContainer string `json:"dbContainer,omitempty"` // postgres container, e.g. deploy-postgres-1
	DBName      string `json:"dbName,omitempty"`      // database to dump
	DBUser      string `json:"dbUser,omitempty"`      // defaults to postgres
	EnvFile     string `json:"envFile,omitempty"`     // config file to back up, relative to Dir (e.g. .env)
	BackupDir   string `json:"backupDir,omitempty"`   // remote dir for backups
	Keep        int    `json:"keep,omitempty"`        // retention count (default 14)

	// ReleaseFile is a local JSON manifest (sync + build recipe) used by `stack release`.
	ReleaseFile string `json:"releaseFile,omitempty"`

	// Update policy — used by `stack update`.
	//
	// AutoUpdate is what separates a stack that may be updated unattended from one that may not.
	// `stack update --auto`, the form a scheduler runs, refuses any stack without it. It defaults to
	// false so a newly registered stack is never picked up by a cron job nobody remembered writing.
	AutoUpdate bool `json:"autoUpdate,omitempty"`

	// HealthURL is probed from the VM after containers are recreated, e.g.
	// http://127.0.0.1:8091/health. Without it an update cannot be verified, so it cannot be rolled
	// back either, and --auto refuses to run: an unattended update that cannot tell success from a
	// crash loop is worse than no update at all.
	HealthURL string `json:"healthUrl,omitempty"`

	// UpdateServices limits which services an update touches. Empty means all of them.
	UpdateServices []string `json:"updateServices,omitempty"`
}

// Store is the whole fleet.
type Store struct {
	VMs    []VM    `json:"vms"`
	Stacks []Stack `json:"stacks,omitempty"`
}

func dir() string {
	if d := os.Getenv("BARYOVM_HOME"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".baryovm")
}

func path() string { return filepath.Join(dir(), "fleet.json") }

// Load reads the fleet, returning an empty store if none exists yet.
func Load() (*Store, error) {
	b, err := os.ReadFile(path())
	if os.IsNotExist(err) {
		return &Store{}, nil
	}
	if err != nil {
		return nil, err
	}
	var s Store
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path(), err)
	}
	return &s, nil
}

// Save writes the fleet with owner-only permissions (it references key paths).
func (s *Store) Save() error {
	if err := os.MkdirAll(dir(), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(s, "", "  ")
	return os.WriteFile(path(), b, 0o600)
}

// Find returns the VM with the given name, or nil.
func (s *Store) Find(name string) *VM {
	for i := range s.VMs {
		if s.VMs[i].Name == name {
			return &s.VMs[i]
		}
	}
	return nil
}

// Upsert adds or replaces a VM by name.
func (s *Store) Upsert(vm VM) {
	if existing := s.Find(vm.Name); existing != nil {
		*existing = vm
		return
	}
	s.VMs = append(s.VMs, vm)
}

// Remove deletes a VM by name, reporting whether it existed.
func (s *Store) Remove(name string) bool {
	for i := range s.VMs {
		if s.VMs[i].Name == name {
			s.VMs = append(s.VMs[:i], s.VMs[i+1:]...)
			return true
		}
	}
	return false
}

// FindStack returns the stack with the given name, or nil.
func (s *Store) FindStack(name string) *Stack {
	for i := range s.Stacks {
		if s.Stacks[i].Name == name {
			return &s.Stacks[i]
		}
	}
	return nil
}

// UpsertStack adds or replaces a stack by name.
func (s *Store) UpsertStack(st Stack) {
	if existing := s.FindStack(st.Name); existing != nil {
		*existing = st
		return
	}
	s.Stacks = append(s.Stacks, st)
}

// RemoveStack deletes a stack by name, reporting whether it existed.
func (s *Store) RemoveStack(name string) bool {
	for i := range s.Stacks {
		if s.Stacks[i].Name == name {
			s.Stacks = append(s.Stacks[:i], s.Stacks[i+1:]...)
			return true
		}
	}
	return false
}
