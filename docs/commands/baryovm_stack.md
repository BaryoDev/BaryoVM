## baryovm stack

Manage docker compose stacks on your VMs

### Options

```
  -h, --help   help for stack
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm](baryovm.md)	 - Provision VMs and deploy apps to your own boxes
* [baryovm stack add](baryovm_stack_add.md)	 - Register a compose stack (a project dir on a VM)
* [baryovm stack backup](baryovm_stack_backup.md)	 - Back up the stack's database + config (pg_dump + .env)
* [baryovm stack backups](baryovm_stack_backups.md)	 - List the stack's database backups
* [baryovm stack deploy](baryovm_stack_deploy.md)	 - compose up -d the stack (optionally pull + recreate)
* [baryovm stack list](baryovm_stack_list.md)	 - List registered stacks
* [baryovm stack logs](baryovm_stack_logs.md)	 - Show recent logs for the stack
* [baryovm stack ps](baryovm_stack_ps.md)	 - Show the stack's containers
* [baryovm stack pull](baryovm_stack_pull.md)	 - Pull the stack's images
* [baryovm stack release](baryovm_stack_release.md)	 - Sync source to the VM, build images there, then compose up (config-driven)
* [baryovm stack remove](baryovm_stack_remove.md)	 - Remove a stack registration (does not touch the VM)
* [baryovm stack restore](baryovm_stack_restore.md)	 - Restore the stack's database from a backup (REPLACES current data)
* [baryovm stack set-update](baryovm_stack_set-update.md)	 - Set a stack's update policy (autoUpdate, health URL)
* [baryovm stack update](baryovm_stack_update.md)	 - Pull newer images and recreate, only if they come up healthy
