## baryovm stack backup

Back up the stack's database + config (pg_dump + .env)

```
baryovm stack backup <name> [flags]
```

### Options

```
  -h, --help   help for backup
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm stack](baryovm_stack.md)	 - Manage docker compose stacks on your VMs
