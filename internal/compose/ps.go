// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package compose

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/sshx"
)

// Container is one row of `docker compose ps`, as a UI or an agent wants it.
type Container struct {
	Name    string `json:"name"`
	Service string `json:"service"`
	State   string `json:"state"`
	Health  string `json:"health,omitempty"`
	Image   string `json:"image"`
	Ports   []Port `json:"ports,omitempty"`
}

// Port is one published port of a container.
type Port struct {
	Host      string `json:"host,omitempty"`
	Published int    `json:"published,omitempty"`
	Target    int    `json:"target"`
	Protocol  string `json:"protocol,omitempty"`
}

// PsJSONCmd is the command PsJSON runs.
func (s Stack) PsJSONCmd() string { return s.cmd(" ps --format json") }

// PsJSON lists the stack's containers as data. It asks compose for JSON instead of reading the
// table, which has no stable columns.
func PsJSON(c sshx.Runner, s Stack) ([]Container, error) {
	out, err := c.Run(s.PsJSONCmd())
	if err != nil {
		return nil, err
	}
	return ParsePs(out)
}

// composePs is the shape of one object in compose's JSON. Publishers are the only nested part.
type composePs struct {
	Name       string `json:"Name"`
	Service    string `json:"Service"`
	State      string `json:"State"`
	Health     string `json:"Health"`
	Image      string `json:"Image"`
	Publishers []struct {
		URL           string `json:"URL"`
		TargetPort    int    `json:"TargetPort"`
		PublishedPort int    `json:"PublishedPort"`
		Protocol      string `json:"Protocol"`
	} `json:"Publishers"`
}

// ParsePs reads the output of `docker compose ps --format json`. Compose has printed one JSON
// array in some versions and one object per line in others, so both are accepted. Empty output
// is an empty list (a stack with nothing running); anything that is neither shape is an error.
func ParsePs(out string) ([]Container, error) {
	trim := strings.TrimSpace(out)
	list := []Container{}
	if trim == "" {
		return list, nil
	}
	var rows []composePs
	if strings.HasPrefix(trim, "[") {
		if err := json.Unmarshal([]byte(trim), &rows); err != nil {
			return nil, fmt.Errorf("compose ps output is not JSON: %w", err)
		}
	} else {
		dec := json.NewDecoder(bytes.NewReader([]byte(trim)))
		for {
			var r composePs
			err := dec.Decode(&r)
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, fmt.Errorf("compose ps output is not JSON: %w", err)
			}
			rows = append(rows, r)
		}
	}
	for _, r := range rows {
		ct := Container{Name: r.Name, Service: r.Service, State: r.State, Health: r.Health, Image: r.Image}
		for _, p := range r.Publishers {
			ct.Ports = append(ct.Ports, Port{Host: p.URL, Published: p.PublishedPort, Target: p.TargetPort, Protocol: p.Protocol})
		}
		list = append(list, ct)
	}
	return list, nil
}
