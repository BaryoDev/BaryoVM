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

It also applies rules that each repository can switch off (git config --add
baryovm.rules.off <rule>): protected-branches (no direct or force push to the
default branch or baryovm.protectedBranches; --force-with-lease only on
baryovm.forceLeaseBranches), destructive (reset --hard with uncommitted work,
clean -fdx and deleting a tag on the remote need the target name typed back, or
--confirm <name> with no terminal or under JSON), files (.env, key and keystore
files, service account JSON and files over baryovm.maxFileSize, on the staged
files and the pushed range), text (no AI attribution in commit messages;
baryovm.houseStyle true adds style checks), gate (baryovm.gate script runs
before a push unless it passed on this commit) and audit (each guarded command
goes to the activity log). A refusal says how to pass it. Aliases are expanded
and checked as the command they stand for.

Flags after "git" belong to git, so this command takes none of its own, except
--confirm <name>, which baryovm consumes. For JSON output of a refusal set
BARYOVM_OUTPUT=json.

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
