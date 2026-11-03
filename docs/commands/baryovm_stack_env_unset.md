## baryovm stack env unset

Remove KEY from the stack's env file

```
baryovm stack env unset <name> <KEY> [flags]
```

### Options

```
  -h, --help       help for unset
      --recreate   run compose up -d --force-recreate after the write
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   accepted for compatibility and does nothing: unknown hosts are refused by default (BARYOVM_STRICT_HOST_KEYS is likewise ignored)
      --trust-new-host     learn the key of a host not seen before, on this run only (also BARYOVM_TRUST_NEW_HOST=1); a changed key is always refused
```

### SEE ALSO

* [baryovm stack env](baryovm_stack_env.md)	 - Set, list and unset keys in a stack's .env without ssh
