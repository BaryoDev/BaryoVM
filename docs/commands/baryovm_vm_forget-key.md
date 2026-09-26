## baryovm vm forget-key

Forget a VM's recorded SSH host key, so the next connection learns it again

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
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm vm](baryovm_vm.md)	 - Manage the VMs in your fleet
