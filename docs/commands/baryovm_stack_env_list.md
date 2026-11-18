## baryovm stack env list

List the keys in the stack's env file, values masked

```
baryovm stack env list <name> [flags]
```

### Options

```
  -h, --help   help for list
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   always refuse an unknown host; =false learns its key on first use instead of asking (also BARYOVM_STRICT_HOST_KEYS=1 or 0)
```

### SEE ALSO

* [baryovm stack env](baryovm_stack_env.md)	 - Set, list and unset keys in a stack's .env without ssh
