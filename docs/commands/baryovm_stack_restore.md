## baryovm stack restore

Restore the stack's database from a backup (REPLACES current data)

```
baryovm stack restore <name> [flags]
```

### Examples

```
  baryovm stack restore baryoclub --yes
  baryovm stack restore baryoclub --file db-20260717-011514.dump --yes
```

### Options

```
      --file string   backup to restore (name or path); default: newest
  -h, --help          help for restore
      --yes           confirm the destructive restore
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm stack](baryovm_stack.md)	 - Manage docker compose stacks on your VMs
