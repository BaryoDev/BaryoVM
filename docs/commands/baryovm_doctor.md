## baryovm doctor

Check (and with --fix, auto-install) local prerequisites

### Synopsis

Reports the local tools and cloud credentials BaryoVM uses. With --fix,
a missing tool BaryoVM knows how to install is installed; anything it cannot
install is reported with what to do about it.

```
baryovm doctor [flags]
```

### Options

```
      --fix    install the missing tools BaryoVM knows how to install
  -h, --help   help for doctor
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   accepted for compatibility and does nothing: unknown hosts are refused by default (BARYOVM_STRICT_HOST_KEYS is likewise ignored)
      --trust-new-host     learn the key of a host not seen before, on this run only (also BARYOVM_TRUST_NEW_HOST=1); a changed key is always refused
```

### SEE ALSO

* [baryovm](baryovm.md)	 - Provision VMs and deploy apps to your own boxes
