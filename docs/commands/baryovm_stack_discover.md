## baryovm stack discover

List the compose projects already running on a VM and the `stack add` for each (read-only unless --adopt)

### Synopsis

Runs `docker compose ls` on the VM and reads each project's config for its service count and a
likely postgres container, then prints the `stack add` command for each project.

The postgres container is a guess: the first service whose image name contains "postgres".
Check it before you rely on it for backups.

--adopt registers the projects in the local fleet, asking about each one on a terminal.
Without a terminal, or under -o json, it needs --yes. Projects already registered are skipped.

```
baryovm stack discover <vm> [flags]
```

### Examples

```
  baryovm stack discover oracle
  baryovm stack discover oracle --sudo
  baryovm stack discover oracle --adopt
  baryovm stack discover oracle --adopt --yes -o json
```

### Options

```
      --adopt   register the projects found in the local fleet, asking about each one on a terminal
  -h, --help    help for discover
      --sudo    run docker through sudo -n, for a VM where the SSH user cannot reach the docker socket. Adopted stacks are registered with --sudo
      --yes     with --adopt, register every project without asking (required without a terminal or under -o json)
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm stack](baryovm_stack.md)	 - Manage docker compose stacks on your VMs
