## baryovm cloud

Run wrangler, firebase or gcloud, refusing a deploy that ships a key or aims at the wrong project

```
baryovm cloud [flags]
```

### Options

```
  -h, --help   help for cloud
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   accepted for compatibility and does nothing: unknown hosts are refused by default (BARYOVM_STRICT_HOST_KEYS is likewise ignored)
      --trust-new-host     learn the key of a host not seen before, on this run only (also BARYOVM_TRUST_NEW_HOST=1); a changed key is always refused
```

### SEE ALSO

* [baryovm](baryovm.md)	 - Provision VMs and deploy apps to your own boxes
* [baryovm cloud firebase](baryovm_cloud_firebase.md)	 - Run firebase after scanning the files it deploys and checking the project
* [baryovm cloud gcloud](baryovm_cloud_gcloud.md)	 - Run gcloud, refusing a command that writes a key file inside a git working tree
* [baryovm cloud wrangler](baryovm_cloud_wrangler.md)	 - Run wrangler after scanning the Worker bundle and its config for keys
