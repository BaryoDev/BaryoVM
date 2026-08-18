# Security

BaryoVM drives other machines over SSH, so it's designed to hold as little
sensitive material as possible and to keep what it does hold local and locked
down.

## How BaryoVM handles credentials

- **SSH keys**. BaryoVM stores only the **path** to your private key
  (`keyPath`), never the key contents. Authentication uses your existing key
  files (and any `ssh-agent`), exactly as your normal `ssh` does.
- **Fleet state**. VM and stack registrations live in `~/.baryovm/fleet.json`
  (override with `BARYOVM_HOME`), written **owner-only (`0600`)**. It contains
  hostnames, users, key paths and stack config, and **no passwords, tokens, or key
  material**.
- **App secrets**. BaryoVM never reads or copies your apps' secrets. Backups
  may copy a stack's config file (e.g. `.env`) into the remote backup directory
  on **your** VM; that file never leaves the machine through BaryoVM.
- **Cloud APIs**. provider calls use the official Go SDKs, which read your
  standard `~/.aws` / `~/.oci` credentials directly. BaryoVM does not store cloud
  keys.
- **Remote commands**. all values interpolated into remote shell commands are
  single-quote escaped (`internal/sshx.Quote`, covered by tests) to prevent
  command injection.

## Your responsibilities

- Protect your SSH private keys and `~/.baryovm/fleet.json`.
- BaryoVM runs commands on hosts you register, so only add machines you control.
- Restore is destructive (`stack restore` replaces a database) and requires
  `--yes`.

## Reporting a vulnerability

Please report security issues privately to **arnelirobles@gmail.com** rather
than opening a public issue. Include steps to reproduce and the version
(`baryovm version`). We'll acknowledge and work on a fix before any public
disclosure.

## Scope / status

BaryoVM is early-stage. VM provisioning (Lightsail) is billable and ships behind
`--dry-run`; treat it as experimental. The SSH/compose/backup/release paths are
the tested, day-to-day surface.
