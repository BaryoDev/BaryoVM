## baryovm vm harden

Apply the SSH hardening policy to a VM

### Synopsis

Applies an opinionated, idempotent SSH policy: per-source penalties in sshd
where the daemon supports them, fail2ban with escalating bans, and the small
surface reductions that cost nothing.

It never changes the SSH port, never disables public key authentication and
never touches authorized_keys, so it cannot lock you out. The sshd config is
validated before anything is reloaded, and reloaded rather than restarted, so
an open session survives a mistake.

```
baryovm vm harden <name> [flags]
```

### Options

```
      --dry-run          report what would change and write nothing
  -h, --help             help for harden
      --ignore strings   extra CIDRs fail2ban must never ban (loopback is always exempt)
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm vm](baryovm_vm.md)	 - Manage the VMs in your fleet
