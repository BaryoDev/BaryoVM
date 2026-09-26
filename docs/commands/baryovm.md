## baryovm

Provision VMs and deploy apps to your own boxes

### Synopsis

BaryoVM turns provision → install Docker → deploy into one command.
CLI-first: the MAUI app and MCP server drive this same CLI (with -o json).

### Options

```
  -h, --help               help for baryovm
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm deploy](baryovm_deploy.md)	 - Deploy a container image to a VM
* [baryovm doctor](baryovm_doctor.md)	 - Check (and with --fix, auto-install) local prerequisites
* [baryovm stack](baryovm_stack.md)	 - Manage docker compose stacks on your VMs
* [baryovm up](baryovm_up.md)	 - Provision (if new) → install Docker → deploy, in one command
* [baryovm version](baryovm_version.md)	 - Print the BaryoVM version
* [baryovm vm](baryovm_vm.md)	 - Manage the VMs in your fleet
