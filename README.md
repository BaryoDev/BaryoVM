# BaryoVM — planning doc

> **Status: PARKED / tabled** (drafted 2026-07-15). This is a design record to pick up later.
> Nothing built yet. No infrastructure touched.

A self-hosted control plane to manage a fleet of VMs and the apps/databases/domains on them —
from a UI, a CLI, and an MCP server — instead of doing everything by hand over SSH.

**Model: open source, MPL-2.0** (same as Talaan and the barakoCMS modules) — free to use, source
published, file-level copyleft. Right call for an infra tool: it holds your SSH keys + secrets, and
people trust what they can audit. Puts it on equal footing with open-source rivals (Coolify/Dokploy),
so the differentiation rests where it should — MCP-first, native mobile, Baryo-ecosystem fit.

---

## Why (the problem)

Deploying one site to the existing Oracle VM today takes: SSH in → `rsync` source → `docker build`
→ hand-write a `.env` → `docker compose up` → write an nginx server block → run `certbot` → reload
nginx → add a GoDaddy A record. The VM already juggles nginx per-domain TLS by hand, certbot
renewals, ~6 containers, several databases, and credentials scattered in `CREDENTIALS.md` files.

BaryoVM erases that: **click a domain → live HTTPS site**, plus databases, secrets, and DNS handled
for you.

---

## Decisions already made (log)

1. **Engine strategy: thin layer over Docker API + a reverse proxy.** Not a from-scratch PaaS, not
   wrapping Coolify/Dokploy. BaryoVM owns the API/CLI/MCP/UI; Docker + the proxy do the heavy lifting.
2. **Do NOT touch the current Oracle VM.** It stays on nginx + certbot, untouched. BaryoVM is designed
   and first proven against a **new** Linux VM.
3. **Reverse proxy is an adapter, not a forced migration.**
   - Existing boxes → `NginxProxy` (BaryoVM scripts the nginx + certbot steps we already do by hand).
   - Greenfield boxes → `TraefikProxy` (label-driven, auto-TLS) — no migration cost, so use it there.
   - Traefik's advantage is *where complexity lives*: routing + TLS become a property of the container
     (Docker is the source of truth), so BaryoVM stays a thin "run container, done" layer instead of a
     config-file templating + certbot-lifecycle manager. Blast radius is one container, not all sites.
     But it is **not required** — nginx-scripted is a valid path for what already exists.
4. **First target: a fresh AWS Lightsail Ubuntu VM** that we connect to over SSH.
5. Reuse the Baryo ecosystem: Verdict (results), Carom (resilience on SSH/Docker/DNS calls),
   BarakoCMS.Email.Resend (email). State in Postgres/Marten. Secrets in an encrypted vault.

---

## Architecture

Everything is a **control plane + thin front-ends + pluggable provider adapters** (the barakoCMS
module idea, applied to infrastructure).

```
        ┌────────── front-ends (thin) ──────────┐
        │  MAUI app   ·   CLI   ·   MCP server   │   all call the same API
        └───────────────────┬────────────────────┘
                   BaryoVM Control API (.NET / FastEndpoints)
        inventory · desired-state · secrets vault · audit · orchestration
                   │  SSH + Docker API (agentless)
        ┌──────────┴───────────┬───────────────────┐
       VM (Lightsail)       VM …               VM …
        Docker + Traefik (greenfield)  /  Docker + nginx (existing)

Provider adapters:
 ├─ IVmProvider      → Ssh (any Linux box) · Lightsail (AWS API) · OracleCloud (OCI) · Hetzner · DO …
 ├─ IReverseProxy    → Traefik (greenfield) · Nginx (existing, scripted)
 ├─ IGitProvider     → GitHub (App + webhooks) · GitLab …
 ├─ IDnsProvider     → GoDaddy · Cloudflare …
 └─ IEmailProvider   → Resend (reuse BarakoCMS.Email.Resend)
```

**Agentless:** the Control API talks to each VM's Docker daemon over an SSH tunnel
(`ssh://user@host`, Docker.DotNet). No daemon to install. An optional per-VM agent can be added
later for live status/log streaming.

---

## Interfaces

