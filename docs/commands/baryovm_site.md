## baryovm site

Provision and deploy a static site on a VM (nginx, TLS, webroot)

```
baryovm site [flags]
```

### Options

```
  -h, --help   help for site
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   accepted for compatibility and does nothing: unknown hosts are refused by default (BARYOVM_STRICT_HOST_KEYS is likewise ignored)
      --trust-new-host     learn the key of a host not seen before, on this run only (also BARYOVM_TRUST_NEW_HOST=1); a changed key is always refused
```

### SEE ALSO

* [baryovm](baryovm.md)	 - Provision VMs and deploy apps to your own boxes
* [baryovm site add](baryovm_site_add.md)	 - Set up a static site on a VM: webroot, nginx server block, certificate
* [baryovm site deploy](baryovm_site_deploy.md)	 - Sync a built directory into a site's webroot and reload nginx
