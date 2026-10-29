## baryovm stack env

Set, list and unset keys in a stack's .env without ssh

### Synopsis

Manage the env file of a registered stack on its VM. A value is never an argument: set prompts for
it with no echo, or reads one line from stdin when piped, and sends it over the ssh session's stdin.
No value appears on a command line, locally or on the VM, or in the output.

The file is the stack's --env-file (default .env) under its directory, written at mode 600, and
created with its directory if missing. Compose reads .env only when containers start, so pass
--recreate to run "compose up -d --force-recreate" after a change. The value is stored as typed,
as KEY=value on one line, and compose then reads it by its own rules for quotes and "$".

```
baryovm stack env [flags]
```

### Options

```
  -h, --help   help for env
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm stack](baryovm_stack.md)	 - Manage docker compose stacks on your VMs
* [baryovm stack env list](baryovm_stack_env_list.md)	 - List the keys in the stack's env file, values masked
* [baryovm stack env set](baryovm_stack_env_set.md)	 - Set KEY in the stack's env file (value from a no-echo prompt or stdin)
* [baryovm stack env unset](baryovm_stack_env_unset.md)	 - Remove KEY from the stack's env file
