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
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm](baryovm.md)	 - Provision VMs and deploy apps to your own boxes
* [baryovm guard check](baryovm_guard_check.md)	 - Run the hook checks (called by the hooks install-hooks writes)
* [baryovm guard install-hooks](baryovm_guard_install-hooks.md)	 - Write pre-commit and pre-push hooks that run the same checks
