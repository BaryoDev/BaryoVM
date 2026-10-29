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
- **Host identity**. Before running anything on a VM, BaryoVM checks the host's
  SSH key against `~/.baryovm/known_hosts` (override with `BARYOVM_HOME`),
  written **owner-only (`0600`)**. The first connection to a machine records the
  key and prints its fingerprint; every later connection must match. A key that
  has **changed** stops the command and prints both fingerprints, because a
  rebuilt VM and someone else answering on that address look identical from
  here. `baryovm vm forget-key <name-or-host>` drops one recorded key so the
  next connection learns the new one. There is no flag that disables the check
  for every host. Your own `~/.ssh/known_hosts` is read as an additional source,
  never written.
- **Host identity in CI**. Trust on first use assumes there is a first use. A
  runner with no memory between runs would learn a key every run, which is not
  verification. Set `BARYOVM_STRICT_HOST_KEYS=1` (or `--strict-host-keys`) and
  BaryoVM refuses an unknown host instead of learning it, so the job fails
  rather than trusting whatever answered. Record the key first, ideally from a
  committed file or a secret rather than an `ssh-keyscan` at deploy time.
- **App secrets**. BaryoVM never reads or copies your apps' secrets. Backups
  may copy a stack's config file (e.g. `.env`) into the remote backup directory
  on **your** VM; that file never leaves the machine through BaryoVM.
- **Cloud APIs**. Provider calls use the official Go SDKs, which read your
  standard `~/.aws` / `~/.oci` credentials directly. BaryoVM does not store cloud
  keys.
- **Publishing**. `baryovm publish npm` and `baryovm publish nuget` scan every file a package would ship for keys and refuse a registry outside `baryovm.allowedRegistries` (git config) or `BARYOVM_ALLOWED_REGISTRIES`. The npm registry is read from `npm config get`, and NuGet needs an explicit `--source` and symbol handling while a list is set. With neither set any registry is allowed; a policy that cannot be read refuses. Oversized files are refused, not skipped. See USAGE.md.
- **Remote commands**. All values interpolated into remote shell commands are
  single-quote escaped (`internal/sshx.Quote`, covered by tests) to prevent
  command injection.

- **GitHub runner tokens**. `vm runner` fetches a registration or removal token locally with
  your `gh` login, just before it is used, and sends it to the VM on **stdin**. It is read inside
  the `github-runner` user's shell and exported there for `config.sh`. It is never on a command
  line, never written to `fleet.json` or anywhere else by BaryoVM, and is scrubbed from any error
  or JSON output before it leaves the process.

- **Forced-command wrapper**. `baryovm remote serve`, when pinned as an SSH forced command, parses
  `SSH_ORIGINAL_COMMAND` with no shell and accepts only `baryovm-remote v1 <op> <stack>` for
  `update`, `backup`, `status` and `logs`. No byte of the request reaches a command line: the
  stack's directory, services, health URL and database settings come from `/etc/baryovm/remote.json`,
  and each is quoted with `sshx.Quote`. The wrapper refuses to run if that file or its directory is
  not owned by root, is writable by group or others, or is a symlink. `exec` and `release` are not
  offered. On other VMs baryovm echoes a random token first and refuses if a forced command
  swallowed it.

## Your responsibilities

- `vm runner` is the one opt-in exception to the agentless design: it leaves GitHub's runner
  running on the VM, making outbound connections only. It refuses public repos, since a fork pull
  request could run its code on the VM. The `github-runner` user is in the `docker` group, which
  is root-equivalent on that VM, so treat anything that can push to the repo as able to run as
  root there.

- Protect your SSH private keys and `~/.baryovm/fleet.json`.
- BaryoVM runs commands on hosts you register, so only add machines you control.
- `vm exec` runs arbitrary remote commands as the SSH user for that VM. That is
  the same authority as opening an interactive SSH session with the registered
  key; the CLI only shortens the path.
- Restore is destructive (`stack restore` replaces a database) and requires
  `--yes`.

## Reporting a vulnerability

Please report security issues privately to **arnelirobles@gmail.com** rather
than opening a public issue. Include steps to reproduce and the version
(`baryovm version`). We'll acknowledge and work on a fix before any public
disclosure.

## Scope / status

BaryoVM is early-stage. VM provisioning (Lightsail) is billable and prints a plan
only until `--yes` is passed; treat it as experimental. The SSH/compose/backup/release paths are
the tested, day-to-day surface.
