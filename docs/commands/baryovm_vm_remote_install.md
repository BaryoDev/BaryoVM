## baryovm vm remote install

Copy the wrapper to a VM and print the authorized_keys line to add

### Synopsis

Run with an ordinary (unrestricted) key, once. Copies a linux baryovm binary to
/usr/local/bin/baryovm-remote (root-owned, so the SSH user cannot replace it), writes a template
/etc/baryovm/remote.json if there is none (it allows nothing until you edit it), and prints the
authorized_keys line. It never edits authorized_keys.

The binary is this one when it is a linux build for the VM's architecture, otherwise pass
--binary with a linux release binary.

```
baryovm vm remote install <vm> [flags]
```

### Options

```
      --binary string       linux baryovm binary to install (default: this binary, when it is linux and matches the VM)
  -h, --help                help for install
      --public-key string   the CI key's public key line, to print a complete authorized_keys entry
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   accepted for compatibility and does nothing: unknown hosts are refused by default (BARYOVM_STRICT_HOST_KEYS is likewise ignored)
      --trust-new-host     learn the key of a host not seen before, on this run only (also BARYOVM_TRUST_NEW_HOST=1); a changed key is always refused
```

### SEE ALSO

* [baryovm vm remote](baryovm_vm_remote.md)	 - Run baryovm behind an SSH forced command (a restricted CI key)
