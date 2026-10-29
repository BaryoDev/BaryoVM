## baryovm vm health

Ask a VM the questions an owner should ask monthly (read only)

### Synopsis

Reads disk use on / and the docker root, pending updates split into security and
other, whether a reboot is required, containers that are restarting or exited
unexpectedly, and whether automatic security updates are enabled. Each is a line
with a verdict: ok, warn, fail, unknown or skip.

It changes nothing on the host. It needs `sudo -n` for docker and the package
manager; a check it cannot answer is reported as unknown and the rest still run.
Exits non-zero when any check is a fail, which includes any pending security update.

With --all it checks every registered VM. In -o json a single VM gives one report
in data, and --all gives a list of reports.

```
baryovm vm health [name] [flags]
```

### Options

```
      --all                check every registered VM
      --disk-fail int      disk percent used at which a filesystem fails (1-100) (default 90)
      --disk-warn int      disk percent used at which a filesystem warns (1-100, below --disk-fail) (default 80)
  -h, --help               help for health
      --max-restarts int   most restarts a running container may have before it warns (0 warns on any) (default 5)
      --max-updates int    most non-security updates allowed pending before updates warns (0 warns on any) (default 25)
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm vm](baryovm_vm.md)	 - Manage the VMs in your fleet
