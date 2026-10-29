## baryovm ship

Run the release gate, tag, watch the publish run, verify the registry, create the GitHub release

### Synopsis

Checks that the release is safe (clean tree on the default branch and synced, version not
already tagged or published, changelog section for that version, release notes), then tags
and pushes the tag, watches the workflow run, asserts its publish step ran and was not
skipped, polls the registry until every package resolves, reads the published artifact's
metadata against the spec's assertions, and creates the GitHub release.

BaryoVM never publishes and never holds a registry credential: the repository's own workflow
publishes, and BaryoVM only reads the registry. The GitHub token comes from --token-ref.
A rerun after an interrupted watch finds the tag at HEAD and picks up from the watch.
What was asserted, with the values seen, is saved under $BARYOVM_HOME/ship/<name>/.

```
baryovm ship <name> [flags]
```

### Examples

```
  baryovm ship shop --dry-run
  baryovm ship shop --yes -o json
```

### Options

```
      --dry-run            run every preflight check and print the plan; tag and push nothing
      --file string        publish spec JSON (default: the stack's setting, then baryovm.publish.json)
  -h, --help               help for ship
      --repo string        repository directory (default ".")
      --timeout duration   how long to watch the workflow run (default 30m0s)
      --token-ref string   secret reference for the GitHub token (env:, file:, cmd:) (default "cmd:gh auth token")
      --version string     version to release (default: the one the project file declares)
      --yes                skip the confirmation before tagging
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm](baryovm.md)	 - Provision VMs and deploy apps to your own boxes
