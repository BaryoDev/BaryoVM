## baryovm project release

Release every stack of a project file, in order, with no vm add or stack add

### Synopsis

Reads the project file and nothing else: not fleet.json, so a person and a CI job release the same
thing. Stacks are released in the file's order, and the first failed release or verify stops the
rest. Every VM needs a pinned hostKey in the file, or --strict-host-keys with its key already
known: a project release never trusts a host on first use.

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
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm project](baryovm_project.md)	 - Release from a committed project file, or write one from your fleet
