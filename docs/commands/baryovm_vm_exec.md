## baryovm vm exec

Run a command on a registered VM over SSH

### Synopsis

Run an arbitrary command on one registered VM using the same SSH key already
in fleet.json. This is the same authority as opening an interactive SSH
session; the CLI only shortens the path.

There is no --sudo flag: put sudo in the remote command yourself (prefer
sudo -n so -o json does not hang on a password prompt).

A non-zero remote exit becomes a non-zero CLI exit. Under -o json the envelope
keeps stdout, stderr and exitCode as separate fields.

The remote command must follow -- so flags like -h belong to the remote
process rather than this CLI (e.g. baryovm vm exec web1 -- df -h).

```
baryovm vm exec <name> -- <command>... [flags]
```

### Options

```
  -h, --help   help for exec
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm vm](baryovm_vm.md)	 - Manage the VMs in your fleet
