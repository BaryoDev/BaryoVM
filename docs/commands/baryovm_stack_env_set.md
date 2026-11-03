## baryovm stack env set

Set KEY in the stack's env file (value from a no-echo prompt or stdin)

```
baryovm stack env set <name> <KEY> [flags]
```

### Options

```
  -h, --help       help for set
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
