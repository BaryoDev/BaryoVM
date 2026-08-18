# BaryoVM CLI usage

> Early-stage. Built CLI-first in Go; the .NET MAUI app and MCP server will
> drive this same CLI via `-o json`. See the [README](README.md) for an overview.

## Build

```sh
go build -o /usr/local/bin/baryovm ./cmd/baryovm   # or anywhere on PATH
baryovm version
```

State lives in `~/.baryovm/fleet.json` (override with `BARYOVM_HOME`). It is
owner-only (0600) and references SSH key paths, not key contents.

## What works today (verified live against the Oracle VM)

- Register existing VMs and drive them over SSH (agentless, no daemon to install).
- Install Docker on a VM (idempotent).
- Deploy single containers, and manage docker compose stacks (the day-to-day path).
- `doctor` checks local prerequisites and auto-installs missing tools with `--fix`.
- Every command supports `-o json` for machine consumers (MAUI/MCP/agents).

## What is built but untested

- Provisioning a new Lightsail VM (`vm provision`, `up`). It is billable, so it
  ships behind `--dry-run` until you actually need a new box. OCI is stubbed.

## Fleet: VMs

```sh
# Register an existing VM (e.g. the Oracle box)
baryovm vm add oracle --host <your-vm-ip> --user opc --key ~/.ssh/id_ed25519

baryovm vm list
baryovm vm ping oracle           # SSH in, report host + Docker version
baryovm vm bootstrap oracle      # install Docker if missing
baryovm vm remove oracle         # forget it (does not destroy the machine)
```

## Stacks: docker compose over SSH (day-to-day deploys)

Your real workloads (barakoCMS, BaryoClub) run as compose projects, so this is
the main deploy path.

```sh
# Register a compose project living on a VM
baryovm stack add barako --vm oracle --path /opt/barakocms

baryovm stack list
baryovm stack ps barako                                   # compose ps
baryovm stack deploy barako --force-recreate --service app,admin
baryovm stack deploy barako --pull                        # pull images, then recreate
baryovm stack pull barako
baryovm stack logs barako --service app --tail 100
baryovm stack remove barako                               # forget the registration
```

`stack deploy` flags: `--service a,b` (limit to services), `--pull`,
`--force-recreate`, `--no-deps`.

### Release: sync source, build, deploy (config-driven)

`stack release` is the full deploy in one command: **back up → rsync source → build
images on the VM → compose up**. It's driven by a JSON manifest in your project, so the
pipeline is generic (no app-specific logic). The compose dir (with its `.env`) is never in
the sync list, so secrets can't be wiped by `--delete`.

`baryovm.release.json`:
```json
{
  "localRoot": "~/repos/BaryoClub",
  "remoteRoot": "/home/deploy/baryoclub-src/BaryoClub",
  "sync": ["api", "web"],
  "exclude": ["bin", "obj", "node_modules", ".next", ".git"],
  "builds": [
    { "image": "baryoclub-api:latest", "dockerfile": "deploy/Dockerfile.api", "context": "api" },
    { "image": "baryoclub-web:latest", "dockerfile": "deploy/Dockerfile.web", "context": "web",
      "args": { "NEXT_PUBLIC_API_URL": "" } }
  ]
}
```

```sh
baryovm stack add baryoclub --vm oracle --path …/deploy \
  --release-file ~/repos/BaryoClub/baryovm.release.json \
  --db-container deploy-postgres-1 --db-name baryoclub --env-file .env

baryovm stack release baryoclub                 # backup → sync → build → deploy
baryovm stack release baryoclub --no-backup     # skip the pre-release backup
baryovm stack release baryoclub --no-build      # just sync + compose up
baryovm stack release baryoclub --config ./other.release.json
```

Per-build options: `image`, `dockerfile`, `context` (both relative to `remoteRoot`), `args`
(build args), `noCache`.

Manifest options:

| key | default | what it does |
|---|---|---|
| `localRoot` | required | local source root, `~` allowed |
| `remoteRoot` | required | absolute destination on the VM |
| `sync` | required | paths under `localRoot` to rsync. **A trailing slash follows rsync's own rule** |
| `exclude` | none | rsync excludes |
| `builds` | none | images to build on the VM after syncing |
| `sudo` | `false` | run the **remote** rsync as root |
| `noCompose` | `false` | skip `docker compose up` at the end |
| `postDeploy` | none | commands to run on the VM after syncing and building |

#### The trailing slash on `sync` entries

This is rsync's rule, kept rather than invented, and it decides where files land:

- `"out"` copies the **directory**, giving `remoteRoot/out/...`
- `"out/"` copies its **contents**, giving `remoteRoot/...`

Get it wrong on a webroot and `--delete` empties the directory the site is served from while
putting the site one level down. Check with a dry run before the first release of a new stack:

```sh
rsync -az --delete --dry-run --itemize-changes --rsync-path="sudo -n rsync" \
  -e "ssh -i ~/.ssh/your_key" ~/repos/site/out/ user@host:/var/www/site/
```

### Static sites (no containers)

Not everything on a VM is a container. A built static site is files the host's own web server
serves, and it needs three things a compose stack does not: a root-owned destination, no
`compose up`, and a command or two afterwards.

```json
{
  "localRoot": "~/repos/baryo-web",
  "remoteRoot": "/var/www/baryo-web",
  "sync": ["out/"],
  "builds": [],
  "sudo": true,
  "noCompose": true,
  "postDeploy": [
    "sudo restorecon -R /var/www/baryo-web || true",
    "sudo nginx -t",
    "sudo systemctl reload nginx"
  ]
}
```

