## baryovm publish nuget

Run dotnet nuget push after scanning the package and checking the source

### Synopsis

Runs dotnet nuget push with the same arguments, after two checks:

  files   the .nupkg is opened as a zip and every entry is scanned for keys
  source  --source (or -s), and --symbol-source, are checked against the allowed
          registries; with no --source the default is api.nuget.org

An entry over the size limit is refused, not skipped. A source that is a name
from NuGet.Config cannot be resolved here and is refused while a registry policy
is set: pass the feed URL. A folder source is always allowed. A refusal names
the file and line, never the value, and dotnet is not run.

The allowed registries come from git config (one --add per host) and the
BARYOVM_ALLOWED_REGISTRIES environment variable (comma separated). With neither
set, any source is allowed:

  git config --add baryovm.allowedRegistries pkgs.dev.azure.com

Flags after "nuget" belong to dotnet, so this command takes none of its own. For
JSON output of a refusal set BARYOVM_OUTPUT=json.

```
baryovm publish nuget <dotnet nuget push arguments> [flags]
```

### Examples

```
  baryovm publish nuget bin/Release/App.1.0.0.nupkg -s https://api.nuget.org/v3/index.json -k $KEY
```

### Options

```
  -h, --help   help for nuget
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm publish](baryovm_publish.md)	 - Publish an npm or NuGet package, refusing one that holds a key or aims at the wrong registry
