## baryovm vm add

Register an existing VM by host + SSH key

```
baryovm vm add <name> [flags]
```

### Options

```
  -h, --help          help for add
      --host string   public IP or hostname (required)
      --key string    path to the private SSH key (required)
      --port int      SSH port (default 22)
      --user string   SSH user (default "ubuntu")
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm vm](baryovm_vm.md)	 - Manage the VMs in your fleet
