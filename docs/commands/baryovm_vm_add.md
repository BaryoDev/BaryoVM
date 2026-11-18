## baryovm vm add

Register an existing VM by host + SSH key

### Synopsis

Register an existing VM and record whether it runs Linux or Windows. The OS
is detected once, over SSH, when the VM is added; pass --os to skip the
connection. If the host cannot be reached it is saved with no OS (read as
linux) and a warning; a host key mismatch still fails. With --os windows the SSH
shell is recorded as cmd, the OpenSSH default; pass --shell powershell if the
host's default shell is powershell.exe.

An unknown host key is asked about on a terminal and refused with no terminal or
under -o json. A script or app that has had a person check the key can pass its
SHA256 fingerprint with --host-key-fingerprint: a host offering exactly that key
is recorded and the command continues, and any other key is refused, also for a
host that already has a recorded key. This holds under --strict-host-keys too,
since the flag is the checked key. It cannot be combined with --os or --remote
forced, which make no connection. A host with a different recorded key is still
refused.

```
baryovm vm add <name> [flags]
```

### Options

```
  -h, --help                          help for add
      --host string                   public IP or hostname (required)
      --host-key-fingerprint string   trust an unknown host that offers exactly this SHA256 fingerprint, without asking
      --key string                    path to the private SSH key (required)
      --os string                     host OS, linux or windows (default: detect over SSH)
      --port int                      SSH port (default 22)
      --remote baryovm remote serve   "forced" when the key is pinned to baryovm remote serve (see `vm remote install`)
      --shell string                  SSH shell of a Windows host, cmd or powershell (with --os windows; default cmd)
      --user string                   SSH user (default "ubuntu")
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   always refuse an unknown host; =false learns its key on first use instead of asking (also BARYOVM_STRICT_HOST_KEYS=1 or 0). A project release never learns or asks, even with =false
```

### SEE ALSO

* [baryovm vm](baryovm_vm.md)	 - Manage the VMs in your fleet
