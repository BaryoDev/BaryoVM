## baryovm site deploy

Sync a built directory into a site's webroot and reload nginx

### Synopsis

Sync the contents of <dir> into the webroot the site was added with (read from the local
inventory, where 'site add' recorded it), then relabel with restorecon where SELinux is present,
run nginx -t and reload. The transfer is the same rsync, with the same host key check, that
'stack release' uses, and --delete removes files that are no longer in <dir>.

An empty or missing directory is refused, since syncing it would empty the webroot.

```
baryovm site deploy <domain> <dir> [flags]
```

### Examples

```
  baryovm site deploy example.com ./out
```

### Options

```
      --dry-run   print the rsync and reload commands without connecting or changing anything
  -h, --help      help for deploy
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   accepted for compatibility and does nothing: unknown hosts are refused by default (BARYOVM_STRICT_HOST_KEYS is likewise ignored)
      --trust-new-host     learn the key of a host not seen before, on this run only (also BARYOVM_TRUST_NEW_HOST=1); a changed key is always refused
```

### SEE ALSO

* [baryovm site](baryovm_site.md)	 - Provision and deploy a static site on a VM (nginx, TLS, webroot)
