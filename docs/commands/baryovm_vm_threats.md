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
      --strict-host-keys   accepted for compatibility and does nothing: unknown hosts are refused by default (BARYOVM_STRICT_HOST_KEYS is likewise ignored)
      --trust-new-host     learn the key of a host not seen before, on this run only (also BARYOVM_TRUST_NEW_HOST=1); a changed key is always refused
```

### SEE ALSO

* [baryovm vm](baryovm_vm.md)	 - Manage the VMs in your fleet