```sh
baryovm stack add baryo-web --vm oracle --path /var/www/baryo-web \
  --release-file ~/repos/baryo-web/baryovm.release.json

npm run build          # or whatever produces out/
baryovm stack release baryo-web
```

Three notes worth having before you run it:

**`sudo` elevates the remote rsync, not the local one.** It becomes `--rsync-path="sudo -n rsync"`,
because it is the copy of rsync running on the VM that needs to write into a root-owned path. The
account you SSH as needs passwordless sudo for it.

The `-n` matters. Without it, a host that does want a password writes the prompt into rsync's data
channel, which corrupts the protocol stream, so the transfer hangs or dies with something opaque
instead of telling you sudo needs a password. With `-n` it fails at once and says so.

**`postDeploy` runs in order and stops at the first failure.** Putting `nginx -t` before the reload
means a broken config fails the release loudly instead of reloading nothing and reporting success.

**No backup runs for a static stack**, because there is no database registered. The previous
release is not kept, so the rollback is to rebuild the previous commit and release again.

### Backups (pg_dump + config, over SSH)

Register the DB + config once, then back up / restore with one command:

```sh
baryovm stack add baryoclub --vm oracle --path /home/deploy/baryoclub-src/BaryoClub/deploy \
  --db-container deploy-postgres-1 --db-name baryoclub --env-file .env \
  --backup-dir /home/deploy/baryoclub-backups --keep 14

baryovm stack backup baryoclub      # pg_dump (custom format) + copy .env, prune to --keep
baryovm stack backups baryoclub     # list backups, newest first
baryovm stack restore baryoclub --yes                    # newest (REPLACES data)
baryovm stack restore baryoclub --file db-YYYY….dump --yes
```

If the project's `.env` is root-owned, the right posture for a file holding the database password,
add `--sudo`, and every docker and file operation for that stack runs through `sudo -n`. Without it
compose fails with "permission denied" reading the file, even though Docker itself is reachable.
Loosening the file to suit the tool would be the wrong trade.

```sh
baryovm stack add baryodev --vm oracle --path /opt/baryo-cms --sudo \
  --db-container baryo-postgres --db-name barako_cms --env-file .env
```

Backup config flags on `stack add`: `--db-container`, `--db-name`, `--db-user`
(default postgres), `--env-file` (relative to the project dir), `--backup-dir`
(default `~/<name>-backups`), `--keep` (default 14). Restore refuses without `--yes`.

Example: the barakoCMS admin health-page redeploy is one command:

```sh
baryovm stack deploy barako --force-recreate --service app,admin
```

### Updates (health-gated, with rollback)

`stack deploy` applies whatever is there. `stack update` decides whether to, and undoes it if the
result is not healthy:

```sh
baryovm stack set-update barako --health-url http://127.0.0.1:5005/health --auto
baryovm stack set-update baryoclub --health-url http://127.0.0.1:8091/health --no-auto

baryovm stack update barako --dry-run   # report what is pending, change nothing
baryovm stack update barako             # apply, verify, roll back if it does not come up
baryovm stack update barako --auto      # what a scheduler runs
```

What it does: pull, compare each container against what its reference now points at, and stop if
nothing is stale. An unchanged stack is never recreated, so a nightly job is not a nightly restart.
When something is stale it backs up the database, recreates only the affected services, and polls the
health URL from the VM. If health does not come back, it points the references at the images that were
running before and brings those back up, then checks again and says which of the two states it ended in.

`--auto` refuses any stack without `autoUpdate`, and any stack without a `healthUrl`: an unattended
update that cannot tell a healthy start from a crash loop is worse than no update at all. `--auto`
also refuses `--no-backup`. Set `autoUpdate` on the tiers you are willing to have change unattended:
playground, not production.

Run it from cron on the VM, or from anywhere with SSH access:

```sh
0 4 * * * /usr/local/bin/baryovm stack update barako --auto -o json >> ~/baryovm-update.log 2>&1
```

Verified against a stack whose image was swapped for one that exits on start: the update was applied,
the health check failed, the previous image was restored and the site was serving again, and
separately, that a genuine new image is kept when it comes up healthy.

## Single-container deploy

```sh
baryovm deploy --vm oracle --image nginx:alpine --name site -p 80:80
baryovm deploy --vm oracle --image app:latest --name app \
  -p 127.0.0.1:8080:8080 -e KEY=value --pull
```

## Provision + deploy in one (untested, billable)

```sh
# Preview only, no AWS calls:
baryovm vm provision web1 --provider lightsail --key ~/.ssh/id_ed25519 --dry-run

# The one button: provision (if new) -> install Docker -> deploy
baryovm up web1 --provider lightsail --key ~/.ssh/id_ed25519 \
  --image nginx:alpine --container site -p 80:80

# On an already-registered VM, up just bootstraps + deploys:
baryovm up oracle --image app:latest --container app -p 80:8080
```

## Prerequisites

```sh
baryovm doctor          # report local tools + cloud creds
baryovm doctor --fix    # download and install anything missing
```

Cloud APIs use the in-process Go SDKs, which read `~/.aws` and `~/.oci`
directly, so there is no cloud CLI to install for provisioning.

## JSON output

Add `-o json` to any command; MAUI/MCP/agents parse the result envelope:

```json
{ "ok": true, "action": "vm ping", "message": "oracle reachable",
  "data": { "docker": "Docker version 29.1.3, build f52814d",
            "host": "Linux 6.12.0-105.51.5.el9uek.aarch64" } }
```
