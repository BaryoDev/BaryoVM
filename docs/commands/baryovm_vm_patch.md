## baryovm vm patch

Update OS packages on a VM and check the stack came back

### Synopsis

Snapshots the kernel, containers (with health) and nginx, runs the package
update as a detached systemd unit so a dropped session cannot stop it halfway,
and asks the host whether a reboot is needed (needs-restarting -r, or
/var/run/reboot-required).

It reboots only with --reboot, or when you answer yes at the prompt on a
terminal. After that it waits for SSH, then checks that every container that
was running is running and healthy again, nginx is active and nginx -t passes.
A container that was already unhealthy before is not counted as a failure.
Exits non-zero and names any container that did not return.

--dry-run lists the pending updates and whether a reboot is currently required.

```
baryovm vm patch <name> [flags]
```

### Options

```
      --dry-run   list pending updates and whether a reboot would follow, change nothing
  -h, --help      help for patch
      --reboot    reboot without asking when the host says it needs one
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm vm](baryovm_vm.md)	 - Manage the VMs in your fleet
