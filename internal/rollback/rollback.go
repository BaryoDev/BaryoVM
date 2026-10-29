// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package rollback keeps the images a stack was running before its last release or update, in a
// plain JSON file next to the database backups on the host, and puts them back on request. Images
// only: the database is never touched here, because a restore replaces data and stays its own
// explicit, --yes gated command.
package rollback

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/BaryoDev/BaryoVM/internal/backup"
	"github.com/BaryoDev/BaryoVM/internal/compose"
	"github.com/BaryoDev/BaryoVM/internal/sshx"
)

// File is the record's name inside the backup dir. It does not match the db-*.dump or env-* globs,
// so listing and retention leave it alone.
const File = "images-before.json"

// Image is one service as it was running before the change.
type Image struct {
	Service string `json:"service"`
	Ref     string `json:"ref"`
	ID      string `json:"id"`
}

// Record is what the host holds.
type Record struct {
	Stack  string  `json:"stack"`
	At     string  `json:"at"`
	Source string  `json:"source"` // "release" or "update"
	Images []Image `json:"images"`
}

// FromCompose keeps the services that have a running image to go back to. A service with no
// running container, or no tag to retag it to, has nothing to roll back.
func FromCompose(stack, source string, imgs []compose.Image) Record {
	r := Record{Stack: stack, Source: source, At: time.Now().UTC().Format(time.RFC3339), Images: []Image{}}
	for _, i := range imgs {
		if i.Running == "" || i.Ref == "" {
			continue
		}
		r.Images = append(r.Images, Image{Service: i.Service, Ref: i.Ref, ID: i.Running})
	}
	return r
}

// Save writes the record next to the backups.
func Save(c sshx.Runner, cfg backup.Config, r Record) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	_, err = c.Run(cfg.WriteFileCmd(File, append(b, '\n')))
	return err
}

// Load reads the record. Nil when nothing was recorded yet.
func Load(c sshx.Runner, cfg backup.Config) (*Record, error) {
	out, err := c.Run(cfg.ReadFileCmd(File))
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(out) == "" {
		return nil, nil
	}
	var r Record
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		return nil, fmt.Errorf("%s in %s is not valid JSON: %w", File, cfg.DirNote(), err)
	}
	return &r, nil
}

// Apply retags each recorded image back to its reference, then brings the stack up so the
// containers are recreated from them. It never touches the database.
func Apply(c sshx.Runner, s compose.Stack, r *Record) error {
	if len(r.Images) == 0 {
		return fmt.Errorf("the record holds no images to go back to")
	}
	for _, i := range r.Images {
		if _, err := compose.Retag(c, s, i.ID, i.Ref); err != nil {
			return fmt.Errorf("retag %s: %w", i.Service, err)
		}
	}
	if _, err := compose.Up(c, s, compose.UpOptions{}); err != nil {
		return fmt.Errorf("compose up: %w", err)
	}
	return nil
}
