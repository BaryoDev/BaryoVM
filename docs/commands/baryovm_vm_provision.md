## baryovm vm provision

Provision a new cloud VM and register it in the fleet

```
baryovm vm provision <name> [flags]
```

### Options

```
      --dry-run              print the plan without creating anything
  -h, --help                 help for provision
      --http-from strings    limit port 80 to these CIDRs or the cloudflare preset (default: open)
      --https-from strings   limit port 443 to these CIDRs or the cloudflare preset (default: open)
      --key string           private SSH key to authorize + connect with (required)
      --provider string      cloud provider: lightsail (oci coming) (default "lightsail")
      --region string        cloud region (default: from ~/.aws)
      --size string          instance size / bundle (default: provider's smallest)
      --ssh-from strings     limit port 22 to these CIDRs (default: open to 0.0.0.0/0)
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm vm](baryovm_vm.md)	 - Manage the VMs in your fleet
