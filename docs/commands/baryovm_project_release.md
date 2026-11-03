## baryovm project release

Release every stack of a project file, in order, with no vm add or stack add

### Synopsis

Reads the project file and nothing else: not fleet.json, so a person and a CI job release the same
thing. Stacks are released in the file's order, and the first failed release or verify stops the
rest. Every VM needs a pinned hostKey in the file, or its key already known: a project release
never trusts a host on first use, and --trust-new-host does not change that.

```
baryovm project release [flags]
```

### Examples

```
  baryovm project release
  baryovm project release --file deploy/baryovm.project.json --no-backup
```

### Options

```
      --adopt stack release --adopt   take over what BaryoVM did not create: see stack release --adopt
      --file string                   project file to release from (default "baryovm.project.json")
  -h, --help                          help for release
      --no-backup                     skip the automatic pre-release DB backup
      --no-build                      skip building images (just sync + compose up)
      --release-root string           let every manifest reach inside <dir> as well as its own directory (absolute; from the command line only, never the file). See USAGE.md
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   accepted for compatibility and does nothing: unknown hosts are refused by default (BARYOVM_STRICT_HOST_KEYS is likewise ignored)
      --trust-new-host     learn the key of a host not seen before, on this run only (also BARYOVM_TRUST_NEW_HOST=1); a changed key is always refused
```

### SEE ALSO

* [baryovm project](baryovm_project.md)	 - Release from a committed project file, or write one from your fleet
