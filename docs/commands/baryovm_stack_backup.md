## baryovm stack backup

Back up the stack's database + config (pg_dump + .env)

```
baryovm stack backup <name> [flags]
```

### Options

```
      --dry-run   print the backup script without connecting
  -h, --help      help for backup
      --pull      after the backup, copy this run's files to ~/.baryovm/backups/<stack>/ on this machine (owner-only). Not available on a forced-command VM
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   accepted for compatibility and does nothing: unknown hosts are refused by default (BARYOVM_STRICT_HOST_KEYS is likewise ignored)
      --trust-new-host     learn the key of a host not seen before, on this run only (also BARYOVM_TRUST_NEW_HOST=1); a changed key is always refused
```

### SEE ALSO

* [baryovm stack](baryovm_stack.md)	 - Manage docker compose stacks on your VMs
