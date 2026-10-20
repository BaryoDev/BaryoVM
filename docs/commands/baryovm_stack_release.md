## baryovm stack release

Sync source to the VM, build images there, then compose up (config-driven)

```
baryovm stack release <name> [flags]
```

### Examples

```
  baryovm stack release baryoclub
  baryovm stack release baryoclub --config ./baryovm.release.json --no-backup
```

### Options

```
      --config string   release manifest path (overrides the stack's --release-file)
  -h, --help            help for release
      --no-backup       skip the automatic pre-release DB backup
      --no-build        skip building images (just sync + compose up)
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm stack](baryovm_stack.md)	 - Manage docker compose stacks on your VMs
