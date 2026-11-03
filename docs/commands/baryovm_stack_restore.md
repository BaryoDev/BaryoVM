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
      --strict-host-keys   accepted for compatibility and does nothing: unknown hosts are refused by default (BARYOVM_STRICT_HOST_KEYS is likewise ignored)
      --trust-new-host     learn the key of a host not seen before, on this run only (also BARYOVM_TRUST_NEW_HOST=1); a changed key is always refused
```

### SEE ALSO

* [baryovm stack](baryovm_stack.md)	 - Manage docker compose stacks on your VMs
