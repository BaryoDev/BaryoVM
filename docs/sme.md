# One server that already runs your product

For one machine somebody set up a while ago, with compose projects already running on it. You do
not start from a fresh VM. You register what is there, get a backup you have proved restores, and
turn on updates that undo themselves. It ends with a restore drill passing.

> Not yet run on a fresh VM. Every command here exists in the current binary and its flags were
> checked against `--help`, but the output has not been copied from a real run.

## 1. Register the machine

```sh
baryovm vm add prod --host <vm-ip> --user deploy --key ~/.ssh/id_ed25519
baryovm vm ping prod
```

`vm add` records the host key the first time it connects. Check the fingerprint against the
machine.

## 2. Find what is running

```sh
baryovm stack discover prod
```

Read only. For each compose project it prints the directory, the service count, a Postgres
container guess and the `stack add` command that would register it. The guess is the first service
whose image name contains `postgres`. Check it before you rely on it.

If the SSH user cannot reach the Docker socket, add `--sudo`.

Register what you found:

```sh
baryovm stack discover prod --adopt
```

It asks about each project. Without a terminal, add `--yes`.

## 3. Get backups working today

If a stack has one Postgres database, set it on the stack. Use the container and database names
from the discover output:

```sh
baryovm stack add shop --vm prod --path /opt/shop \
  --db-container shop-postgres-1 --db-name shop --env-file .env --keep 14
baryovm stack backup shop
baryovm stack backups shop
```

Running `stack add` again for an existing stack updates it in place. For other databases or for
files, use `stack set-backup shop --file backup.json`. The strategies are listed in
`baryovm stack set-backup --help` and in [USAGE.md](../USAGE.md#backup-strategies).

A copy that lives only on the VM dies with the VM. Pull one to your machine:

```sh
baryovm stack backup shop --pull
```

## 4. Prove the backup restores

```sh
baryovm stack restore shop --drill
```

This loads the newest dump into a throwaway Postgres container, checks it and removes the
container. It touches nothing live and needs no `--yes`.

## 5. Updates that go back on failure

```sh
baryovm stack set-update shop --health-url http://127.0.0.1:8080/health
baryovm stack update shop --dry-run
baryovm stack update shop
```

`stack update` pulls. If nothing changed, it does nothing. If something changed, it backs up,
recreates the affected services and checks the health URL from the VM. If that fails, it puts the
old images back.

To run it unattended, mark the stack, then schedule the command:

```sh
baryovm stack set-update shop --auto
baryovm stack update shop --auto --notify "$WEBHOOK_URL" -o json
```

`--auto` refuses a stack with no health check or no backup, on purpose. A scheduled GitHub Actions
example is in [examples/scheduled-update.yml](examples/scheduled-update.yml). That workflow has not
been run on a live runner.

## 6. See it in one place

```sh
baryovm status
baryovm vm health prod
```

## Done

You have the server registered, a backup copy on your machine and a restore drill that passed.
`baryovm stack restore shop --drill -o json` gives you the drill result as one JSON envelope.
