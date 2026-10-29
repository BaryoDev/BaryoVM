// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package fleet

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
)

// DefaultProjectFile is the name `project release` and `project export` use when given none.
const DefaultProjectFile = "baryovm.project.json"

// Project is the committed, secret-free description of what to release: the VMs, the stacks on
// them and the order to release them in.
//
// It is the same shape as fleet.json on purpose. VMs and Stacks are the same structs, with the same
// field names (vms, keyPath, stacks, vm, dir, releaseFile), so a field added to Stack reaches a
// project file with no second edit and the two files cannot drift into two vocabularies (#91). Only
// project and order are new.
//
// A project file is read on its own. It is never merged with fleet.json: when the two describe the
// same stack, the project file is the whole truth for `project release`, so a CI job and a person
// release the same thing.
type Project struct {
	Project string  `json:"project"`
	VMs     []VM    `json:"vms"`
	Stacks  []Stack `json:"stacks"`
	// Order is the stacks to release, first to last. Empty means every stack in the order written.
	Order []string `json:"order,omitempty"`

	// rest holds top-level keys a newer BaryoVM wrote that this one does not know: see unknown.go.
	rest unknownFields
}

func (p *Project) UnmarshalJSON(b []byte) error {
	type plain Project
	var pl plain
	if err := json.Unmarshal(b, &pl); err != nil {
		return err
	}
	*p = Project(pl)
	rest, err := captureUnknown(b, knownKeys(reflect.TypeOf(plain{})))
	if err != nil {
		return err
	}
	p.rest = rest
	return nil
}

func (p Project) MarshalJSON() ([]byte, error) {
	type plain Project
	b, err := json.Marshal(plain(p))
	if err != nil {
		return nil, err
	}
	return mergeUnknown(b, p.rest)
}

// ParseProject reads a project file. Secrets are refused first, on the raw JSON, so a literal in a
// field this binary has never heard of is still caught; then the file is parsed into the typed
// structs and checked.
func ParseProject(b []byte) (*Project, error) {
	var raw any
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("parse project file: %w", err)
	}
	if err := refuseSecrets(raw); err != nil {
		return nil, err
	}
	var p Project
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("parse project file: %w", err)
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

