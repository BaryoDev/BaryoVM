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
      --strict-host-keys   always refuse an unknown host; =false learns its key on first use instead of asking (also BARYOVM_STRICT_HOST_KEYS=1 or 0). A project release never learns or asks, even with =false
```

### SEE ALSO

* [baryovm guard check](baryovm_guard_check.md)	 - Run the hook checks (called by the hooks install-hooks writes)
