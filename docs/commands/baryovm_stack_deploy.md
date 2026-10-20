## baryovm stack deploy

compose up -d the stack (optionally pull + recreate)

```
baryovm stack deploy <name> [flags]
```

### Examples

```
  baryovm stack deploy barako --force-recreate --service api,console
  baryovm stack deploy barako --pull
```

### Options

```
      --force-recreate    recreate containers even if unchanged
  -h, --help              help for deploy
      --no-deps           don't touch linked services
      --pull              pull images before recreating
      --service strings   limit to these services (comma-separated)
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm stack](baryovm_stack.md)	 - Manage docker compose stacks on your VMs
