// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package compose

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/sshx"
)

// Adoptee is a container started by hand that --adopt is about to remove.
type Adoptee struct {
	Name    string `json:"name"`
	Image   string `json:"image"`
	Created string `json:"created"`
}

// anonymousVolume matches the 64 hex name docker gives a volume nobody named. It has no name the
// user could declare external, so it is not what the refusal is about.
var anonymousVolume = regexp.MustCompile(`^[0-9a-f]{64}$`)

// VolumeRefusal is one named volume a container uses that the compose project does not declare
// external. Removing the container would leave the data behind and the new container on an empty
// volume.
type VolumeRefusal struct {
	Container string `json:"container"`
	Volume    string `json:"volume"`
}

// AdoptRefusedError is returned when adopting would abandon named volumes. There is no flag that
// bypasses it: no one means to start a database on empty storage, and a green deploy over that
// reads like total data loss (#7).
type AdoptRefusedError struct {
	Refusals []VolumeRefusal
}

func (e *AdoptRefusedError) Error() string {
	parts := make([]string, 0, len(e.Refusals))
	for _, r := range e.Refusals {
		parts = append(parts, fmt.Sprintf("container %q uses the named volume %q, which the compose file does not declare external, "+
			"so compose would create a new empty volume and leave the data behind. Add this to the compose file and try again:\n\n%s",
			r.Container, r.Volume, VolumeYAML(r.Volume)))
	}
	return "refusing to adopt: " + strings.Join(parts, "\n")
}

// VolumeYAML is the compose snippet that points the project at a volume that already exists.
func VolumeYAML(volume string) string {
	return "volumes:\n  " + volume + ":\n    external: true"
}

// InspectMountsCmd reads a container's mounts as JSON.
func (s Stack) InspectMountsCmd(name string) string {
	return s.docker() + ` inspect --format '{{json .Mounts}}' ` + sshx.Quote(name)
}

// InspectInfoCmd reads a container's image and created time, space separated.
func (s Stack) InspectInfoCmd(name string) string {
	return s.docker() + ` inspect --format '{{.Config.Image}} {{.Created}}' ` + sshx.Quote(name)
}

// RemoveCmd force-removes a container. It does not pass -v, so its volumes stay.
func (s Stack) RemoveCmd(name string) string {
	return s.docker() + " rm -f " + sshx.Quote(name)
}

// externalVolumes returns the volume names the compose file declares external, under both the key
// and the resolved name, since compose config reports the actual name in `name`.
func externalVolumes(configJSON string) (map[string]bool, error) {
	var cfg struct {
		Volumes map[string]struct {
			Name     string `json:"name"`
			External any    `json:"external"`
		} `json:"volumes"`
	}
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		return nil, fmt.Errorf("parse compose config: %w", err)
	}
	ext := map[string]bool{}
	for key, v := range cfg.Volumes {
		if v.External == nil {
			continue
		}
		if b, ok := v.External.(bool); ok && !b {
			continue
		}
		ext[key] = true
		if v.Name != "" {
			ext[v.Name] = true
		}
	}
	return ext, nil
}

// PlanAdoption reads each container's mounts and the compose config, and returns what would be
// removed. It returns *AdoptRefusedError when a container uses a named volume the compose file does
// not declare external. It never changes the host.
func PlanAdoption(c sshx.Runner, s Stack, names []string) ([]Adoptee, error) {
	cfgOut, err := c.Run(s.ConfigCmd())
	if err != nil {
		return nil, err
	}
	ext, err := externalVolumes(cfgOut)
	if err != nil {
		return nil, err
	}
	var plan []Adoptee
	var refusals []VolumeRefusal
	for _, name := range names {
		out, err := c.Run(s.InspectMountsCmd(name))
		if err != nil {
			return nil, fmt.Errorf("read mounts of %q: %w", name, err)
		}
		var mounts []struct {
			Type string `json:"Type"`
			Name string `json:"Name"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &mounts); err != nil {
			return nil, fmt.Errorf("parse mounts of %q: %w", name, err)
		}
		for _, m := range mounts {
			if m.Type != "volume" || m.Name == "" || anonymousVolume.MatchString(m.Name) || ext[m.Name] {
				continue
			}
			refusals = append(refusals, VolumeRefusal{Container: name, Volume: m.Name})
		}
		info, err := c.Run(s.InspectInfoCmd(name))
		if err != nil {
			return nil, fmt.Errorf("inspect %q: %w", name, err)
		}
		a := Adoptee{Name: name}
		fields := strings.SplitN(strings.TrimSpace(info), " ", 2)
		a.Image = fields[0]
		if len(fields) == 2 {
			a.Created = fields[1]
		}
		plan = append(plan, a)
	}
	if len(refusals) > 0 {
		sort.Slice(refusals, func(i, j int) bool {
			if refusals[i].Container != refusals[j].Container {
				return refusals[i].Container < refusals[j].Container
			}
			return refusals[i].Volume < refusals[j].Volume
		})
		return nil, &AdoptRefusedError{Refusals: refusals}
	}
	return plan, nil
}

// Adopt removes the planned containers.
func Adopt(c sshx.Runner, s Stack, plan []Adoptee) error {
	for _, a := range plan {
		if out, err := c.Run(s.RemoveCmd(a.Name)); err != nil {
			return fmt.Errorf("remove %q: %w: %s", a.Name, err, strings.TrimSpace(out))
		}
	}
	return nil
}
