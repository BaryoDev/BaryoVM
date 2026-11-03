## baryovm guard check pre-push

Scan the commits being pushed and check the remote host (reads git's ref lines on stdin)

```
baryovm guard check pre-push <remote> <url> [flags]
```

### Options

```
  -h, --help   help for pre-push
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   accepted for compatibility and does nothing: unknown hosts are refused by default (BARYOVM_STRICT_HOST_KEYS is likewise ignored)
      --trust-new-host     learn the key of a host not seen before, on this run only (also BARYOVM_TRUST_NEW_HOST=1); a changed key is always refused
```

### SEE ALSO

* [baryovm guard check](baryovm_guard_check.md)	 - Run the hook checks (called by the hooks install-hooks writes)
