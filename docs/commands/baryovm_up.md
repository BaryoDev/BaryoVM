## baryovm up

Provision (if new) → install Docker → deploy, in one command

### Synopsis

If <name> is already registered, up just installs Docker and deploys.
If it is new, pass --provider/--key to provision it first.

```
baryovm up <name> [flags]
```

### Examples

```
  baryovm up web1 --image nginx:alpine --container site -p 80:80
  baryovm up web1 --provider lightsail --key ~/.ssh/id_ed25519 --image app:latest --container app -p 80:8080
```

### Options

```
      --container string      container name (required)
      --dry-run               print the plan without creating anything
  -e, --env stringArray       environment KEY=value (repeatable)
  -h, --help                  help for up
      --image string          container image (required)
      --key string            private SSH key to authorize + connect with (required)
      --provider string       cloud provider: lightsail (oci coming) (default "lightsail")
  -p, --publish stringArray   port mapping host:container (repeatable)
      --pull                  pull the image before running
      --region string         cloud region (default: from ~/.aws)
      --size string           instance size / bundle (default: provider's smallest)
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm](baryovm.md)	 - Provision VMs and deploy apps to your own boxes
