## baryovm vm bootstrap

Install the container runtime (docker or podman) on a VM (idempotent)

```
baryovm vm bootstrap <name> [flags]
```

### Options

```
      --dry-run          print the install script without connecting
  -h, --help             help for bootstrap
      --runtime string   runtime to install: docker (default) or podman
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm vm](baryovm_vm.md)	 - Manage the VMs in your fleet
