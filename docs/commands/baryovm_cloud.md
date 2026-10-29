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
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm](baryovm.md)	 - Provision VMs and deploy apps to your own boxes
* [baryovm cloud firebase](baryovm_cloud_firebase.md)	 - Run firebase after scanning the files it deploys and checking the project
* [baryovm cloud gcloud](baryovm_cloud_gcloud.md)	 - Run gcloud, refusing a command that writes a key file inside a git working tree
* [baryovm cloud wrangler](baryovm_cloud_wrangler.md)	 - Run wrangler after scanning the Worker bundle and its config for keys
