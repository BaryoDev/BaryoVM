## baryovm vm add

Register an existing VM by host + SSH key

### Synopsis

Register an existing VM and record whether it runs Linux or Windows. The OS
is detected once, over SSH, when the VM is added; pass --os to skip the
connection. If the host cannot be reached it is saved with no OS (read as
linux) and a warning; a host key mismatch still fails. With --os windows the SSH
shell is recorded as cmd, the OpenSSH default; pass --shell powershell if the
host's default shell is powershell.exe.

```
baryovm vm add <name> [flags]
```

### Options

```
  -h, --help           help for add
      --host string    public IP or hostname (required)
      --key string     path to the private SSH key (required)
      --os string      host OS, linux or windows (default: detect over SSH)
      --port int       SSH port (default 22)
      --shell string   SSH shell of a Windows host, cmd or powershell (with --os windows; default cmd)
      --user string    SSH user (default "ubuntu")
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm vm](baryovm_vm.md)	 - Manage the VMs in your fleet
