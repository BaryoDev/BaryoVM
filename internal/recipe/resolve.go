// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package recipe

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// Ref is where a project says an input's value lives. It is never the value.
//
// This is the half of the contract the project owns. A recipe declares that it needs DB_PASSWORD;
// the project says DB_PASSWORD comes from the environment variable APP_DB_PASSWORD, or from a file
// on disk. BaryoVM reads it at deploy time, writes it into the remote config, and keeps nothing.
//
// A literal is not one of the sources, and that is the point. A project file is exactly the kind of
// thing that gets committed, and the moment a literal is accepted somebody commits one. Refusing it
// in the parser is cheaper than finding it in a repository later.
type Ref struct {
	// From names the source: "env" or "file".
	From string `json:"from"`

	// Name is the environment variable, for From == "env".
	Name string `json:"name,omitempty"`

	// Path is the file to read, for From == "file". Trailing whitespace is trimmed, because a
	// secret written with `echo` carries a newline and a trailing newline in a password is a
	// failure that looks like a wrong password.
	Path string `json:"path,omitempty"`

	// Value is refused. It exists in the struct only so a project containing one gets an error
	// that says why, rather than "unknown field value" from the decoder.
	Value string `json:"value,omitempty"`
}

// Bindings is a project's answer to a recipe's inputs: input name to where its value lives.
type Bindings map[string]Ref

// Resolved is the result of reading every binding. Values are held in memory for the length of one
// deploy and are never written to BaryoVM's own state.
type Resolved struct {
	values  map[string]string
	secrets map[string]bool
}

// Get returns a resolved value.
func (r *Resolved) Get(name string) (string, bool) {
	v, ok := r.values[name]
	return v, ok
}

// IsSecret reports whether a name was declared secret by the recipe, so a caller can keep it out of
// a log line or a JSON envelope.
func (r *Resolved) IsSecret(name string) bool { return r.secrets[name] }

// Names returns every resolved name, sorted.
func (r *Resolved) Names() []string {
	out := make([]string, 0, len(r.values))
	for k := range r.values {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Redacted renders the resolved set for a human or a log: secrets show as their length only, so a
// deploy can be debugged without the value appearing anywhere it could be read later.
func (r *Resolved) Redacted() map[string]string {
	out := make(map[string]string, len(r.values))
	for k, v := range r.values {
		if r.secrets[k] {
			out[k] = fmt.Sprintf("(secret, %d chars)", len(v))
			continue
		}
		out[k] = v
	}
	return out
}

// ValidateBindings checks a project's bindings before anything is read, so a malformed project
// fails before a deploy starts rather than partway through.
func ValidateBindings(b Bindings, path string) error {
	names := make([]string, 0, len(b))
	for name := range b {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		ref := b[name]
		if ref.Value != "" {
			return fmt.Errorf(
				"%s: input %q has a literal value.\n"+
					"A project file gets committed, so a secret written here is a secret in a\n"+
					"repository. Point at where the value lives instead:\n"+
					"\n"+
					"  %q: { \"from\": \"env\", \"name\": \"MY_%s\" }",
				path, name, name, name)
		}
		switch ref.From {
		case "env":
			if strings.TrimSpace(ref.Name) == "" {
				return fmt.Errorf("%s: input %q reads from env and names no variable", path, name)
			}
		case "file":
			if strings.TrimSpace(ref.Path) == "" {
				return fmt.Errorf("%s: input %q reads from a file and names no path", path, name)
			}
		case "":
			return fmt.Errorf("%s: input %q has no source: set \"from\" to \"env\" or \"file\"", path, name)
		default:
			return fmt.Errorf("%s: input %q has source %q, which is not \"env\" or \"file\"", path, name, ref.From)
		}
	}
	return nil
}

// Resolve reads every input a recipe declares, using the project's bindings.
//
// It reads nothing until it has checked that everything required is present. A deploy that gets
// halfway through resolving and then stops has already read secrets it did not need.
func Resolve(r *Recipe, b Bindings, projectPath string) (*Resolved, error) {
	if err := ValidateBindings(b, projectPath); err != nil {
		return nil, err
	}

	// A binding for an input the recipe does not declare is a mistake worth naming: usually a
	// rename in the recipe that the project has not followed, and silently ignoring it means the
	// value the operator thought they were supplying never arrives.
	declared := map[string]bool{}
	for _, in := range r.Inputs {
		declared[in.Name] = true
	}
	var stray []string
	for name := range b {
		if !declared[name] {
			stray = append(stray, name)
		}
	}
	if len(stray) > 0 {
		sort.Strings(stray)
		return nil, fmt.Errorf(
			"%s binds %d input(s) the recipe %q does not declare: %s.\n"+
				"The recipe declares: %s.\n"+
				"A binding nothing reads is usually a rename the project has not followed.",
			projectPath, len(stray), r.Name, strings.Join(stray, ", "),
			strings.Join(orNone(r.InputNames()), ", "))
	}

	// Check presence before reading anything.
	have := make(map[string]string, len(b))
	for name := range b {
		have[name] = ""
	}
	if missing := Missing(r.Inputs, have); len(missing) > 0 {
		return nil, fmt.Errorf("%s: %s", projectPath, ExplainMissing(missing))
	}

	out := &Resolved{
		values:  make(map[string]string, len(r.Inputs)),
		secrets: make(map[string]bool, len(r.Inputs)),
	}
	for _, in := range r.Inputs {
		out.secrets[in.Name] = in.Secret

		ref, bound := b[in.Name]
		if !bound {
			if in.Default != "" {
				out.values[in.Name] = in.Default
			}
			continue
		}

		v, err := read(ref)
		if err != nil {
			return nil, fmt.Errorf("%s: input %q: %w", projectPath, in.Name, err)
		}
		if v == "" && in.IsRequired() {
			return nil, fmt.Errorf(
				"%s: input %q resolved to an empty value from %s.\n"+
					"An empty required input starts a stack that fails later and less clearly, so it\n"+
					"is refused here.", projectPath, in.Name, describe(ref))
		}
		out.values[in.Name] = v
	}
	return out, nil
}

func read(ref Ref) (string, error) {
	switch ref.From {
	case "env":
		return os.Getenv(ref.Name), nil
	case "file":
		b, err := os.ReadFile(ref.Path)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", ref.Path, err)
		}
		// A secret written with `echo` carries a newline, and a trailing newline in a password is
		// a failure that presents as a wrong password.
		return strings.TrimRight(string(b), "\r\n"), nil
	default:
		return "", fmt.Errorf("unknown source %q", ref.From)
	}
}

func describe(ref Ref) string {
	switch ref.From {
	case "env":
		return "environment variable " + ref.Name
	case "file":
		return "file " + ref.Path
	default:
		return "source " + ref.From
	}
}

func orNone(ss []string) []string {
	if len(ss) == 0 {
		return []string{"(none)"}
	}
	return ss
}
