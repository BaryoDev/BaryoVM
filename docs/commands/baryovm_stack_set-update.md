## baryovm stack set-update

Set a stack's update policy (autoUpdate, health URL)

```
baryovm stack set-update <name> [flags]
```

### Examples

```
  baryovm stack set-update playground --health-url http://127.0.0.1:5005/health --auto
  baryovm stack set-update club --health-url http://127.0.0.1:8091/health --no-auto
```

### Options

```
      --auto                allow unattended updates (requires --health-url)
      --health-url string   URL probed from the VM after an update, e.g. http://127.0.0.1:8091/health
  -h, --help                help for set-update
      --no-auto             disallow unattended updates
      --no-database         this stack has no database, so --auto may update it with no backup (--no-database=false to undo)
      --service strings     limit updates to these services
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm stack](baryovm_stack.md)	 - Manage docker compose stacks on your VMs
