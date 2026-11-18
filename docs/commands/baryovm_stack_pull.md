## baryovm stack pull

Pull the stack's images

```
baryovm stack pull <name> [flags]
```

### Options

```
  -h, --help              help for pull
      --service strings   limit to these services (comma-separated)
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   always refuse an unknown host; =false learns its key on first use instead of asking (also BARYOVM_STRICT_HOST_KEYS=1 or 0)
```

### SEE ALSO

* [baryovm stack](baryovm_stack.md)	 - Manage docker compose stacks on your VMs
