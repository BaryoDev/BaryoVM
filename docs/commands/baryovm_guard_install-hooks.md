## baryovm guard install-hooks

Write pre-commit and pre-push hooks that run the same checks

### Synopsis

Writes git pre-commit and pre-push hooks in this repository that call
`baryovm guard check`, so a commit or push made without `baryovm git` is
checked too. A hook that is not BaryoVM's is left alone unless --force, which
moves it to <name>.baryovm-old first. The hooks fail when baryovm is not on PATH.

```
baryovm guard install-hooks [flags]
```

### Options

```
      --force   replace a hook that is not BaryoVM's, keeping it as <name>.baryovm-old
  -h, --help    help for install-hooks
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   accepted for compatibility and does nothing: unknown hosts are refused by default (BARYOVM_STRICT_HOST_KEYS is likewise ignored)
      --trust-new-host     learn the key of a host not seen before, on this run only (also BARYOVM_TRUST_NEW_HOST=1); a changed key is always refused
```

### SEE ALSO

* [baryovm guard](baryovm_guard.md)	 - Key and remote checks that run before git and gh
