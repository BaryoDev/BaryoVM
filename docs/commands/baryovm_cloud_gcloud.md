## baryovm cloud gcloud

Run gcloud, refusing a command that writes a key file inside a git working tree

### Synopsis

Runs gcloud with the same arguments. Everything passes through except commands that
write a key or secret to a file:

  iam service-accounts keys create <file>
  secrets versions access ... --out-file <file>
  kms decrypt ... --plaintext-file <file>

(with or without alpha or beta). If that file is inside a git working tree, the
command is refused before gcloud runs, and the message says to write it outside the
repository. "-" and /dev/stdout are allowed. If a command looks like key creation
but cannot be read, or git cannot say whether the path is in a repository, it is
refused.

Flags after "gcloud" belong to gcloud, so this command takes none of its own.
For JSON output of a refusal set BARYOVM_OUTPUT=json.

```
baryovm cloud gcloud <gcloud arguments> [flags]
```

### Examples

```
  baryovm cloud gcloud iam service-accounts keys create ~/.config/key.json --iam-account sa@p.iam.gserviceaccount.com
```

### Options

```
  -h, --help   help for gcloud
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm cloud](baryovm_cloud.md)	 - Run wrangler, firebase or gcloud, refusing a deploy that ships a key or aims at the wrong project
