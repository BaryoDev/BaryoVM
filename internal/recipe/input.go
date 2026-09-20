// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package recipe

import (
	"fmt"
	"regexp"
	"strings"
)

// Input is something a recipe needs before it can be deployed, declared by name and never by value.
//
// This is what makes a recipe publishable. WordPress needs a database password and Umbraco needs a
// connection string; a recipe that cannot say "I need a password" is a recipe only its author can
// use. Saying it by name costs nothing and lets a stranger deploy the same stack.
//
// A recipe never carries the value. The project supplies a reference to where it lives, and BaryoVM
// resolves that reference at deploy time and writes it into the remote config without storing it.
// That is the first rule of this tool kept rather than bent: it holds no secrets.
type Input struct {
	// Name is the variable the stack reads, e.g. DB_PASSWORD.
	Name string `json:"name"`

	// Description is shown when the input is missing, so the error says what the thing is for
	// rather than only what it is called.
	Description string `json:"description,omitempty"`

	// Secret marks a value that must never be logged, echoed, or written anywhere but the remote
	// config. It also forbids a default: a default secret is a shared secret.
	Secret bool `json:"secret,omitempty"`

	// Default is used when the project does not supply the input. Not allowed for a secret.
	Default string `json:"default,omitempty"`

	// Required fails the deploy when the input is absent and has no default. Defaults to true for
	// a secret, because a stack that starts without its password fails later and less clearly.
	Required *bool `json:"required,omitempty"`

	// Example is shown in the error, e.g. "https://example.com". Never a real value.
	Example string `json:"example,omitempty"`
}

// IsRequired reports whether a deploy must have this input.
func (i Input) IsRequired() bool {
	if i.Required != nil {
		return *i.Required
	}
	// A secret with no explicit answer is required. The alternative, defaulting to optional, means
	// a missing password produces a stack that starts and then fails somewhere less obvious.
	return i.Secret || i.Default == ""
}

// nameRe is the shape of an environment variable name. Inputs land in a remote config file as
// KEY=value, so a name with a space, an equals sign or a newline in it is not a name, it is an
// injection into the file being written.
var nameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func validateInputs(ins []Input, path string) error {
	seen := map[string]bool{}
	for i, in := range ins {
		if strings.TrimSpace(in.Name) == "" {
			return fmt.Errorf("recipe %s: input %d has no name", path, i)
		}
		if !nameRe.MatchString(in.Name) {
			return fmt.Errorf(
				"recipe %s: input %q is not a usable variable name.\n"+
					"Inputs are written into the remote config as NAME=value, so a name has to be\n"+
					"letters, digits and underscores, starting with a letter or underscore.",
				path, in.Name)
		}
		if seen[in.Name] {
			return fmt.Errorf("recipe %s: two inputs named %q", path, in.Name)
		}
		seen[in.Name] = true

		// A default for a secret is a secret in the recipe, which is a file in a public repository.
		// This is the rule the whole design rests on, so it is refused rather than warned about.
		if in.Secret && in.Default != "" {
			return fmt.Errorf(
				"recipe %s: input %q is secret and has a default.\n"+
					"A default secret is a shared secret, and a recipe is published. Declare it with\n"+
					"no default and let the project supply a reference.", path, in.Name)
		}
	}
	return nil
}

// Missing returns the inputs a deploy does not have, given the names it does have. The caller
// resolves references; this package only knows what was asked for.
func Missing(ins []Input, have map[string]string) []Input {
	var out []Input
	for _, in := range ins {
		if _, ok := have[in.Name]; ok {
			continue
		}
		if in.Default != "" {
			continue
		}
		if in.IsRequired() {
			out = append(out, in)
		}
	}
	return out
}

// ExplainMissing renders the missing inputs as something an operator can act on, naming each one,
// what it is for, and where a value could come from.
func ExplainMissing(missing []Input) string {
	if len(missing) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "this recipe needs %d input(s) the project does not supply:\n", len(missing))
	for _, in := range missing {
		fmt.Fprintf(&b, "\n  %s", in.Name)
		if in.Secret {
			b.WriteString("  (secret)")
		}
		b.WriteString("\n")
		if in.Description != "" {
			fmt.Fprintf(&b, "    %s\n", in.Description)
		}
		if in.Example != "" {
			fmt.Fprintf(&b, "    example: %s\n", in.Example)
		}
	}
	b.WriteString("\nSupply each one in the project as a reference, for example:\n")
	b.WriteString("\n  \"" + missing[0].Name + "\": { \"from\": \"env\", \"name\": \"MY_" + missing[0].Name + "\" }\n")
	return b.String()
}
