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
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm](baryovm.md)	 - Provision VMs and deploy apps to your own boxes
