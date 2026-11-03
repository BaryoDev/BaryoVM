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
      --strict-host-keys   accepted for compatibility and does nothing: unknown hosts are refused by default (BARYOVM_STRICT_HOST_KEYS is likewise ignored)
      --trust-new-host     learn the key of a host not seen before, on this run only (also BARYOVM_TRUST_NEW_HOST=1); a changed key is always refused
```

### SEE ALSO

* [baryovm vm](baryovm_vm.md)	 - Manage the VMs in your fleet
