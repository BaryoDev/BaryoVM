## baryovm publish

Publish an npm or NuGet package, refusing one that holds a key or aims at the wrong registry

```
baryovm publish [flags]
```

### Options

```
  -h, --help   help for publish
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm](baryovm.md)	 - Provision VMs and deploy apps to your own boxes
* [baryovm publish npm](baryovm_publish_npm.md)	 - Run npm publish after scanning the package and checking the registry
* [baryovm publish nuget](baryovm_publish_nuget.md)	 - Run dotnet nuget push after scanning the package and checking the source
