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
      --strict-host-keys   accepted for compatibility and does nothing: unknown hosts are refused by default (BARYOVM_STRICT_HOST_KEYS is likewise ignored)
      --trust-new-host     learn the key of a host not seen before, on this run only (also BARYOVM_TRUST_NEW_HOST=1); a changed key is always refused
```

### SEE ALSO

* [baryovm vm runner](baryovm_vm_runner.md)	 - Install a GitHub Actions self-hosted runner on a VM
