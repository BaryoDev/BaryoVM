## baryovm stack rollback

Put back the images the stack ran before its last release or update

### Synopsis

Retags the images recorded before the last `stack release` or `stack update` and recreates the
containers from them. Images only: the database is never touched. The matching database backups
are printed, and `stack restore --yes` stays the explicit word for the data.

```
baryovm stack rollback <name> [flags]
```

### Examples

```
  baryovm stack rollback app --list
  baryovm stack rollback app
```

### Options

```
  -h, --help   help for rollback
      --list   show what there is to roll back to, change nothing
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   accepted for compatibility and does nothing: unknown hosts are refused by default (BARYOVM_STRICT_HOST_KEYS is likewise ignored)
      --trust-new-host     learn the key of a host not seen before, on this run only (also BARYOVM_TRUST_NEW_HOST=1); a changed key is always refused
```

### SEE ALSO

* [baryovm stack](baryovm_stack.md)	 - Manage docker compose stacks on your VMs
