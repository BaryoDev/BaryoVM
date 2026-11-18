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
      --strict-host-keys   always refuse an unknown host; =false learns its key on first use instead of asking (also BARYOVM_STRICT_HOST_KEYS=1 or 0). A project release never learns or asks, even with =false
```

### SEE ALSO

* [baryovm](baryovm.md)	 - Provision VMs and deploy apps to your own boxes
* [baryovm project export](baryovm_project_export.md)	 - Write a project file from the stacks registered on this machine
* [baryovm project release](baryovm_project_release.md)	 - Release every stack of a project file, in order, with no vm add or stack add
