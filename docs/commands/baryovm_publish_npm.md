## baryovm publish npm

Run npm publish after scanning the package and checking the registry

### Synopsis

Runs npm publish with the same arguments, after two checks:

  files     npm pack --dry-run --json lists what would ship, and every file is
            scanned for keys
  registry  the registry npm would publish to (publishConfig, then what
            npm config get registry answers, with all your
            flags, run in the current directory) is checked against the allowed
            registries; if npm cannot answer, the publish is refused

A package marked "private": true that would go to the public registry is
refused, with the registry named. A refusal names the file and line, never the
value, and npm publish is not run. A tarball argument and workspace flags are
refused because they cannot be checked.

The allowed registries come from git config (one --add per host) and the
BARYOVM_ALLOWED_REGISTRIES environment variable (comma separated). With neither
set, any registry is allowed:

  git config --add baryovm.allowedRegistries registry.npmjs.org

Flags after "npm" belong to npm, so this command takes none of its own. For
JSON output of a refusal set BARYOVM_OUTPUT=json. OTP and Trusted Publishing
stay with you.

```
baryovm publish npm <npm publish arguments> [flags]
```

### Examples

```
  baryovm publish npm --access public
  baryovm publish npm ./packages/core
```

### Options

```
  -h, --help   help for npm
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   accepted for compatibility and does nothing: unknown hosts are refused by default (BARYOVM_STRICT_HOST_KEYS is likewise ignored)
      --trust-new-host     learn the key of a host not seen before, on this run only (also BARYOVM_TRUST_NEW_HOST=1); a changed key is always refused
```

### SEE ALSO

* [baryovm publish](baryovm_publish.md)	 - Publish an npm or NuGet package, refusing one that holds a key or aims at the wrong registry
