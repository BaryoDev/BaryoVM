## baryovm stack tls

Write the nginx vhost and get the certificate from the release manifest's tls block

### Synopsis

Runs the manifest's tls block on its own, without a release: write a port 80 vhost, issue the
certificate with certbot, write the real vhost, nginx -t, reload. Safe to rerun. A vhost file
BaryoVM did not write is left alone unless --adopt is given, and then it is backed up first.
If nginx -t rejects the new file the old one is restored and nginx is not reloaded.

```
baryovm stack tls <name> [flags]
```

### Examples

```
  baryovm stack tls app --dry-run
  baryovm stack tls app --config ./baryovm.release.json
```

### Options

```
      --adopt           take over a vhost file BaryoVM did not write (the old file is backed up)
      --config string   release manifest path (overrides the stack's --release-file)
      --dry-run         print the generated configs and commands without touching the VM
  -h, --help            help for tls
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm stack](baryovm_stack.md)	 - Manage docker compose stacks on your VMs
