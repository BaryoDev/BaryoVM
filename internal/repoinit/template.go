// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package repoinit scaffolds the release CI of a repository from a template and checks that
// trusted publishing works (#52).
//
// What it does not do is create the registry policy. nuget.org and npm create trusted publishing
// policies through their web UI and have no documented API, so the policy is a manual step and
// this package only says exactly what to enter. What it can do, and does, is trigger the
// workflow's dry run and read whether the OIDC exchange step succeeded.
//
// Nothing here prints. Every GitHub call goes through the GitHub interface, which has a real
// implementation (API) and is faked in tests.
package repoinit

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// The built-in templates. all: keeps the dot directories (.github) that a plain pattern skips.
// _common holds the repository furniture every built-in template shares.
//
//go:embed all:templates
var builtin embed.FS

const (
	manifestFile = "template.json"
	filesDir     = "files"
	commonName   = "_common"
)

var (
	nameRe     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	workflowRe = regexp.MustCompile(`^[A-Za-z0-9._-]+\.ya?ml$`)
)

// Manifest is a template's template.json.
type Manifest struct {
	Description string `json:"description"`
	// Registry is what the trusted publishing policy is created on, e.g. nuget.org.
	Registry    string `json:"registry"`
	RegistryURL string `json:"registryUrl"`
	// Workflow is the file name, under .github/workflows, of the publish workflow. It is the
	// third of the four values entered on the registry.
	Workflow string `json:"workflow"`
	// Environment is the fourth value. Empty means the workflow uses none and the field is left
	// blank on the registry.
	Environment string `json:"environment"`
	// OIDCSteps are the step names in the publish workflow that prove the OIDC exchange. verify
	// reports the policy as working only when every one of them succeeded.
	OIDCSteps []string `json:"oidcSteps"`
	// RequiredChecks are the job names branch protection waits for.
	RequiredChecks []string `json:"requiredChecks"`
	// DispatchInputs are the inputs verify sends to start the dry run.
	DispatchInputs map[string]string `json:"dispatchInputs"`
	// Notes are printed after the four values.
	Notes []string `json:"notes"`
}

// Template is a loaded template: its manifest and the files it writes, keyed by the slash path
// relative to the repository root.
type Template struct {
	Name     string
	Source   string // "built-in" or the directory it came from
	Manifest Manifest
	Files    map[string][]byte
}

// Names lists the template names available: the built-in ones, plus the ones in dir.
func Names(dir string) []string {
	set := map[string]bool{}
	if ents, err := builtin.ReadDir("templates"); err == nil {
		for _, e := range ents {
			if e.IsDir() && e.Name() != commonName {
				set[e.Name()] = true
			}
		}
	}
	if dir != "" {
		if ents, err := os.ReadDir(dir); err == nil {
			for _, e := range ents {
				if e.IsDir() && nameRe.MatchString(e.Name()) {
					set[e.Name()] = true
				}
			}
		}
	}
	out := make([]string, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Load returns the template called name. A template of that name in dir wins over the built-in
// one, and a directory template stands alone: it does not get the shared furniture, so what it
// does not carry is not written.
func Load(name, dir string) (*Template, error) {
	if !nameRe.MatchString(name) {
		return nil, fmt.Errorf("template name %q: use lowercase letters, digits and dashes", name)
	}
	if dir != "" {
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			t, err := loadFS(os.DirFS(p), nil, name)
			if err != nil {
				return nil, fmt.Errorf("template %s in %s: %w", name, dir, err)
			}
			t.Source = filepath.Clean(p)
			return t, nil
		}
	}
	sub, err := fs.Sub(builtin, "templates/"+name)
	if _, statErr := fs.Stat(sub, manifestFile); err != nil || statErr != nil {
		return nil, fmt.Errorf("no template named %q: the available ones are %s", name, strings.Join(Names(dir), ", "))
	}
	common, err := fs.Sub(builtin, "templates/"+commonName+"/"+filesDir)
	if err != nil {
		return nil, err
	}
	t, err := loadFS(sub, common, name)
	if err != nil {
		return nil, fmt.Errorf("built-in template %s: %w", name, err)
	}
	t.Source = "built-in"
	return t, nil
}

// loadFS reads a manifest and a files tree. common, when set, is laid down first so the
// template's own files replace the shared ones of the same path.
func loadFS(fsys fs.FS, common fs.FS, name string) (*Template, error) {
	raw, err := fs.ReadFile(fsys, manifestFile)
	if err != nil {
		return nil, fmt.Errorf("no %s", manifestFile)
	}
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("%s: %w", manifestFile, err)
	}
	t := &Template{Name: name, Manifest: m, Files: map[string][]byte{}}
	if common != nil {
		if err := readTree(common, t.Files); err != nil {
			return nil, err
		}
	}
	if _, err := fs.Stat(fsys, filesDir); err != nil {
		return nil, fmt.Errorf("no %s directory", filesDir)
	}
	files, err := fs.Sub(fsys, filesDir)
	if err != nil {
		return nil, err
	}
	if err := readTree(files, t.Files); err != nil {
		return nil, err
	}
	if err := t.validate(); err != nil {
		return nil, err
	}
	return t, nil
}

// readTree reads the regular files under fsys. A symlink is refused, since a directory
// template is someone else's input and a link could read a file outside it.
func readTree(fsys fs.FS, into map[string][]byte) error {
	return fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s is not a regular file: templates hold plain files only", p)
		}
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		into[path.Clean(p)] = b
		return nil
	})
}

func (t *Template) validate() error {
	m := t.Manifest
	if !workflowRe.MatchString(m.Workflow) {
		return fmt.Errorf("manifest workflow %q must be a file name ending in .yml or .yaml", m.Workflow)
	}
	if _, ok := t.Files[".github/workflows/"+m.Workflow]; !ok {
		return fmt.Errorf("manifest names the workflow %s but the template has no .github/workflows/%s", m.Workflow, m.Workflow)
	}
	if m.Registry == "" {
		return fmt.Errorf("manifest has no registry")
	}
	if len(m.OIDCSteps) == 0 {
		return fmt.Errorf("manifest has no oidcSteps: verify would have nothing to read")
	}
	for p := range t.Files {
		if p == "." || strings.HasPrefix(p, "../") || strings.HasPrefix(p, "/") {
			return fmt.Errorf("file path %q leaves the repository", p)
		}
	}
	return nil
}

// Vars are the values substituted into a template's files. The tokens are @@NAME@@, not
// {{ }}, because a workflow is full of ${{ }} that must survive untouched.
type Vars struct {
	Owner     string
	Repo      string
	CodeOwner string
}

// Render returns the template's files with the tokens replaced.
func (t *Template) Render(v Vars) map[string][]byte {
	r := strings.NewReplacer(
		"@@OWNER@@", v.Owner,
		"@@REPO@@", v.Repo,
		"@@FULL_NAME@@", v.Owner+"/"+v.Repo,
		"@@CODEOWNER@@", v.CodeOwner,
	)
	out := make(map[string][]byte, len(t.Files))
	for p, b := range t.Files {
		out[p] = []byte(r.Replace(string(b)))
	}
	return out
}
