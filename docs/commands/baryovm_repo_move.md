## baryovm repo move

Move a stack's repository to another Git host and repoint what pulls from it

### Synopsis

Moves the source repository of a stack to another Git host without losing a branch or tag, and
repoints the places that pull from the old one. Without --yes (or with --dry-run) it prints the
plan and does nothing: no call to either host, no connection to a VM.

With --yes, in this order:

  1. read the branches and tags of both sides, and refuse when the destination already has
     commits that differ from the source (an identical destination is fine, so a rerun is safe)
  2. find checkouts of the repository on the registered VMs (--vm narrows the search), with
     the crontab and systemd lines that run from them; a checkout with local changes is
     reported and left alone
  3. export issues and pull requests, with their comments, from GitHub to issues.json and
     pulls.json in --export-dir (other hosts have nowhere to import them, so you keep the files)
  4. create the destination when --create is given (GitHub with gh, Azure Repos with az, using
     their own sign-in; any other host must be created by hand)
  5. push every branch and tag (no force, nothing deleted) and compare the two ref lists; they must
     be identical or nothing is repointed
  6. set origin of each checkout on the VMs, over SSH, with sudo -n where the stack has sudo
  7. repoint the local clone, keeping the old remote as old-origin
  8. record the new source on the stack

If a step after the push fails, the ones before it are put back. The old repository is never
deleted or archived: the commands for that are printed last. The crontab and systemd lines found
are listed so you can repoint them to the deployed copy; they are not edited.

No token goes on a command line or into an error. Both hosts use the sign-in of git, gh and az on
this machine. --read-cred takes a reference (env:NAME, file:PATH or cmd:<command>) to a read-only
token for https remotes; it is sent to the VM on stdin and stored in the checkout's .git directory
for git's credential store. URLs that carry a user or token are refused.

```
baryovm repo move <stack> [flags]
```

### Examples

```
  baryovm repo move app --to https://dev.azure.com/org/proj/_git/app --create
  baryovm repo move app --to git@git.example.com:team/app.git --local ~/src/app --yes
```

### Options

```
      --create              create the destination repository when it does not exist
      --dry-run             print the plan and do nothing (this is already the default without --yes)
      --export-dir string   where issues.json and pulls.json go (default ./<stack>-repo-export)
      --from string         the current remote URL (default the stack's recorded source, then the local clone's origin)
  -h, --help                help for move
      --local string        the local clone to repoint (its origin must be the old remote; "" skips it) (default ".")
      --read-cred string    reference to a read-only token for https checkouts: env:NAME, file:PATH or cmd:<command>
      --read-user string    user name stored with the --read-cred token (default "baryovm")
      --to string           the new remote URL (required)
      --vm strings          VM to search for checkouts (repeatable; default every registered VM)
      --yes                 really move; without it the plan is printed and nothing is done
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm repo](baryovm_repo.md)	 - Move a repository, scaffold its release CI, and verify trusted publishing
