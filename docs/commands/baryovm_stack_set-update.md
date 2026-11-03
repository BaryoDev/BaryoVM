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
      --strict-host-keys   accepted for compatibility and does nothing: unknown hosts are refused by default (BARYOVM_STRICT_HOST_KEYS is likewise ignored)
      --trust-new-host     learn the key of a host not seen before, on this run only (also BARYOVM_TRUST_NEW_HOST=1); a changed key is always refused
```

### SEE ALSO

* [baryovm stack](baryovm_stack.md)	 - Manage docker compose stacks on your VMs
