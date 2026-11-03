## baryovm prove

Print the release record of a project as one page: what shipped, what was published, what was deployed

### Synopsis

Renders the record `baryovm ship` saved under $BARYOVM_HOME/ship/<name>/, and nothing else:
it never calls GitHub, a registry or a VM. A fact the record does not hold is printed as
not checked, so a project with no deployment check says "deployment not verified". Without
--version it picks the highest recorded version by semver order. A failed assertion or
failed step is shown as failed and the command exits non-zero. A dry run is refused.

```
baryovm prove <name> [flags]
```

### Examples

```
  baryovm prove shop
  baryovm prove shop --version 1.2.4
  baryovm prove shop -o json
```

### Options

```
  -h, --help             help for prove
      --version string   the version to prove (default: the highest recorded)
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   accepted for compatibility and does nothing: unknown hosts are refused by default (BARYOVM_STRICT_HOST_KEYS is likewise ignored)
      --trust-new-host     learn the key of a host not seen before, on this run only (also BARYOVM_TRUST_NEW_HOST=1); a changed key is always refused
```

### SEE ALSO

* [baryovm](baryovm.md)	 - Provision VMs and deploy apps to your own boxes
