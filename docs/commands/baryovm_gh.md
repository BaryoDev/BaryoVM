## baryovm gh

Run gh, refusing a PR, release or issue whose text carries a key

### Synopsis

Runs gh with the same arguments. For pr create, release create and issue
create it first scans --title, --body, --body-file, --notes and --notes-file
for keys. A refusal names the file and line and never prints the value, and gh
is not run. A body read from stdin (-F -) is refused because it cannot be
checked: write it to a file. Every other gh command passes straight through
with gh's output and exit code.

Per-repository rules, each off with git config --add baryovm.rules.off <rule>:
text (no AI attribution trailer or footer in what gh publishes; baryovm.houseStyle
true adds style checks), destructive (repo delete and release delete need the
name typed back, or --confirm <name> with no terminal or under JSON), visibility
(repo create needs --public, --private or --internal, and the one baryovm.visibility
names) and audit.

Flags after "gh" belong to gh, so this command takes none of its own, except
--confirm <name>, which baryovm consumes. For JSON output of a refusal set
BARYOVM_OUTPUT=json.

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
      --strict-host-keys   accepted for compatibility and does nothing: unknown hosts are refused by default (BARYOVM_STRICT_HOST_KEYS is likewise ignored)
      --trust-new-host     learn the key of a host not seen before, on this run only (also BARYOVM_TRUST_NEW_HOST=1); a changed key is always refused
```

### SEE ALSO

* [baryovm](baryovm.md)	 - Provision VMs and deploy apps to your own boxes
