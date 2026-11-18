## baryovm vm remote set

Mark a VM as behind a forced command (or clear the mark)

```
baryovm vm remote set <vm> <forced|off> [flags]
```

### Options

```
  -h, --help   help for set
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   always refuse an unknown host; =false learns its key on first use instead of asking (also BARYOVM_STRICT_HOST_KEYS=1 or 0). A project release never learns or asks, even with =false
```

### SEE ALSO

* [baryovm vm remote](baryovm_vm_remote.md)	 - Run baryovm behind an SSH forced command (a restricted CI key)
