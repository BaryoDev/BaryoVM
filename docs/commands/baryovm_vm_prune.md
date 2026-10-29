## baryovm vm prune

Free disk space on a VM, planning first

### Synopsis

Lists what could be deleted (item, size, reason) and deletes nothing. Add
--apply to run the plan; it prints the space freed and df before and after.

It plans: Docker build cache, dangling images, anonymous unused volumes,
stopped containers older than --stopped-days, rollback images beyond the
newest --keep-images per repository, bin/obj/TestResults beside a .csproj or
.fsproj under --work-dir, clean git worktrees under --worktree-dir whose
branch has nothing off the remote, and npm, go-build, pip, playwright and
older editor server caches.

It never plans a named volume, an image any container uses, a bin/ without a
project file beside it, or a dirty worktree. --floor <GB> exits 0 with no
changes while free space is at or above it, so a cron job stays quiet.

```
baryovm vm prune <name> [flags]
```

### Options

```
      --apply                  run the plan (the default only lists it)
      --floor int              do nothing while free space is at or above this many GB
  -h, --help                   help for prune
      --keep-images int        rollback images kept per repository (default 2)
      --stopped-days int       plan stopped containers older than this many days (default 14)
      --work-dir strings       folder searched for build output beside a project file (repeatable)
      --worktree-dir strings   folder searched for git worktrees (repeatable)
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm vm](baryovm_vm.md)	 - Manage the VMs in your fleet
