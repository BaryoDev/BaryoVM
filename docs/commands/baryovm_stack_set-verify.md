## baryovm stack set-verify

Set a stack's verify spec (checks, attempts, delay) read by release, update and rollback

### Synopsis

Stores a verify spec on the stack in fleet.json. A check is either a route on the stack's own
site with assertions on what it serves (status, minBytes, contains, absent, jsonMatch), or a
command run on the VM in the stack's directory. Routes are fetched from the stack's healthUrl
origin, or from the spec's baseUrl; a check cannot name another host.

A stack with no spec keeps verifying with its healthUrl alone. A release manifest's own
`verify` commands still win for a release. --clear removes the spec.

```
baryovm stack set-verify <name> [flags]
```

### Examples

```
  baryovm stack set-verify shop --file verify.json
  baryovm stack set-verify shop --clear
```

### Options

```
      --clear         remove the stack's verify spec
      --file string   JSON file holding the verify spec
  -h, --help          help for set-verify
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm stack](baryovm_stack.md)	 - Manage docker compose stacks on your VMs
