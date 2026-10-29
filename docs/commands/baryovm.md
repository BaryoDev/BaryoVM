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

* [baryovm cloud](baryovm_cloud.md)	 - Run wrangler, firebase or gcloud, refusing a deploy that ships a key or aims at the wrong project
* [baryovm deploy](baryovm_deploy.md)	 - Deploy a container image to a VM
* [baryovm doctor](baryovm_doctor.md)	 - Check (and with --fix, auto-install) local prerequisites
* [baryovm gh](baryovm_gh.md)	 - Run gh, refusing a PR, release or issue whose text carries a key
* [baryovm git](baryovm_git.md)	 - Run git, refusing a commit or push that carries a key or goes to the wrong host
* [baryovm guard](baryovm_guard.md)	 - Key and remote checks that run before git and gh
* [baryovm project](baryovm_project.md)	 - Release from a committed project file, or write one from your fleet
* [baryovm publish](baryovm_publish.md)	 - Publish an npm or NuGet package, refusing one that holds a key or aims at the wrong registry
* [baryovm repo](baryovm_repo.md)	 - Move a stack's source repository to another Git host
* [baryovm scan](baryovm_scan.md)	 - Run an OWASP ZAP baseline (passive) scan against a URL
* [baryovm site](baryovm_site.md)	 - Provision and deploy a static site on a VM (nginx, TLS, webroot)
* [baryovm stack](baryovm_stack.md)	 - Manage docker compose stacks on your VMs
* [baryovm up](baryovm_up.md)	 - Provision (if new) → install Docker → deploy, in one command
* [baryovm version](baryovm_version.md)	 - Print the BaryoVM version
* [baryovm vm](baryovm_vm.md)	 - Manage the VMs in your fleet
