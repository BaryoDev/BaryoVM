## baryovm vm runner

Install a GitHub Actions self-hosted runner on a VM

### Synopsis

Install a GitHub Actions self-hosted runner for one private repo on a registered VM, over SSH.
It is the one place BaryoVM leaves an agent behind on the VM: GitHub's own runner, outbound
connections only, and only when you ask for it.

A public repo is refused, because a fork pull request could run its code on your VM.

The registration token is fetched locally with gh just before it is used and sent on stdin. It is
never stored, never on a command line and never in an error. The gh login needs admin rights on
the repo.

Rerunning is safe: a configured, running runner is left alone, a stopped one is started, and a
missing service is installed. The memory and CPU limits cover the runner and what it runs
directly. Containers a job starts through Docker run under dockerd and are not bounded by them.

```
baryovm vm runner <vm> [flags]
```

### Options

```
      --cpu-quota string    systemd CPUQuota for the runner service (default "200%")
  -h, --help                help for runner
      --labels string       extra labels, comma separated, added to self-hosted, Linux and the arch
      --memory-max string   systemd MemoryMax for the runner service (default "4G")
      --name string         runner name (default <vm>-<repo>)
      --repo string         repo to serve, as owner/name (required)
      --version string      runner version, such as 2.320.0 (default the latest release)
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm vm](baryovm_vm.md)	 - Manage the VMs in your fleet
* [baryovm vm runner remove](baryovm_vm_runner_remove.md)	 - Take a GitHub Actions runner off a VM and out of GitHub