// LoadProject reads and checks the project file at path.
func LoadProject(path string) (*Project, error) {
	b, err := os.ReadFile(path) // #nosec G304 -- the path is the operator's own argument
	if err != nil {
		return nil, fmt.Errorf("read project file: %w", err)
	}
	p, err := ParseProject(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return p, nil
}

// ReleaseOrder is the stacks to release, in order.
func (p *Project) ReleaseOrder() []string {
	if len(p.Order) > 0 {
		return p.Order
	}
	out := make([]string, 0, len(p.Stacks))
	for _, s := range p.Stacks {
		out = append(out, s.Name)
	}
	return out
}

// Validate checks what must hold before anything is contacted. Errors name the field.
func (p *Project) Validate() error {
	if strings.TrimSpace(p.Project) == "" {
		return fmt.Errorf("project: required, name the project")
	}
	if err := ValidateKeyPath(p.VMs); err != nil {
		return err
	}
	vms := map[string]bool{}
	for i, v := range p.VMs {
		f := fmt.Sprintf("vms[%d]", i)
		switch {
		case v.Name == "":
			return fmt.Errorf("%s.name: required", f)
		case vms[v.Name]:
			return fmt.Errorf("%s.name: %q is listed twice", f, v.Name)
		case v.Host == "":
			return fmt.Errorf("%s.host: required (vm %q)", f, v.Name)
		case v.User == "":
			return fmt.Errorf("%s.user: required (vm %q)", f, v.Name)
		}
		vms[v.Name] = true
	}
	stacks := map[string]bool{}
	for i, s := range p.Stacks {
		f := fmt.Sprintf("stacks[%d]", i)
		switch {
		case s.Name == "":
			return fmt.Errorf("%s.name: required", f)
		case stacks[s.Name]:
			return fmt.Errorf("%s.name: %q is listed twice", f, s.Name)
		case !vms[s.VM]:
			return fmt.Errorf("%s.vm: stack %q references %q, which is not in vms", f, s.Name, s.VM)
		case !strings.HasPrefix(s.Dir, "/"):
			return fmt.Errorf("%s.dir: stack %q needs the absolute remote directory", f, s.Name)
		}
		if s.ReleaseFile != "" && (filepath.IsAbs(s.ReleaseFile) || strings.HasPrefix(s.ReleaseFile, "~")) {
			return fmt.Errorf("%s.releaseFile: %q is not relative to the project file, so it cannot be shared: write it relative to the directory holding the project file", f, s.ReleaseFile)
		}
		stacks[s.Name] = true
	}
	seen := map[string]bool{}
	for i, name := range p.Order {
		switch {
		case !stacks[name]:
			return fmt.Errorf("order[%d]: %q is not a stack in this file", i, name)
		case seen[name]:
			return fmt.Errorf("order[%d]: %q is listed twice", i, name)
		}
		seen[name] = true
	}
	for _, name := range p.ReleaseOrder() {
		for _, s := range p.Stacks {
			if s.Name == name && s.ReleaseFile == "" {
				return fmt.Errorf("stacks.%s.releaseFile: required to release it from a project file", name)
			}
		}
	}
	for i, v := range p.VMs {
		if v.HostKey == "" {
			continue
		}
		if f := strings.Fields(v.HostKey); strings.ContainsAny(v.HostKey, "\r\n") || len(f) < 2 || len(f) > 3 || !strings.HasPrefix(f[0], "ssh-") && !strings.HasPrefix(f[0], "ecdsa-") {
			return fmt.Errorf("vms[%d].hostKey: want a public host key line like \"ssh-ed25519 AAAA...\"", i)
		}
	}
	return nil
}

var envRefRe = regexp.MustCompile(`^env:[A-Za-z_][A-Za-z0-9_]*$`)

// IsEnvRef reports whether v is an `env:NAME` reference.
func IsEnvRef(v string) bool { return envRefRe.MatchString(v) }

// ValidateKeyPath refuses a keyPath that is not a reference. A reference is `env:NAME` (the
// variable holds a path to the key) or a path. Anything else, and anything that carries key
// material, is a literal that would be committed.
func ValidateKeyPath(vms []VM) error {
	for i, v := range vms {
		if err := checkKeyRef(fmt.Sprintf("vms[%d].keyPath", i), v.KeyPath); err != nil {
			return err
		}
	}
	return nil
}

func checkKeyRef(field, v string) error {
	if v == "" {
		return fmt.Errorf("%s: required, as env:NAME or a path to the key", field)
	}
	if IsEnvRef(v) {
		return nil
	}
	if strings.HasPrefix(v, "env:") {
		return fmt.Errorf("%s: %q is not a valid env:NAME reference", field, v)
	}
	if strings.ContainsAny(v, "\r\n") || strings.Contains(v, "PRIVATE KEY") || strings.HasPrefix(v, "ssh-") {
		return fmt.Errorf("%s: holds key material, not a reference: use env:NAME or a path to the key", field)
	}
	if !strings.ContainsAny(v, `/\`) && !strings.HasPrefix(v, "~") {
		return fmt.Errorf("%s: the value looks like a literal secret, not a path: use env:NAME or a path to the key", field)
	}
	return nil
}

var (
	privateKeyRe = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY`)
	tokenRe      = regexp.MustCompile(`\b(ghp_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|AKIA[0-9A-Z]{16}|xox[abprs]-[A-Za-z0-9-]{10,}|sk_live_[A-Za-z0-9]{10,}|npm_[A-Za-z0-9]{30,})`)
	secretNameRe = regexp.MustCompile(`(?i)(password|passwd|passphrase|secret|token|api[_-]?key|private[_-]?key|credential)`)
)

// refuseSecrets walks the parsed JSON and refuses what looks like a literal secret, naming the
// field. It looks at every field, including ones this binary does not know, because the file is
// committed and a newer field is as able to hold a password as an old one. The error never repeats
// the value.
func refuseSecrets(v any) error { return walkSecrets("", v, false) }

// hostKeyPathRe is the one place a public key is expected. Its value is exempt from the token
// check only, since base64 can look like anything.
var hostKeyPathRe = regexp.MustCompile(`^vms\[\d+\]\.hostKey$`)

// walkSecrets visits every leaf. secret is true under a field named like a secret, so every leaf
// below it is judged, not only the direct child.
func walkSecrets(path string, v any, secret bool) error {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			p := k
			if path != "" {
				p = path + "." + k
			}
			if err := walkSecrets(p, child, secret || secretNameRe.MatchString(k)); err != nil {
				return err
			}
		}
	case []any:
		for i, child := range t {
			if err := walkSecrets(fmt.Sprintf("%s[%d]", path, i), child, secret); err != nil {
				return err
			}
		}
	case string:
		if privateKeyRe.MatchString(t) {
			return fmt.Errorf("%s: holds a private key: keep the key out of the file and reference it as env:NAME or a path", path)
		}
		if secret && !emptyOrRef(t) {
			return fmt.Errorf("%s: a field named like a secret holds a literal value: write env:NAME instead", path)
		}
		if hostKeyPathRe.MatchString(path) {
			t = strings.Join(strings.Fields(t)[min(2, len(strings.Fields(t))):], " ") // only the comment
		}
		if tokenRe.MatchString(t) {
			return fmt.Errorf("%s: holds what looks like an access token: write env:NAME instead", path)
		}
	default:
		if secret && !emptyOrRef(v) {
			return fmt.Errorf("%s: a field named like a secret holds a literal value: write env:NAME instead", path)
		}
	}
	return nil
}

func emptyOrRef(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == "" || IsEnvRef(t)
	case bool:
		return !t
	case map[string]any, []any:
		// A container is judged by what is inside it.
		return true
	}
	return false
}

// Store builds the in-memory fleet a release runs from, with every path made usable: dir is the
// directory holding the project file, releaseFile and relative key paths resolve against it, and
// an env:NAME key reference becomes the path the variable holds. Nothing is written to disk.
func (p *Project) Store(dir string, getenv func(string) string) (*Store, error) {
	s := &Store{}
	for i, v := range p.VMs {
		if v.Provider == "" {
			v.Provider = "ssh"
		}
		kp, err := resolveKey(fmt.Sprintf("vms[%d].keyPath", i), v.KeyPath, dir, getenv)
		if err != nil {
			return nil, err
		}
		v.KeyPath = kp
		s.VMs = append(s.VMs, v)
	}
	for _, st := range p.Stacks {
		if st.ReleaseFile != "" {
			st.ReleaseFile = filepath.Join(dir, filepath.FromSlash(st.ReleaseFile))
		}
		s.Stacks = append(s.Stacks, st)
	}
	return s, nil
}

func resolveKey(field, ref, dir string, getenv func(string) string) (string, error) {
	if IsEnvRef(ref) {
		name := strings.TrimPrefix(ref, "env:")
		v := getenv(name)
		if v == "" {
			return "", fmt.Errorf("%s: %s is not set: export it as the path to the SSH key", field, name)
		}
		return v, nil
	}
	if strings.HasPrefix(ref, "~") || filepath.IsAbs(ref) {
		return ref, nil
	}
	return filepath.Join(dir, filepath.FromSlash(ref)), nil
}