- **Control API** (.NET, FastEndpoints + Marten/Postgres for state) — the brain; everything else is a client.
- **CLI** — `baryovm deploy`, `baryovm db create`, `baryovm vm add` (System.CommandLine) over the API.
- **MCP server** — exposes the API as agent tools; an AI agent can run the whole fleet conversationally.
  This is the sharpest differentiator vs. Coolify/Dokploy (which are web-only, no MCP).
- **MAUI app** (Blazor Hybrid) — native **desktop + phone**; manage infra from your phone. Shares UI
  with a web Blazor front-end. Also a differentiator.

### MCP / CLI tool surface (initial)
`list_vms · vm_status · add_vm · deploy_site · redeploy · list_apps · stop_app · create_database ·
list_databases · set_secret · rotate_secret · set_dns_record · app_logs · provision_tls`

---

## Capabilities

### Manage Linux + Oracle VMs
- **Manage any existing VM:** register by host + SSH key → BaryoVM drives its Docker/apps. Works for
  Lightsail, Hetzner, a Pi — anything with SSH + Docker. (MVP.)
- **Provision new VMs:** `LightsailProvider` / `OracleCloudProvider` use the cloud APIs to create an
  instance, allocate a static IP, open 80/443, install Docker + Traefik, and auto-register. ("New VM"
  becomes a button.) Later than MVP.

### Connect to GitHub
- A **GitHub App** → list repos; **deploy = pick repo + branch** (clone, build, ship with proxy labels).
- **Webhooks** → push to `main` auto-redeploys. Your CI/CD, no per-repo Actions YAML.

### Type a domain, click → configures GoDaddy ("like Resend")
- GoDaddy **REST API** (developer.godaddy.com key+secret) → deploy a site and the `A` record is created
  automatically to the VM's IP. No dashboard.
- Email setup writes the Resend **MX / SPF / DKIM** records into GoDaddy, then polls Resend to flip
  verification green.

### Databases & secrets
- "Add Postgres/Redis" → run container, create db+user, store creds in the vault, return the connection
  string, attach to the app as env.
- **Encrypted secrets vault** (envelope encryption, stored in Postgres) — replaces the `CREDENTIALS.md`
  files entirely.

---

## First scenario: a fresh Lightsail VM (the greenfield path)

**You do (once, ~2 min in the AWS console):** create an **Ubuntu** Lightsail instance, **attach a static
IP**, **open ports 22/80/443** in the Lightsail firewall, download the **.pem key** (user `ubuntu`).

**BaryoVM onboards it ("Add VM"):** you give it the static IP + `ubuntu` + .pem (stored in the vault).
It SSHes in → installs Docker → creates the `baryo-edge` network → deploys **Traefik** (80/443, Docker
provider, Let's Encrypt resolver, persistent `acme.json`) → optional hardening (ufw, auto-updates,
fail2ban). VM shows "ready" in the fleet.

**Deploy a site (the click):** pick a GitHub repo+branch (or image), type `app.baryo.dev`, deploy.
BaryoVM builds/pulls → runs the container with Traefik labels → injects vault secrets → calls the
GoDaddy API to create the `app` A-record → Traefik routes + issues the cert. **Live at
`https://app.baryo.dev`** — no SSH, no nginx, no certbot, no manual DNS. Push later → webhook →
auto-redeploy.

---

## Proposed solution layout (.NET)

```
BaryoVM.Core      — domain: Vm, App, Database, Secret, Deployment, DnsRecord + interfaces
BaryoVM.Api       — control plane (FastEndpoints + Marten/Postgres)
BaryoVM.Docker    — remote Docker control via Docker.DotNet over SSH
BaryoVM.Proxy     — IReverseProxy: TraefikProxy + NginxProxy
BaryoVM.Vm        — IVmProvider: SshVmProvider (+ Lightsail/OCI later)
BaryoVM.Dns       — IDnsProvider: GoDaddy (+ Cloudflare)
BaryoVM.Git       — IGitProvider: GitHub App + webhook receiver
BaryoVM.Secrets   — encrypted vault (envelope encryption)
BaryoVM.Cli       — System.CommandLine client
BaryoVM.Mcp       — MCP server over the API
BaryoVM.App       — MAUI Blazor Hybrid (desktop + mobile)
```

