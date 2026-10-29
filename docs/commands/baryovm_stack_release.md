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
      --adopt           take over what BaryoVM did not create: a hand-run container holding a container_name (removed, refused if it has a named volume not declared external), and with a tls block a vhost file (backed up first)
      --clear           drop the record of an earlier release that stopped, without running anything
      --config string   release manifest path (overrides the stack's --release-file)
  -h, --help            help for release
      --no-backup       skip the automatic pre-release DB backup
      --no-build        skip building images (just sync + compose up)
      --resume          continue an earlier release that stopped, from the first step not done, reusing its backup
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm stack](baryovm_stack.md)	 - Manage docker compose stacks on your VMs
