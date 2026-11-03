## baryovm stack publish

Run the release gate, then tag and push the tag

### Synopsis

The same gate as `baryovm ship`, then the tag and its push, and nothing after: the
repository's workflow publishes. Use `baryovm ship` to also watch, verify and release.

```
baryovm stack publish <name> [flags]
```

### Examples

```
  baryovm stack publish shop --dry-run
  baryovm stack publish shop --yes
```

### Options

```
      --dry-run            run every preflight check and print the plan; tag and push nothing
      --file string        publish spec JSON (default: the stack's setting, then baryovm.publish.json)
  -h, --help               help for publish
      --repo string        repository directory (default ".")
      --timeout duration   how long to watch the workflow run (default 30m0s)
      --token-ref string   secret reference for the GitHub token (env:, file:, cmd:) (default "cmd:gh auth token")
      --version string     version to release (default: the one the project file declares)
      --yes                skip the confirmation before tagging
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   accepted for compatibility and does nothing: unknown hosts are refused by default (BARYOVM_STRICT_HOST_KEYS is likewise ignored)
      --trust-new-host     learn the key of a host not seen before, on this run only (also BARYOVM_TRUST_NEW_HOST=1); a changed key is always refused
```

### SEE ALSO

* [baryovm stack](baryovm_stack.md)	 - Manage docker compose stacks on your VMs
