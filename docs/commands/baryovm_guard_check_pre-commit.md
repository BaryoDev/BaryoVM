## baryovm guard check pre-commit

Scan the staged diff for keys

```
baryovm guard check pre-commit [flags]
```

### Options

```
  -h, --help   help for pre-commit
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm guard check](baryovm_guard_check.md)	 - Run the hook checks (called by the hooks install-hooks writes)
