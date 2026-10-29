// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/guard"
	"github.com/spf13/cobra"
)

// publishExec runs npm to list the files a package would ship. Tests replace it.
var publishExec guard.Exec = runCapture

// runCapture runs a command in dir and returns its stdout. stderr is kept out of the JSON and
// shown only in the error.
func runCapture(dir, name string, args []string) (string, error) {
	cmd := exec.Command(name, args...) // #nosec G204 -- name is "npm", set by the wrapper command
	cmd.Dir = dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return string(out), fmt.Errorf("%s", msg)
		}
		return string(out), err
	}
	return string(out), nil
}

func newPublishCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "publish",
		Short: "Publish an npm or NuGet package, refusing one that holds a key or aims at the wrong registry",
	}
	cmd.AddCommand(newPublishNpmCmd(), newPublishNuGetCmd())
	return cmd
}

func newPublishNpmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "npm <npm publish arguments>",
		Short: "Run npm publish after scanning the package and checking the registry",
		Long: strings.TrimSpace(`
Runs npm publish with the same arguments, after two checks:

  files     npm pack --dry-run --json lists what would ship, and every file is
            scanned for keys
  registry  the registry npm would publish to (publishConfig, then --registry,
            npm_config_registry, the project .npmrc and the user .npmrc, then
            registry.npmjs.org) is checked against the allowed registries

A package marked "private": true that would go to the public registry is
refused, with the registry named. A refusal names the file and line, never the
value, and npm publish is not run. A tarball argument and workspace flags are
refused because they cannot be checked.

The allowed registries come from git config (one --add per host) and the
BARYOVM_ALLOWED_REGISTRIES environment variable (comma separated). With neither
set, any registry is allowed:

  git config --add baryovm.allowedRegistries registry.npmjs.org

Flags after "npm" belong to npm, so this command takes none of its own. For
JSON output of a refusal set BARYOVM_OUTPUT=json. OTP and Trusted Publishing
stay with you.
`),
		Example:            "  baryovm publish npm --access public\n  baryovm publish npm ./packages/core",
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
				return cmd.Help()
			}
			pol, err := loadRegistryPolicy(nil)
			if err != nil {
				return finishGuard(policyRefusal("publish npm", err), "npm", append([]string{"publish"}, args...))
			}
			nc := guard.NpmContext{EnvRegistry: os.Getenv("npm_config_registry"), UserConfig: userNpmrc()}
			if nc.EnvRegistry == "" {
				nc.EnvRegistry = os.Getenv("NPM_CONFIG_REGISTRY")
			}
			v := guard.CheckNpmPublish(guardRunner, publishExec, args, pol, nc)
			return finishGuard(v, "npm", append([]string{"publish"}, args...))
		},
	}
}

func userNpmrc() string {
	if p := os.Getenv("NPM_CONFIG_USERCONFIG"); p != "" {
		return p
	}
	if p := os.Getenv("npm_config_userconfig"); p != "" {
		return p
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".npmrc")
	}
	return ""
}

func newPublishNuGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "nuget <dotnet nuget push arguments>",
		Short: "Run dotnet nuget push after scanning the package and checking the source",
		Long: strings.TrimSpace(`
Runs dotnet nuget push with the same arguments, after two checks:

  files   the .nupkg is opened as a zip and every entry is scanned for keys
  source  --source (or -s), and --symbol-source, are checked against the allowed
          registries; with no --source the default is api.nuget.org

An entry over the size limit is refused, not skipped. A source that is a name
from NuGet.Config cannot be resolved here and is refused while a registry policy
is set: pass the feed URL. A folder source is always allowed. A refusal names
the file and line, never the value, and dotnet is not run.

The allowed registries come from git config (one --add per host) and the
BARYOVM_ALLOWED_REGISTRIES environment variable (comma separated). With neither
set, any source is allowed:

  git config --add baryovm.allowedRegistries pkgs.dev.azure.com

Flags after "nuget" belong to dotnet, so this command takes none of its own. For
JSON output of a refusal set BARYOVM_OUTPUT=json.
`),
		Example:            "  baryovm publish nuget bin/Release/App.1.0.0.nupkg -s https://api.nuget.org/v3/index.json -k $KEY",
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
				return cmd.Help()
			}
			full := append([]string{"nuget", "push"}, args...)
			pol, err := loadRegistryPolicy(nil)
			if err != nil {
				return finishGuard(policyRefusal("publish nuget", err), "dotnet", full)
			}
			return finishGuard(guard.CheckNuGetPush(guardRunner, args, pol), "dotnet", full)
		},
	}
}
