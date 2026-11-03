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
      --strict-host-keys   accepted for compatibility and does nothing: unknown hosts are refused by default (BARYOVM_STRICT_HOST_KEYS is likewise ignored)
      --trust-new-host     learn the key of a host not seen before, on this run only (also BARYOVM_TRUST_NEW_HOST=1); a changed key is always refused
```

### SEE ALSO

* [baryovm vm remote](baryovm_vm_remote.md)	 - Run baryovm behind an SSH forced command (a restricted CI key)
