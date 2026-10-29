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
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm](baryovm.md)	 - Provision VMs and deploy apps to your own boxes
* [baryovm site add](baryovm_site_add.md)	 - Set up a static site on a VM: webroot, nginx server block, certificate
* [baryovm site deploy](baryovm_site_deploy.md)	 - Sync a built directory into a site's webroot and reload nginx
