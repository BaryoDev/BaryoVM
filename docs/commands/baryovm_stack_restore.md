## baryovm stack restore

Restore the stack's database from a backup (REPLACES current data)

```
baryovm stack restore <name> [flags]
```

### Examples

```
  baryovm stack restore baryoclub --yes
  baryovm stack restore baryoclub --file db-20260717-011514.dump --yes
  baryovm stack restore baryoclub --drill
```

### Options

```
      --drill         prove the backup restores: load it into a throwaway postgres container, check it, remove the container. Touches nothing live, needs no --yes
      --dry-run       print the restore script and the backup it would use, without connecting (needs no --yes)
      --file string   backup to restore (name or path); default: newest
  -h, --help          help for restore
      --yes           confirm the destructive restore
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   always refuse an unknown host; =false learns its key on first use instead of asking (also BARYOVM_STRICT_HOST_KEYS=1 or 0). A project release never learns or asks, even with =false
```

### SEE ALSO

* [baryovm stack](baryovm_stack.md)	 - Manage docker compose stacks on your VMs
