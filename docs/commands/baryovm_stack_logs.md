## baryovm stack logs

Show recent logs for the stack

```
baryovm stack logs <name> [flags]
```

### Options

```
  -h, --help              help for logs
      --service strings   limit to these services (comma-separated)
      --tail int          lines per service (default 100)
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm stack](baryovm_stack.md)	 - Manage docker compose stacks on your VMs
