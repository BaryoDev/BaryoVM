## baryovm cloud wrangler

Run wrangler after scanning the Worker bundle and its config for keys

### Synopsis

Runs wrangler with the same arguments. For deploy, publish and versions upload it
first checks three things:

  bundle     wrangler <command> --dry-run --outdir <scratch> builds the Worker, and
             every file in the bundle is scanned for keys
  config     wrangler.toml, wrangler.json or wrangler.jsonc (or the file named by
             --config) is scanned; values under vars are stored in clear text
  arguments  a key passed as --var NAME:value is refused for the same reason

A key is refused with the file and line, never the value, and the message points at
"wrangler secret put NAME". If the dry run fails, the deploy is refused. "pages
deploy <dir>" and "pages publish <dir>" scan the directory; --assets and the config assets directory are scanned too. Other wrangler commands pass through. Put
options after the command: a leading option is refused. --outdir is set by
baryovm and cannot be given.

Flags after "wrangler" belong to wrangler, so this command takes none of its own.
For JSON output of a refusal set BARYOVM_OUTPUT=json.

```
baryovm cloud wrangler <wrangler arguments> [flags]
```

### Examples

```
  baryovm cloud wrangler deploy --env production
```

### Options

```
  -h, --help   help for wrangler
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm cloud](baryovm_cloud.md)	 - Run wrangler, firebase or gcloud, refusing a deploy that ships a key or aims at the wrong project
