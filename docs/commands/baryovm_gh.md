## baryovm gh

Run gh, refusing a PR, release or issue whose text carries a key

### Synopsis

Runs gh with the same arguments. For pr create, release create and issue
create it first scans --title, --body, --body-file, --notes and --notes-file
for keys. A refusal names the file and line and never prints the value, and gh
is not run. A body read from stdin (-F -) is refused because it cannot be
checked: write it to a file. Every other gh command passes straight through
with gh's output and exit code.

Flags after "gh" belong to gh, so this command takes none of its own. For JSON
output of a refusal set BARYOVM_OUTPUT=json.

```
baryovm gh <gh arguments> [flags]
```

### Examples

```
  baryovm gh pr create --title 'Add login' --body-file body.md
```

### Options

```
  -h, --help   help for gh
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm](baryovm.md)	 - Provision VMs and deploy apps to your own boxes
