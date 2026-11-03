## baryovm guard

Key and remote checks that run before git and gh

```
baryovm guard [flags]
```

### Options

```
  -h, --help   help for guard
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   accepted for compatibility and does nothing: unknown hosts are refused by default (BARYOVM_STRICT_HOST_KEYS is likewise ignored)
      --trust-new-host     learn the key of a host not seen before, on this run only (also BARYOVM_TRUST_NEW_HOST=1); a changed key is always refused
```

### SEE ALSO

* [baryovm](baryovm.md)	 - Provision VMs and deploy apps to your own boxes
* [baryovm guard check](baryovm_guard_check.md)	 - Run the hook checks (called by the hooks install-hooks writes)
* [baryovm guard install-hooks](baryovm_guard_install-hooks.md)	 - Write pre-commit and pre-push hooks that run the same checks
