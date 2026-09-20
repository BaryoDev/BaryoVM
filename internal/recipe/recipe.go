// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package recipe is the declaration an application publishes about how it is deployed.
//
// A recipe is owned and versioned by the application repository, not by BaryoVM. That is the whole
// point: `stack add <name>` stays generic, the name is whatever the operator calls their stack, and
// what makes it a barako or Umbraco or WordPress stack is the recipe it points at. No application
// name appears in BaryoVM's command surface, because the moment one does every other stack is
// second class and a contributor finds a general deploy tool with one app wired into it.
//
// A recipe says what a stack IS, positively: its services, its static files, its routes, how it is
// backed up, and what inputs it needs. It never says what a stack lacks. A static site is a recipe
// with routes and no services, not a compose stack with its pieces missing, which is what the
// NoCompose and NoDatabase flags made it.
//
// A recipe never carries a value for an input it marks secret. It declares that it needs one by
// name, and the project supplies a reference to where it lives. See input.go.
package recipe

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// SchemaVersion is the only version this binary understands.
//
// An unknown version is refused rather than parsed leniently. Accepting a newer recipe and ignoring
// the fields it adds is silent partial application: the deploy succeeds, the thing the new field
// asked for never happens, and nobody finds out until the part that was quietly dropped mattered.
// Refusing costs an upgrade; the alternative costs a deployment nobody can explain.
const SchemaVersion = 1

// Recipe is one application's deployment declaration.
type Recipe struct {
	// Schema is the recipe format version. Required: a recipe without one cannot be checked for
	// compatibility, and guessing that a missing version means the current one is how an old file
	// silently becomes a new one.
	Schema int `json:"schema"`

	// Name identifies the recipe itself, e.g. "barako" or "wordpress". It is not the stack name:
	// an operator names their stack, and several stacks can run the same recipe.
	Name string `json:"name"`

	// Version is the recipe's own version, so a project can record which one it deployed. Free
	// text; semver is the obvious convention but nothing here parses it.
	Version string `json:"version,omitempty"`

	// Description is one line for `baryovm stack info`.
	Description string `json:"description,omitempty"`

	// Services are the containers this stack runs. Empty is legitimate: a static site has none.
	Services []Service `json:"services,omitempty"`

	// Files are static trees this stack serves or needs on disk.
	Files []Files `json:"files,omitempty"`

	// Routes are the hostnames this stack answers on. BaryoVM declares them; a proxy the VM
	// already runs serves them. This tool does not become a certificate manager.
	Routes []Route `json:"routes,omitempty"`

	// Backup is the list of strategies for this stack. An empty list is an explicit statement that
	// there is nothing to back up, which is what NoDatabase used to say with a negative flag.
	Backup []Strategy `json:"backup,omitempty"`

	// Inputs the recipe needs before it can be deployed. Values never appear here.
	Inputs []Input `json:"inputs,omitempty"`
}

// Service is one container in the stack.
type Service struct {
	Name string `json:"name"`

	// Image is a registry reference. Either Image or Build is set, never both: an image that is
	// pulled and an image that is built are different lifecycles, and a service claiming both
	// leaves "which one wins" to whoever reads the code next.
	Image string `json:"image,omitempty"`

	// Build is a path, relative to the project root, containing a Dockerfile.
	Build string `json:"build,omitempty"`

	// Ports are "host:container" mappings, as compose writes them.
	Ports []string `json:"ports,omitempty"`

	// HealthURL is probed after this service starts. Reaching one service's health endpoint is
	// what makes a rollback decidable.
	HealthURL string `json:"healthUrl,omitempty"`
}

// Files is a static tree: where it comes from locally and where it lands on the VM.
type Files struct {
	// From is a path relative to the project root. A trailing "/" copies the contents rather than
	// the directory, which is rsync's rule and the one that empties a webroot when got wrong.
	From string `json:"from"`
	To   string `json:"to"`
}

// Route is a hostname this stack answers on.
type Route struct {
	Host string `json:"host"`

	// Port is where the traffic goes on the VM. Required, because a route with no destination is
	// a hostname nobody serves.
	Port int `json:"port"`

	// Path scopes the route to a prefix. Empty means the whole host.
	Path string `json:"path,omitempty"`

	// TLS asks for a certificate from whatever proxy the VM runs. BaryoVM does not issue or renew
	// one: a tool that silently stops renewing is worse than one that never offered.
	TLS bool `json:"tls,omitempty"`
}

// Load reads a recipe from a path.
func Load(path string) (*Recipe, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read recipe %s: %w", path, err)
	}
	return Parse(b, path)
}

