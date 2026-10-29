## baryovm git

Run git, refusing a commit or push that carries a key or goes to the wrong host

### Synopsis

Runs git with the same arguments, after two checks:

  commit  scans the staged diff for keys (and the working diff with -a or
          paths) plus the message
  push    checks the remote host against the allowed hosts, then scans every
          commit not already on the remote

A refusal names the file and line and never prints the value, and git is not
run. Everything else passes straight through to git, with git's output and exit
code. A line carrying the comment allow-secret is skipped, for test fixtures.

The allowed hosts come from git config (one --add per host) and the
BARYOVM_ALLOWED_HOSTS environment variable (comma separated). With neither
set, any host is allowed:

  git config --add baryovm.allowedHosts github.com

Flags after "git" belong to git, so this command takes none of its own. For
JSON output of a refusal set BARYOVM_OUTPUT=json.

```
baryovm git <git arguments> [flags]
```

### Examples

```
  baryovm git commit -m 'add login'
  baryovm git push -u origin main
```

### Options

```
  -h, --help   help for git
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm](baryovm.md)	 - Provision VMs and deploy apps to your own boxes
