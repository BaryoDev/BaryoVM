## baryovm project

Release from a committed project file, or write one from your fleet

```
baryovm project [flags]
```

### Options

```
  -h, --help   help for project
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   accepted for compatibility and does nothing: unknown hosts are refused by default (BARYOVM_STRICT_HOST_KEYS is likewise ignored)
      --trust-new-host     learn the key of a host not seen before, on this run only (also BARYOVM_TRUST_NEW_HOST=1); a changed key is always refused
```

### SEE ALSO

* [baryovm](baryovm.md)	 - Provision VMs and deploy apps to your own boxes
* [baryovm project export](baryovm_project_export.md)	 - Write a project file from the stacks registered on this machine
* [baryovm project release](baryovm_project_release.md)	 - Release every stack of a project file, in order, with no vm add or stack add
