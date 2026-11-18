## baryovm repo

Move a repository, scaffold its release CI, and verify trusted publishing

```
baryovm repo [flags]
```

### Options

```
  -h, --help   help for repo
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   always refuse an unknown host; =false learns its key on first use instead of asking (also BARYOVM_STRICT_HOST_KEYS=1 or 0). A project release never learns or asks, even with =false
```

### SEE ALSO

* [baryovm](baryovm.md)	 - Provision VMs and deploy apps to your own boxes
* [baryovm repo init](baryovm_repo_init.md)	 - Scaffold release CI from a template and protect the default branch
* [baryovm repo move](baryovm_repo_move.md)	 - Move a stack's repository to another Git host and repoint what pulls from it
* [baryovm repo verify](baryovm_repo_verify.md)	 - Run the publish workflow's dry run and report whether trusted publishing works
