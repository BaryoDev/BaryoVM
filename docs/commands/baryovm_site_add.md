## baryovm site add

Set up a static site on a VM: webroot, nginx server block, certificate

### Synopsis

Create the webroot and the ACME challenge directory, write an nginx server block for the domain
(HTTP to HTTPS redirect, TLS, try_files for a static export, a year-long cache on hashed assets,
no-cache on HTML, gzip scoped to the static locations), get a certificate with certbot in webroot
mode, test with nginx -t, and reload. Where SELinux is present the webroot is relabelled with
restorecon.

The order is fixed by the certificate: an HTTP-only block goes in first so certbot's challenge can
be served, then the full block once the certificate exists. Any failing step puts the previous
server block back (or removes the new file) and reloads nginx if it had been reloaded, so nginx
ends where it started, and the error names the step.

DNS is not touched: the domain has to resolve to the VM before the certificate can be issued. A
site that already exists, on the VM or in the local inventory, is refused unless --force.

Every command that needs root runs through sudo -n, so the SSH user needs passwordless sudo. Unlike the
billable provisioning commands, this one acts on a VM you already own and is undone by removing
one file, so it does not need --yes. --dry-run prints the server blocks and the commands first.

```
baryovm site add <domain> [flags]
```

### Examples

```
  baryovm site add example.com --vm web1 --root /var/www/example.com --email ops@example.com --dry-run
  baryovm site add example.com --vm web1 --root /var/www/example.com --email ops@example.com
```

### Options

```
      --dry-run        print the server blocks and commands without connecting or changing anything
      --email string   ACME contact address (default: register without one)
      --force          replace a site that already exists
  -h, --help           help for add
      --root string    absolute webroot on the VM, such as /var/www/example.com (required)
      --vm string      registered VM to set the site up on (required)
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm site](baryovm_site.md)	 - Provision and deploy a static site on a VM (nginx, TLS, webroot)