// Parse reads a recipe from bytes. path is used only in error messages.
func Parse(b []byte, path string) (*Recipe, error) {
	var r Recipe
	dec := json.NewDecoder(strings.NewReader(string(b)))
	// Unknown fields are an error for the same reason an unknown schema version is: a field this
	// binary does not know is one it will not act on, and a deploy that quietly skips part of what
	// was asked for is the failure this whole package is shaped to avoid.
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf("parse recipe %s: %w", path, err)
	}
	if err := r.Validate(path); err != nil {
		return nil, err
	}
	return &r, nil
}

// Validate checks a recipe is one this binary can act on completely.
func (r *Recipe) Validate(path string) error {
	if r.Schema == 0 {
		return fmt.Errorf("recipe %s has no schema version: add \"schema\": %d", path, SchemaVersion)
	}
	if r.Schema != SchemaVersion {
		return &ErrSchema{Path: path, Got: r.Schema, Want: SchemaVersion}
	}
	if strings.TrimSpace(r.Name) == "" {
		return fmt.Errorf("recipe %s has no name", path)
	}

	if len(r.Services) == 0 && len(r.Files) == 0 {
		return fmt.Errorf("recipe %s declares neither services nor files, so there is nothing to deploy", path)
	}

	seen := map[string]bool{}
	for i, s := range r.Services {
		if strings.TrimSpace(s.Name) == "" {
			return fmt.Errorf("recipe %s: service %d has no name", path, i)
		}
		if seen[s.Name] {
			return fmt.Errorf("recipe %s: two services named %q", path, s.Name)
		}
		seen[s.Name] = true

		switch {
		case s.Image == "" && s.Build == "":
			return fmt.Errorf("recipe %s: service %q has neither image nor build", path, s.Name)
		case s.Image != "" && s.Build != "":
			return fmt.Errorf("recipe %s: service %q has both image and build, so which one runs is undefined", path, s.Name)
		}
	}

	for i, f := range r.Files {
		if strings.TrimSpace(f.From) == "" || strings.TrimSpace(f.To) == "" {
			return fmt.Errorf("recipe %s: files %d needs both from and to", path, i)
		}
		if !strings.HasPrefix(f.To, "/") {
			return fmt.Errorf("recipe %s: files %d to %q must be an absolute path on the VM", path, i, f.To)
		}
	}

	for i, rt := range r.Routes {
		if strings.TrimSpace(rt.Host) == "" {
			return fmt.Errorf("recipe %s: route %d has no host", path, i)
		}
		if rt.Port <= 0 || rt.Port > 65535 {
			return fmt.Errorf("recipe %s: route %q needs a port between 1 and 65535, got %d", path, rt.Host, rt.Port)
		}
	}

	if err := validateStrategies(r.Backup, path); err != nil {
		return err
	}
	return validateInputs(r.Inputs, path)
}

// ErrSchema is returned for a recipe this binary cannot act on completely.
type ErrSchema struct {
	Path string
	Got  int
	Want int
}

func (e *ErrSchema) Error() string {
	if e.Got > e.Want {
		return fmt.Sprintf(
			"recipe %s is schema %d and this baryovm understands %d.\n"+
				"It was written for a newer version, and running it here would silently skip whatever\n"+
				"the newer version added. Upgrade baryovm, or use a recipe published for schema %d.",
			e.Path, e.Got, e.Want, e.Want)
	}
	return fmt.Sprintf(
		"recipe %s is schema %d and this baryovm understands %d.\n"+
			"Update the recipe to schema %d.", e.Path, e.Got, e.Want, e.Want)
}

// ServiceNames returns the service names in declaration order, for a command that needs to name
// them on a remote compose invocation.
func (r *Recipe) ServiceNames() []string {
	out := make([]string, 0, len(r.Services))
	for _, s := range r.Services {
		out = append(out, s.Name)
	}
	return out
}

// IsStatic reports whether this recipe runs no containers. It replaces the NoCompose flag, and it
// is derived rather than declared: a recipe with no services is static by construction, so the two
// cannot disagree the way a flag and the thing it described could.
func (r *Recipe) IsStatic() bool { return len(r.Services) == 0 }

// HasBackup reports whether anything is backed up. It replaces NoDatabase, derived the same way.
func (r *Recipe) HasBackup() bool { return len(r.Backup) > 0 }

// InputNames returns every declared input name, sorted, for an error that has to list what is
// missing.
func (r *Recipe) InputNames() []string {
	out := make([]string, 0, len(r.Inputs))
	for _, i := range r.Inputs {
		out = append(out, i.Name)
	}
	sort.Strings(out)
	return out
}
