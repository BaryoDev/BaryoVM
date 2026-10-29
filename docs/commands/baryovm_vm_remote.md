## baryovm vm remote

Run baryovm behind an SSH forced command (a restricted CI key)

### Synopsis

A VM owner can pin a CI key in authorized_keys with command="...", so the key can do one
thing and a leak cannot open a shell. baryovm normally sends shell, which such a key discards.
This mode installs `baryovm remote serve` as that forced command. It accepts only update,
backup, status and logs for the stacks listed in /etc/baryovm/remote.json on the VM.
exec and release are not available: exec is arbitrary shell, and release uploads files.

```
baryovm vm remote [flags]
```

### Options

```
  -h, --help   help for remote
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm vm](baryovm_vm.md)	 - Manage the VMs in your fleet
* [baryovm vm remote install](baryovm_vm_remote_install.md)	 - Copy the wrapper to a VM and print the authorized_keys line to add
* [baryovm vm remote set](baryovm_vm_remote_set.md)	 - Mark a VM as behind a forced command (or clear the mark)
