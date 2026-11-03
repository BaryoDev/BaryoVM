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
      --strict-host-keys   accepted for compatibility and does nothing: unknown hosts are refused by default (BARYOVM_STRICT_HOST_KEYS is likewise ignored)
      --trust-new-host     learn the key of a host not seen before, on this run only (also BARYOVM_TRUST_NEW_HOST=1); a changed key is always refused
```

### SEE ALSO

* [baryovm guard](baryovm_guard.md)	 - Key and remote checks that run before git and gh
* [baryovm guard check commit-msg](baryovm_guard_check_commit-msg.md)	 - Scan the commit message file for keys
* [baryovm guard check pre-commit](baryovm_guard_check_pre-commit.md)	 - Scan the staged diff for keys
* [baryovm guard check pre-push](baryovm_guard_check_pre-push.md)	 - Scan the commits being pushed and check the remote host (reads git's ref lines on stdin)
