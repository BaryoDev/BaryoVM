## baryovm repo init

Scaffold release CI from a template and protect the default branch

### Synopsis

Writes the release workflows and the repository furniture (CODEOWNERS, issue templates,
CONTRIBUTING) from a template into a local checkout, applies branch protection with the template's
required checks through the GitHub API, and prints the values to enter on the registry for
trusted publishing. Without --yes (or with --dry-run) it prints that plan and does nothing: no
file written, no GitHub call, no token read.

Built-in templates: dotnet-nuget (nuget.org) and node-npm (npmjs.com). --templates-dir names a
directory whose subdirectories are templates of your own (template.json plus a files/ tree); one
with the same name as a built-in replaces it. Text in the files may use @@OWNER@@, @@REPO@@,
@@FULL_NAME@@ and @@CODEOWNER@@.

A file that already exists is never overwritten without --force: it is listed as "exists" and left
alone, so a rerun after a half finished first run is safe.

Branch protection requires the template's checks (--checks replaces them), an up to date branch,
and a code owner review (--approvals sets the number of approving reviews, default 0). Admins can
still push. A branch that already has protection is left alone and reported as "protection exists,
not changed": the API call replaces the whole rule, so applying it again would reset what someone
hardened by hand (admin enforcement, push restrictions, signatures). --replace-protection replaces
it. A file path that leaves the checkout through a symlinked folder is refused, and a dry run shows
it. It uses the token from --token-ref, else GITHUB_TOKEN, GH_TOKEN, or gh auth token.

BaryoVM does not create the trusted publishing policy. nuget.org and npm create it through their
web pages and have no API for it, so it is yours to do once, by hand. The order that works:

  1. init writes the workflow and protects the branch
  2. commit and push the files, so the workflow exists on the default branch
  3. create the policy on the registry with the values init printed
  4. baryovm repo verify <owner/repo> --template <name> says whether it took

Creating the policy before step 2 names a workflow file that is not there yet, and nothing
complains until the first publish.

```
baryovm repo init <owner/repo> [flags]
```

### Examples

```
  baryovm repo init acme/widgets --template dotnet-nuget
  baryovm repo init acme/widgets --template node-npm --path ~/src/widgets --yes
```

### Options

```
      --approvals int          approving reviews required, besides the code owner review
      --branch string          the branch to protect (default the repository's default branch)
      --checks strings         required status checks, replacing the template's (repeatable)
      --codeowner string       CODEOWNERS handle, @user or @org/team (default @<owner>)
      --dry-run                print the plan and do nothing (this is already the default without --yes)
      --force                  overwrite files that already exist
  -h, --help                   help for init
      --path string            the local checkout to write into (default ".")
      --replace-protection     replace branch protection that already exists (default: leave it alone and say so)
      --template string        template name: dotnet-nuget, node-npm, or one from --templates-dir (required)
      --templates-dir string   a directory of your own templates; one named like a built-in replaces it
      --token-ref string       GitHub token reference: env:NAME, file:PATH or cmd:<command> (default GITHUB_TOKEN, GH_TOKEN, gh auth token)
      --yes                    really write the files and call GitHub; without it the plan is printed and nothing is done
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm repo](baryovm_repo.md)	 - Move a repository, scaffold its release CI, and verify trusted publishing
