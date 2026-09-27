## baryovm vm threats

Show what a VM is being attacked with

### Synopsis

Reads the SSH authentication record and any fail2ban bans, and summarises
who is hitting the machine, what account names they are guessing, and every
login that actually succeeded.

The successful logins are the part worth reading. The rest is volume.

```
baryovm vm threats <name> [flags]
```

### Options

```
  -h, --help           help for threats
      --since string   systemd time expression, e.g. "7 days ago" or 2026-09-01 (default "24 hours ago")
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm vm](baryovm_vm.md)	 - Manage the VMs in your fleet
