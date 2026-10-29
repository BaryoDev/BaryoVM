## baryovm guard check

Run the hook checks (called by the hooks install-hooks writes)

```
baryovm guard check [flags]
```

### Options

```
  -h, --help   help for check
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm guard](baryovm_guard.md)	 - Key and remote checks that run before git and gh
* [baryovm guard check pre-commit](baryovm_guard_check_pre-commit.md)	 - Scan the staged diff for keys
* [baryovm guard check pre-push](baryovm_guard_check_pre-push.md)	 - Scan the commits being pushed and check the remote host (reads git's ref lines on stdin)