Reuse: **Verdict**, **Carom**, **BarakoCMS.Email.Resend**.

---

## Phased roadmap

- **Phase 0 — Substrate.** Onboard a fresh Lightsail VM: SSH register → bootstrap Docker + Traefik.
  (Greenfield → Traefik; Oracle untouched.)
- **Phase 1 — Control API + CLI.** Deploy-a-site (image first, then GitHub build), secrets vault,
  GoDaddy DNS adapter, database provisioning. "New site / new db" become one call.
- **Phase 2 — MCP server** over the API.
- **Phase 3 — MAUI app** (desktop + phone).
- **Later** — VM provisioning adapters (Lightsail/OCI), GitHub push-to-deploy webhooks, more DNS/VM
  providers, per-VM agent for live logs.

---

## Credentials / integrations to wire (when we resume)

- **Lightsail:** static IP + `ubuntu` user + .pem key. Open 22/80/443 in the Lightsail firewall.
- **GoDaddy API key + secret** (developer.godaddy.com) — for click-to-DNS.
- **GitHub App** — for repo deploys + push-to-deploy (optional at first; image deploys need nothing).
- **AWS keys / OCI keys** — only if/when we auto-*provision* VMs (managing existing ones needs just SSH).

All stored in BaryoVM's encrypted vault, never in markdown files.

---

## Use case

**Who:** a solo dev / small team / indie consultant who runs their **own** cheap VMs (Lightsail,
Hetzner, Oracle free tier) and puts several small apps + databases on them.

**Job to be done:** *"Give me a domain and a repo/image, make it a live HTTPS site on my own box —
and let me manage all my VMs, apps, DBs, domains, and secrets from one place: by clicking, from my
phone, or by telling an AI agent."*

The PaaS experience (Vercel/Render ease) on infrastructure **you** own and pay a flat VM price for —
without the SSH + nginx + certbot toil, and without per-app cloud bills. For BaryoDev specifically,
it's the cockpit for barakoCMS, umami, BaryoClub, and whatever's next.

## How it competes

Three things it's measured against:

1. **Doing it by hand (SSH + docker + nginx + certbot).** The baseline. BaryoVM turns a 9-step deploy
   into one click. Easy win — but only vs. yourself.
2. **Managed PaaS (Vercel, Render, Railway, Fly.io).** Easy, but you rent their infra, costs scale per
   app, and there's lock-in. BaryoVM gives similar ease on **your** boxes at a flat VM cost, fully
   owned. Wins on cost + control; loses on zero-ops polish.
3. **Self-hosted PaaS (Coolify, Dokploy, CapRover, Komodo, Portainer).** The real competitors — mature,
   open-source, big communities. **Do not out-feature them.** BaryoVM competes on a narrow, sharp edge:

   - **AI-native / MCP-first** — an AI agent can provision, deploy, and operate the fleet. None of them
     have this. Timely and genuinely novel.
   - **Native desktop + MOBILE app (MAUI)** — they're web dashboards; BaryoVM manages infra from your
     phone with a native app. Manage-on-the-go + push alerts.
   - **Opinionated Baryo-ecosystem fit** — one-click barakoCMS, Resend email + GoDaddy DNS wired
     together, curated for the solo-dev-with-own-VMs workflow rather than every option under the sun.

**Honest headwinds:** Coolify/Dokploy are open source with years of features and community — hard to
match on breadth. Being open source (MPL-2.0) puts BaryoVM on the same trust footing (auditable, no
lock-in), so it competes on the wedge rather than defending a closed binary. The wedge stays
narrow-and-deep (MCP + mobile + ecosystem), not broad. If the differentiation ever stops mattering,
the fallback is to wrap Dokploy's API instead of owning the engine.

---

## Open questions (parked)

- MAUI desktop-first or mobile-first for the initial UI?
- Vault master-key custody: key file on the control host, or a cloud KMS?
- Build strategy for GitHub deploys: Dockerfile-only, or add buildpacks (e.g. Paketo) for repos
  without a Dockerfile?
- Does the Control API itself run on one of the managed VMs, or a separate always-on host?
