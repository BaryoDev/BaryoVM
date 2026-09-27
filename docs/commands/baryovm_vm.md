## baryovm vm

Manage the VMs in your fleet

### Options

```
  -h, --help   help for vm
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm](baryovm.md)	 - Provision VMs and deploy apps to your own boxes
* [baryovm vm add](baryovm_vm_add.md)	 - Register an existing VM by host + SSH key
* [baryovm vm bootstrap](baryovm_vm_bootstrap.md)	 - Install Docker on a VM (idempotent)
* [baryovm vm exec](baryovm_vm_exec.md)	 - Run a command on a registered VM over SSH
* [baryovm vm forget-key](baryovm_vm_forget-key.md)	 - Forget a VM's recorded SSH host key, so the next connection learns it again
* [baryovm vm harden](baryovm_vm_harden.md)	 - Apply the SSH hardening policy to a VM
* [baryovm vm list](baryovm_vm_list.md)	 - List registered VMs
* [baryovm vm ping](baryovm_vm_ping.md)	 - SSH in and report host + Docker status
* [baryovm vm provision](baryovm_vm_provision.md)	 - Provision a new cloud VM and register it in the fleet
* [baryovm vm remove](baryovm_vm_remove.md)	 - Remove a VM from the fleet (does not destroy the machine)
* [baryovm vm threats](baryovm_vm_threats.md)	 - Show what a VM is being attacked with
