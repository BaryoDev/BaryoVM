## baryovm stack set-backup

Set a stack's backup strategies (database, remote database, command, files)

### Synopsis

Stores the stack's backup as a list of strategies in fleet.json. Each has a kind and its own
settings, and declares how it is dumped and how it is restored:

  postgres-container  container, database, user (pg_dump inside a container on the VM)
  postgres-remote     host, port, database, user, image, envFile or passFile (a managed database)
  command             dump and restore, shell lines run on the VM (dump writes stdout, restore reads stdin)
  files               paths, absolute (a tar)

A release runs every strategy before anything changes and stops at the first failure. The file
is a JSON array; an empty array, [], says the stack has nothing to back up, which is what
`stack update --auto` needs from a stack with no database. --clear removes the declaration.

```
baryovm stack set-backup <name> [flags]
```

### Examples

```
  baryovm stack set-backup shop --file backup.json
  baryovm stack set-backup site --file empty.json
  baryovm stack set-backup shop --clear
```

### Options

```
      --clear         remove the stack's backup declaration
      --file string   JSON file holding the array of backup strategies
  -h, --help          help for set-backup
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm stack](baryovm_stack.md)	 - Manage docker compose stacks on your VMs
