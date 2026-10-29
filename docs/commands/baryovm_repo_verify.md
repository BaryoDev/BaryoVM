## baryovm repo verify

Run the publish workflow's dry run and report whether trusted publishing works

### Synopsis

Starts the publish workflow by hand with its dry run switch on, waits for the run to finish, and
reports whether the OIDC exchange step succeeded. The dry run publishes nothing: the exchange only
asks the registry for a short-lived credential, which is exactly what fails when the trusted
publishing policy is missing or does not match.

The workflow must be on the default branch (or --ref) first. The workflow file and the step names
come from --template; --workflow and --step override them for a workflow of your own. The verdict
is "took" when every OIDC step succeeded, "did-not-take" when one failed (the four values to check
are printed again), and "inconclusive" when the run ended before reaching the step or did not
finish within --timeout. Only "took" exits zero.

It uses the token from --token-ref, else GITHUB_TOKEN, GH_TOKEN, or gh auth token. The token needs
the actions write permission on the repository.

```
baryovm repo verify <owner/repo> [flags]
```

### Examples

```
  baryovm repo verify acme/widgets --template dotnet-nuget
  baryovm repo verify acme/widgets --workflow release.yml --step 'Get a NuGet key' -o json
```

### Options

```
  -h, --help                   help for verify
      --ref string             the branch to run it on (default the repository's default branch)
      --step strings           an OIDC step name that must succeed (repeatable; default from the template)
      --template string        the template the repository was scaffolded from: gives the workflow file and step names
      --templates-dir string   a directory of your own templates
      --timeout duration       how long to wait for the run to finish (default 15m0s)
      --token-ref string       GitHub token reference: env:NAME, file:PATH or cmd:<command> (default GITHUB_TOKEN, GH_TOKEN, gh auth token)
      --workflow string        the publish workflow file name (default from the template)
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm repo](baryovm_repo.md)	 - Move a repository, scaffold its release CI, and verify trusted publishing
