// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/guard"
	"github.com/BaryoDev/BaryoVM/internal/ui"
	"github.com/spf13/cobra"
)

// The seams to the cloud CLIs and the file system. Tests replace them, so no test runs wrangler
// or touches a real project.
var (
	cloudExec    guard.Exec    = runCapture
	cloudFS      guard.CloudFS = osCloudFS{}
	cloudTempDir               = makeTempDir
)

type osCloudFS struct{}

func (osCloudFS) ListFiles(dir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	return out, err
}

func (osCloudFS) Exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func (osCloudFS) Getwd() (string, error) { return os.Getwd() }

func makeTempDir() (string, func(), error) {
	dir, err := os.MkdirTemp("", "baryovm-bundle-")
	if err != nil {
		return "", nil, err
	}
	return dir, func() { _ = os.RemoveAll(dir) }, nil
}

func cloudEnv() guard.CloudEnv {
	return guard.CloudEnv{R: guardRunner, FS: cloudFS, Run: cloudExec, TempDir: cloudTempDir}
}

// loadCloudPolicy reads the allowed cloud projects from git config and
// BARYOVM_ALLOWED_CLOUD_PROJECTS, with the same rule as the other policies: unset means no
// restriction, an unreadable config refuses.
func loadCloudPolicy(globals []string) (guard.Policy, error) {
	values, err := gitConfigValues(globals, guard.CloudProjectPolicyKey, "BARYOVM_ALLOWED_CLOUD_PROJECTS")
	if err != nil {
		return guard.Policy{}, err
	}
	return guard.Policy{AllowedCloudProjects: guard.ParseCloudProjects(values)}, nil
}

func newCloudCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cloud",
		Short: "Run wrangler, firebase or gcloud, refusing a deploy that ships a key or aims at the wrong project",
	}
	cmd.AddCommand(newCloudWranglerCmd(), newCloudFirebaseCmd(), newCloudGcloudCmd())
	return cmd
}

func newCloudWranglerCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "wrangler <wrangler arguments>",
		Short: "Run wrangler after scanning the Worker bundle and its config for keys",
		Long: strings.TrimSpace(`
Runs wrangler with the same arguments. For deploy, publish and versions upload it
first checks three things:

  bundle     wrangler <command> --dry-run --outdir <scratch> builds the Worker, and
             every file in the bundle is scanned for keys
  config     wrangler.toml, wrangler.json or wrangler.jsonc (or the file named by
             --config) is scanned; values under vars are stored in clear text
  arguments  a key passed as --var NAME:value is refused for the same reason

A key is refused with the file and line, never the value, and the message points at
"wrangler secret put NAME". If the dry run fails, the deploy is refused. "pages
deploy <dir>" and "pages publish <dir>" scan the directory; --assets and the config assets directory are scanned too. Other wrangler commands pass through. Put
options after the command: a leading option is refused. --outdir is set by
baryovm and cannot be given. With --dry-run nothing is sent, so nothing is checked.

Flags after "wrangler" belong to wrangler, so this command takes none of its own.
For JSON output of a refusal set BARYOVM_OUTPUT=json.
`),
		Example:            "  baryovm cloud wrangler deploy --env production",
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
				return cmd.Help()
			}
			return finishGuard(guard.CheckWrangler(cloudEnv(), args), "wrangler", args)
		},
	}
}

func newCloudFirebaseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "firebase <firebase arguments>",
		Short: "Run firebase after scanning the files it deploys and checking the project",
		Long: strings.TrimSpace(`
Runs firebase with the same arguments. For deploy and hosting:channel:deploy it
first checks two things:

  project  the project id is read from --project (an id or an alias from
           .firebaserc), then the default in .firebaserc, printed, and checked
           against the allowed cloud projects; one that is not on the list is
           refused before firebase runs. If the id cannot be determined the deploy
           is refused. While a project list is set, --project is required, because
           the active project that "firebase use" keeps outside the repository
           cannot be read here
  files    every file in the hosting public directories and functions sources
           named in firebase.json is scanned, except files matched by that entry's
           ignore globs. A framework source is refused: what it builds cannot be listed.
           An entry with predeploy is refused: build first, then remove predeploy

A key is refused with the file and line, never the value. Other firebase commands
pass through. Put options after the command: a leading option is refused.

The allowed projects come from git config (one --add per project id) and the
BARYOVM_ALLOWED_CLOUD_PROJECTS environment variable (comma separated). With
neither set, any project is allowed:

  git config --add baryovm.allowedCloudProjects my-project-id

Flags after "firebase" belong to firebase, so this command takes none of its own.
For JSON output of a refusal set BARYOVM_OUTPUT=json.
`),
		Example:            "  baryovm cloud firebase deploy --only hosting --project my-project-id",
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
				return cmd.Help()
			}
			pol, err := loadCloudPolicy(nil)
			if err != nil {
				return finishGuard(policyRefusal("cloud firebase", err), "firebase", args)
			}
			res := guard.CheckFirebase(cloudEnv(), args, pol)
			if res.Project != "" {
				ui.Detail("firebase project", res.Project)
			}
			return finishGuard(res.Verdict, "firebase", args)
		},
	}
}

func newCloudGcloudCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "gcloud <gcloud arguments>",
		Short: "Run gcloud, refusing a command that writes a key file inside a git working tree",
		Long: strings.TrimSpace(`
Runs gcloud with the same arguments. Everything passes through except commands that
write a key or secret to a file:

  iam service-accounts keys create <file>
  secrets versions access ... --out-file <file>
  kms decrypt ... --plaintext-file <file>

(with or without alpha or beta). If that file is inside a git working tree, the
command is refused before gcloud runs, and the message says to write it outside the
repository. "-" and /dev/stdout are allowed. If a command looks like key creation
but cannot be read, or git cannot say whether the path is in a repository, it is
refused.

Flags after "gcloud" belong to gcloud, so this command takes none of its own.
For JSON output of a refusal set BARYOVM_OUTPUT=json.
`),
		Example:            "  baryovm cloud gcloud iam service-accounts keys create ~/.config/key.json --iam-account sa@p.iam.gserviceaccount.com",
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
				return cmd.Help()
			}
			return finishGuard(guard.CheckGcloud(cloudEnv(), args), "gcloud", args)
		},
	}
}
