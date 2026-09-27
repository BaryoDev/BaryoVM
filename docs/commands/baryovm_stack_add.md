## baryovm stack add

Register a compose stack (a project dir on a VM)

```
baryovm stack add <name> [flags]
```

### Options

```
      --backup-dir string            remote dir for backups (default: ~/<name>-backups)
      --db-container string          postgres container for backups, e.g. deploy-postgres-1
      --db-name string               database to back up
      --db-user string               database user (default: postgres)
      --env-file string              config file to back up, relative to the project dir (e.g. .env)
      --file string                  compose file name (default: compose's own default)
  -h, --help                         help for add
      --keep int                     backups to retain per kind (default 14)
      --path string                  remote project directory, e.g. /opt/barakocms (required)
      --release-file stack release   local JSON release manifest for stack release
      --sudo                         run this stack's remote commands via sudo -n, for a stack whose .env or deploy root is root-owned. See USAGE.md for what it covers and the three things that change with it
      --vm string                    VM the stack runs on (required)
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm stack](baryovm_stack.md)	 - Manage docker compose stacks on your VMs
