# BaryoVM CLI usage

> Local, early-stage. Built CLI-first in Go; the .NET MAUI app and MCP server
> will drive this same CLI via `-o json`. Not on GitHub yet (immature).

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
baryovm vm add oracle --host 140.245.103.105 --user opc --key ~/.ssh/oracle_vm_key

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
  "remoteRoot": "/home/opc/baryoclub-src/BaryoClub",
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

### Backups (pg_dump + config, over SSH)

Register the DB + config once, then back up / restore with one command:

```sh
baryovm stack add baryoclub --vm oracle --path /home/opc/baryoclub-src/BaryoClub/deploy \
  --db-container deploy-postgres-1 --db-name baryoclub --env-file .env \
  --backup-dir /home/opc/baryoclub-backups --keep 14

baryovm stack backup baryoclub      # pg_dump (custom format) + copy .env, prune to --keep
baryovm stack backups baryoclub     # list backups, newest first
baryovm stack restore baryoclub --yes                    # newest (REPLACES data)
baryovm stack restore baryoclub --file db-YYYY….dump --yes
```

Backup config flags on `stack add`: `--db-container`, `--db-name`, `--db-user`
(default postgres), `--env-file` (relative to the project dir), `--backup-dir`
(default `~/<name>-backups`), `--keep` (default 14). Restore refuses without `--yes`.

Example: the barakoCMS admin health-page redeploy is one command:

```sh
baryovm stack deploy barako --force-recreate --service app,admin
```

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
