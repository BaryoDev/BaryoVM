## baryovm vm remove

Remove a VM from the fleet (does not destroy the machine)

```
baryovm vm remove <name> [flags]
```

### Options

```
  -h, --help   help for remove
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   always refuse an unknown host; =false learns its key on first use instead of asking (also BARYOVM_STRICT_HOST_KEYS=1 or 0). A project release never learns or asks, even with =false
```

### SEE ALSO

* [baryovm vm](baryovm_vm.md)	 - Manage the VMs in your fleet
