## baryovm vm runner remove

Take a GitHub Actions runner off a VM and out of GitHub

### Synopsis

Stop and uninstall the runner service, unregister it from GitHub with a remove token (fetched
locally, sent on stdin), and delete the runner directory. If no remove token can be fetched, or
GitHub rejects it, the local runner is still removed and the GitHub entry has to be deleted by
hand. If that local removal fails too, the command fails and the directory stays for another try.

```
baryovm vm runner remove <vm> [flags]
```

### Options

```
  -h, --help          help for remove
      --name string   runner name (default <vm>-<repo>)
      --repo string   repo the runner serves, as owner/name (required)
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm vm runner](baryovm_vm_runner.md)	 - Install a GitHub Actions self-hosted runner on a VM
