## baryovm stack deploy

compose up -d the stack (optionally pull + recreate)

```
baryovm stack deploy <name> [flags]
```

### Examples

```
  baryovm stack deploy app --force-recreate --service api,console
  baryovm stack deploy app --pull
```

### Options

```
      --adopt             remove a container started by hand that holds a container_name the compose file wants (docker rm -f, NOT recoverable: its writable layer and anonymous volumes are lost; refused if it uses a named volume the compose file does not declare external)
      --dry-run           print the compose commands without connecting
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
