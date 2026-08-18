<div align="center">
  <img src="logo.svg" alt="BaryoVM" width="96" height="96" />
  <h1>BaryoVM</h1>
  <p><em>PaaS-style deploys on your own cheap VMs: agentless, over SSH, from one CLI.</em></p>
  <p>
    <img alt="CI" src="https://github.com/BaryoDev/BaryoVM/actions/workflows/ci.yml/badge.svg" />
    <img alt="License: MPL-2.0" src="https://img.shields.io/badge/license-MPL--2.0-blue.svg" />
    <img alt="Go" src="https://img.shields.io/badge/Go-1.25-00ADD8.svg" />
  </p>
</div>

BaryoVM is a small Go CLI that registers your existing VMs and drives the Docker
Compose stacks on them **over plain SSH**, with no agent or daemon to install. It
gives you the day-to-day parts of a PaaS (deploy, release, backup, restore, logs)
without a control-plane to host, and every command speaks `-o json` so a future
UI, MCP server, or agent can drive the exact same surface.

> **Status: early-stage.** The SSH · compose · **release** · **backup/restore**
> paths are tested and used daily. VM *provisioning* (AWS Lightsail) is billable
> and ships behind `--dry-run`, so treat it as experimental. Not affiliated with any
> cloud provider.

## Install

```sh
go install github.com/BaryoDev/BaryoVM/cmd/baryovm@latest   # -> $(go env GOPATH)/bin
# or build locally:
go build -o /usr/local/bin/baryovm ./cmd/baryovm
baryovm version
```

State lives in `~/.baryovm/fleet.json` (override with `BARYOVM_HOME`), written
owner-only (`0600`). It stores hostnames, users and **SSH key paths, never key
contents or secrets**. See [SECURITY.md](SECURITY.md).

## Quickstart

```sh
# 1) Register a VM you already have (agentless, uses your SSH key)
baryovm vm add oracle --host <your-vm-ip> --user opc --key ~/.ssh/id_ed25519
baryovm vm ping oracle          # SSH in, report host + Docker version
baryovm vm bootstrap oracle     # install Docker if missing

# 2) Register a compose stack (a project dir on that VM), with backup + release config
baryovm stack add app --vm oracle --path /home/deploy/app/deploy \
  --db-container app-postgres-1 --db-name app --env-file .env \
  --release-file ./baryovm.release.json

# 3) Deploy: sync source -> build images on the VM -> compose up (backs up first)
baryovm stack release app

# 4) Backups, any time
baryovm stack backup app
baryovm stack restore app --yes
```

## Commands

| Area | Commands |
|------|----------|
| **VMs** | `vm add · list · ping · bootstrap · remove` |
| **Stacks** | `stack add · list · remove · ps · logs · pull` |
| **Deploy** | `stack deploy` (compose up) · **`stack release`** (sync → build → deploy) |
| **Backups** | **`stack backup`** · `stack backups` · **`stack restore --yes`** |
| **Single container** | `deploy` |
| **Provision** (experimental, billable) | `vm provision` · `up` (behind `--dry-run`) |
| **Local** | `doctor [--fix]` · `version` |

Full flags and examples: **[USAGE.md](USAGE.md)**.

### `stack release` is config-driven

A JSON manifest in your repo describes what to sync + build + deploy. The CLI has
no app-specific logic. The compose dir (with its `.env`) is deliberately **never**
in the sync list, so `--delete` can't wipe your secrets.

```json
{
  "localRoot": "~/repos/app",
  "remoteRoot": "/home/deploy/app",
  "sync": ["api", "web"],
  "exclude": ["bin", "obj", "node_modules", ".next", ".git"],
  "builds": [
    { "image": "app-api:latest", "dockerfile": "deploy/Dockerfile.api", "context": "api" },
    { "image": "app-web:latest", "dockerfile": "deploy/Dockerfile.web", "context": "web",
      "args": { "NEXT_PUBLIC_API_URL": "" } }
  ]
}
```

## Automation-friendly

Add `-o json` to any command for a stable result envelope:

```json
{ "ok": true, "action": "stack release", "message": "app released" }
```

This is how the planned MAUI desktop/mobile app and **MCP server** will drive
BaryoVM. The CLI is the single source of truth.

## Where it's going

BaryoVM today is the CLI core. The broader plan, a self-hosted control plane
(native app + MCP server) over Docker with provider adapters (SSH / Lightsail /
OCI), reverse-proxy and DNS ("type a domain, click"), GitHub deploys and email,
lives in [docs/VISION.md](docs/VISION.md).

## Development

```sh
go build ./...
go vet ./...
go test -race ./...
gofmt -l .          # should print nothing
```

## License

[MPL-2.0](LICENSE), open source; per-file notices in every source file. Chosen
deliberately: a tool that holds SSH key paths and drives your machines should be
auditable.
