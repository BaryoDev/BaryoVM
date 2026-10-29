## baryovm stack set-publish

Set a stack's publish spec read by ship and stack publish

### Synopsis

Stores a publish spec (registry, versionFrom, changelog, packages, assertions, requireDeployed)
on the stack. --clear removes it.

```
baryovm stack set-publish <name> [flags]
```

### Examples

```
  baryovm stack set-publish shop --file publish.json
  baryovm stack set-publish shop --clear
```

### Options

```
      --clear         remove the stack's publish spec
      --file string   JSON file holding the publish spec
  -h, --help          help for set-publish
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm stack](baryovm_stack.md)	 - Manage docker compose stacks on your VMs
