## baryovm project export

Write a project file from the stacks registered on this machine

### Synopsis

Turns a hand-registered fleet into a file that can be committed. Release manifest paths under the
output directory become relative to it. The key path becomes an env:NAME reference, and a host key
already in known_hosts is pinned. Whatever cannot be made shareable is refused with the field named.

```
baryovm project export [flags]
```

### Examples

```
  baryovm project export --out deploy/baryovm.project.json --stack api --stack console
```

### Options

```
      --force            replace the output file if it exists
  -h, --help             help for export
      --key-env string   environment variable that holds the key path in CI (default DEPLOY_KEY_PATH)
      --name string      project name (default: the output directory's name)
      --out string       file to write (default "baryovm.project.json")
      --stack strings    stack to include, in release order (repeatable; default: all)
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm project](baryovm_project.md)	 - Release from a committed project file, or write one from your fleet
