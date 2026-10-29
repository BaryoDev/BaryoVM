## baryovm vm harden

Apply the SSH hardening policy to a VM

### Synopsis

Applies an opinionated, idempotent SSH policy: per-source penalties in sshd
where the daemon supports them, fail2ban with escalating bans, and the small
surface reductions that cost nothing.

It never changes the SSH port, never disables public key authentication and
never removes an authorized_keys entry, so it cannot lock you out. The sshd
config is validated before anything is reloaded, and reloaded rather than
restarted, so an open session survives a mistake.

It fixes ~/.ssh permissions, moves authorized_keys.* backups aside, puts the
post-quantum key exchange first where OpenSSH has one, and sets nginx
server_tokens off. It only reports stale keys, pending updates, unattended
upgrades, a missing default_server on 443, a kernel waiting for a reboot and
port 22 open to everyone. Those are decisions, so they are marked as reports
and never changed. --ssh-from limits port 22 in firewalld or ufw, and refuses
a list that excludes the address you are connecting from.

```
baryovm vm harden <name> [flags]
```

### Options

```
      --dry-run            report what would change and write nothing
  -h, --help               help for harden
      --ignore strings     extra CIDRs fail2ban must never ban (loopback is always exempt)
      --ssh-from strings   limit port 22 in firewalld or ufw to these CIDRs (refused if they exclude your own address)
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm vm](baryovm_vm.md)	 - Manage the VMs in your fleet
