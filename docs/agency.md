# Several client VMs, several people

For an agency with a VM per client and colleagues who deploy to them. The questions are about
trust: who deployed what, what happens when it breaks, and what you can show the client. It ends
with a committed project file and the JSON you can hand over.

> Not yet run on a fresh VM. Every command here exists in the current binary and its flags were
> checked against `--help`, but the output has not been copied from a real run.

## Layout: one VM per client

Register each client VM under a name that says whose it is, with its own key:

```sh
baryovm vm add acme-prod --host <ip> --user deploy --key ~/.ssh/acme_ed25519
baryovm stack add acme-web --vm acme-prod --path /opt/acme/web \
  --db-container web-db-1 --db-name acme --env-file .env \
  --release-file ./acme/web.release.json
```

`vm add` stores the key path, never the key. State is `~/.baryovm/fleet.json`, mode 0600.

## Share the fleet through a project file

`fleet.json` is one file on one machine, so a colleague or a CI job cannot use it. Write a
committed, secret-free project file from what you registered:

```sh
baryovm project export --out deploy/baryovm.project.json --name acme --stack acme-web
```

Commit it. Each person or runner supplies only its own key path, through the `env:` reference the
export wrote (`BARYOVM_DEPLOY_KEY_PATH` unless you pass `--key-env`). The file pins each VM's host
key, so a release never trusts a host on first use. Release from it:

```sh
BARYOVM_DEPLOY_KEY_PATH=~/.ssh/acme_ed25519 baryovm project release
```

It stops at the first failed release or verify. Details are in
[USAGE.md](../USAGE.md#project-files-one-fleet-definition-for-a-team-and-for-ci).

## Backup and update policy, per client

Decide it once per client and write it on the stack:

```sh
baryovm stack backup acme-web --pull          # a copy outside the client's VM
baryovm stack restore acme-web --drill        # proves the dump restores
baryovm stack set-update acme-web --health-url http://127.0.0.1:8080/health --no-auto
```

Use `--auto` only on tiers you are willing to change unattended, such as staging. A release always
backs up first, and `stack rollback acme-web` puts back the previous images (`--list` shows what
there is).

## What you can answer for

- Every command appends one line to `~/.baryovm/activity.jsonl`: what ran, on which VM and stack,
  and whether it worked. Arguments and error text are not recorded. See
  [activity-log.md](activity-log.md).
- `baryovm status -o json` reports every stack on every VM: containers up, last release, any
  failed release, backups.

## What to hand the client after a release

Today, hand over the output of these two:

```sh
baryovm stack release acme-web -o json
baryovm status --stack acme-web --verify -o json
```

A release says which verify plan ran, for example `verified by the manifest verify list, 2
check(s) passed on attempt 1`. In JSON that is `data.verify` with `source`, `checks` and
`attempts`. The envelope is described in [JSON.md](JSON.md). `status --verify` runs the stack's
verify checks once more, now.

A release record for clients is planned (#29). It does not exist yet.

## Done

You have a project file in git that a colleague can release from, a backup copy off the client's
VM with a passing drill, and the JSON a client can be given.
