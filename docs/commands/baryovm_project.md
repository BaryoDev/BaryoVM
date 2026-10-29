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
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm](baryovm.md)	 - Provision VMs and deploy apps to your own boxes
* [baryovm project export](baryovm_project_export.md)	 - Write a project file from the stacks registered on this machine
* [baryovm project release](baryovm_project_release.md)	 - Release every stack of a project file, in order, with no vm add or stack add
