// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"fmt"
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/secretref"
)

// cmdResolver resolves references typed on the command line, where cmd: is allowed because the
// operator wrote it.
func cmdResolver() secretref.Resolver { return secretref.Default().WithCmd() }

// fileResolver resolves references read from a committed file (a release manifest) whose directory
// is root: no cmd:, env: names start with BARYOVM_, file: paths stay inside root.
func fileResolver(root string) secretref.Resolver { return secretref.Default().Confine(root) }

// buildEnv merges -e KEY=value and --env-ref KEY=<reference> into one environment. Every reference
// is resolved here, before the caller dials anything, so one that does not resolve stops the
// command with nothing sent. With resolve false (a dry run) the references are only checked for
// syntax and their keys get a placeholder: a dry run never reads a secret.
//
// Errors name the key or the reference, never a value.
func buildEnv(envs, refs []string, resolve bool) (map[string]string, error) {
	env, err := parseEnv(envs)
	if err != nil {
		return nil, err
	}
	r := cmdResolver()
	for _, p := range refs {
		k, ref, ok := strings.Cut(p, "=")
		if !ok || k == "" || ref == "" {
			return nil, fmt.Errorf("invalid --env-ref %q, expected KEY=env:NAME, KEY=file:PATH or KEY=cmd:<command>", p)
		}
		if _, dup := env[k]; dup {
			return nil, fmt.Errorf("%s is set twice: give it once, with -e or --env-ref", k)
		}
		if !resolve {
			if _, err := secretref.Parse(ref); err != nil {
				return nil, err
			}
			env[k] = ""
			continue
		}
		v, err := r.Resolve(ref)
		if err != nil {
			return nil, fmt.Errorf("--env-ref %s: %w", k, err)
		}
		env[k] = v
	}
	return env, nil
}
