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

--nginx-ban-probes adds a fail2ban jail, in its own jail.d file, that bans
sources requesting paths no site serves (.env, .git, .aws, /proc/self/environ
and similar). It matches the path only, never the query string. PHP and
WordPress paths join the list only with --nginx-no-php (you say no site on
this host serves PHP) and only when nginx has no fastcgi_pass; a PHP site
behind proxy_pass has none, so the flag is your word. Another
site can still make a visitor's browser request such a path, so a visitor can
be banned. Behind a CDN or load balancer nginx logs the proxy's address unless
set_real_ip_from is set, and the jail would ban the proxy; private ranges are
never banned. Without the flag the run only reports the jail.

--nginx-drop-probes answers those requests with 444 before they reach an app:
it writes a map and a snippet and adds one include line to each server block,
backs up every file it touches, runs nginx -t, and restores them all if it
fails. Without the flag it only reports how many server blocks lack the
include.

```
baryovm vm harden <name> [flags]
```

### Options

```
      --dry-run             report what would change and write nothing
  -h, --help                help for harden
      --ignore strings      extra CIDRs fail2ban must never ban (loopback is always exempt)
      --nginx-ban-probes    ban sources that request scanner paths (.env, .git, ...) with a fail2ban jail on the nginx access logs
      --nginx-drop-probes   answer scanner requests with 444 in nginx: edits every server block (backed up, nginx -t checked, restored on failure)
      --nginx-no-php        no site on this host serves PHP or WordPress: also ban and drop their paths (still off if nginx has fastcgi_pass)
      --ssh-from strings    limit port 22 in firewalld or ufw to these CIDRs (refused if they exclude your own address)
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm vm](baryovm_vm.md)	 - Manage the VMs in your fleet
