## baryovm vm forget-key

Forget a VM's recorded SSH host key, so the next connection treats it as unknown

### Synopsis

Forget the host key BaryoVM recorded for a VM, for when the VM was rebuilt and now has a new one.

This clears only BaryoVM's own store. If ~/.ssh/known_hosts still holds the old key, the rebuilt
host is refused again as a changed key; remove that line with ssh-keygen -R too.

After that the host is unknown, and the next connection depends on the mode: on a terminal it asks
whether to trust the new key, with no terminal, under -o json or with --strict-host-keys it is
refused, and with --strict-host-keys=false (or BARYOVM_STRICT_HOST_KEYS=0) it learns the new key
without asking. Check the new fingerprint against the cloud console or the machine itself first.

```
baryovm vm forget-key <name-or-host> [flags]
```

### Options

```
  -h, --help   help for forget-key
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   always refuse an unknown host; =false learns its key on first use instead of asking (also BARYOVM_STRICT_HOST_KEYS=1 or 0). A project release never learns or asks, even with =false
```

### SEE ALSO

* [baryovm vm](baryovm_vm.md)	 - Manage the VMs in your fleet
