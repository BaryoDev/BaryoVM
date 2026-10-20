## baryovm deploy

Deploy a container image to a VM

```
baryovm deploy [flags]
```

### Examples

```
  baryovm deploy --vm web1 --image nginx:alpine --name site -p 80:80
  baryovm deploy --vm web1 --image app:latest --name app -p 127.0.0.1:8080:8080 -e KEY=val --pull
```

### Options

```
  -e, --env stringArray       environment KEY=value (repeatable)
  -h, --help                  help for deploy
      --image string          container image (required)
      --name string           container name (required)
  -p, --publish stringArray   port mapping host:container (repeatable)
      --pull                  pull the image before running
      --vm string             target VM name (required)
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm](baryovm.md)	 - Provision VMs and deploy apps to your own boxes
