## baryovm status

Every registered stack on one screen (read only)

### Synopsis

Asks each registered VM once, over one SSH connection, and prints a row per stack: whether its
containers are up, the commit of the last finished release, a release that failed or stopped part
way, and the database backups on the VM. With --verify it also runs the stacks' verify assertions.

A dash means the column is not configured for that stack, which is not a failure. A question
mark means it is configured and could not be read; the reason is in the notes. A VM that does
not answer within --timeout is shown as unreachable with the reason, and every other row still
prints. A VM behind a forced command (`vm remote set`) is asked its status request only and
shows as limited.

Nothing runs on a VM beyond reads unless --verify is set: a verify `command:` check is a shell
command (with sudo when the stack uses it), so it is opt-in. The command exits zero when it
produced a report, however many rows need a look; read `attention` in -o json.

```
baryovm status [flags]
```

### Examples

```
  baryovm status
  baryovm status --vm oracle -o json
  baryovm status --stack shop --timeout 10s
```

### Options

```
      --concurrency int    VMs asked at once (default 4)
  -h, --help               help for status
      --stack string       only this stack
      --timeout duration   how long one VM may take, connection included (default 30s)
      --verify             also run the stacks' verify assertions on the VM (off by default: a verify command check runs a shell command)
      --vm string          only this VM
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm](baryovm.md)	 - Provision VMs and deploy apps to your own boxes
