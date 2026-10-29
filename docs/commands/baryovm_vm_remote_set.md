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
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm vm remote](baryovm_vm_remote.md)	 - Run baryovm behind an SSH forced command (a restricted CI key)
