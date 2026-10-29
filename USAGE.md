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
- `doctor` checks the local tools a release actually runs, and installs what it can with `--fix`.
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
baryovm vm exec oracle -- uname -sr
baryovm vm exec oracle -- df -h /var/www
baryovm vm exec oracle -o json -- cat /etc/os-release
baryovm vm bootstrap oracle      # install Docker if missing
baryovm vm bootstrap oracle --dry-run   # print the install script, no connection
baryovm vm runner oracle --repo acme/app   # GitHub Actions self-hosted runner
baryovm vm runner remove oracle --repo acme/app
baryovm vm remove oracle         # forget it (does not destroy the machine)
baryovm vm forget-key oracle     # drop its recorded SSH host key
```

### Host keys

The first connection to a machine records its SSH host key in
`~/.baryovm/known_hosts` and prints the fingerprint. Every connection after that
must match, and one that does not stops the command:

```text
host key for 203.0.113.10:22 changed
  stored   SHA256:aWxF...
  offered  SHA256:9KpQ...
```

A rebuilt VM is the ordinary reason, and so is someone answering on that address
who should not be. BaryoVM cannot tell them apart, so it stops. If you rebuilt
the machine, `baryovm vm forget-key <name-or-host>` drops the old key and the
next connection learns the new one. There is no flag that turns the check off
for every host.

### Host keys in CI

Trust on first use assumes there is a first use. A CI runner has no memory
between runs, so every run is the first one, and learning a key on each run
means trusting whatever answers on that address, every time, with a key that can
dump your database.

Strict mode refuses to learn. An unknown host is an error:

```sh
export BARYOVM_STRICT_HOST_KEYS=1     # or --strict-host-keys
```

So a CI job records the key first, and fails rather than trusting a new one:

```yaml
- name: Pin the host key
  run: |
    mkdir -p ~/.baryovm
    ssh-keyscan -t ecdsa,ed25519 "$VM_HOST" >> ~/.baryovm/known_hosts

- name: Deploy
  env:
    BARYOVM_STRICT_HOST_KEYS: "1"
  run: baryovm stack release myapp
```

Better still, commit the expected `known_hosts` or keep it in a secret, so the
pin is reviewed rather than fetched from the network on every run. `ssh-keyscan`
at deploy time trusts whatever answers; a committed file does not.

The flag beats the environment, so `--strict-host-keys=false` is how a human
turns it off for one command on a machine where the workflow sets it globally.

`vm exec` runs an arbitrary command on one named VM with the SSH key already in
`fleet.json`. Everything after `--` is the remote command, so its own flags reach
the VM. Put `sudo -n` in the remote command when you need root; there is no
`--sudo` flag. A non-zero remote exit becomes a non-zero CLI exit. Under
`-o json` the envelope keeps `stdout`, `stderr` and `exitCode` as separate fields.

`vm add` connects once, over SSH, to detect the host's OS and records it in `fleet.json`;
give `--os linux|windows` and it does not connect at all. Any host with a POSIX shell
(Linux, macOS, FreeBSD) is recorded as `linux`. If the host cannot be reached (refused,
timed out) or does not answer the probe, `vm add` still saves it, with no OS, which
reads as Linux, and warns; run `vm add` again when the host is up, or pass `--os windows`.
A host key that does not match the one on record is not "unreachable": `vm add` fails
and saves nothing. Entries from before this existed are read as Linux, and an `os` that
is neither `linux` nor `windows` is refused by every command that uses it.

On a Windows VM (OpenSSH Server, administrator account) only `vm exec` works so far.
The words after `--` run as one PowerShell command. Pass the command as one quoted
argument, so your own shell leaves its spaces, quotes and pipes alone:

```sh
baryovm vm exec win -- 'Get-ChildItem "C:\Program Files" | Select-Object -First 3'
```

The exit code, and errors as plain text, come back under either OpenSSH default shell
(cmd or powershell). `vm add` detects which one and records it as `shell`, so re-add a
VM if you change its default shell. With `--os windows` nothing is detected and the
shell is recorded as `cmd`; add `--shell powershell` when the host's default is
powershell.exe. `--shell` is only valid with `--os windows`. A script is sent encoded,
which makes it about three times longer, so a very long one is refused before connecting
(cmd.exe takes a command line of 8191 characters, powershell.exe 32767). Every other
command stops with a message naming it and the VM.

### GitHub Actions runner

```sh
baryovm vm runner oracle --repo acme/app
baryovm vm runner oracle --repo acme/app --labels docker,gpu --memory-max 6G --cpu-quota 300%
baryovm vm runner remove oracle --repo acme/app
```

`vm runner` installs a self-hosted runner for one repo on a registered VM. It needs the `gh`
CLI logged in with admin rights on the repo, and Docker on the VM (`vm bootstrap`). It creates a
`github-runner` user in the docker group, unpacks the runner under
`/opt/github-runner/<owner>__<repo>` (lowercase), registers it, installs it as a systemd service and checks
that the service is active.

| Flag | Default | |
|------|---------|---|
| `--repo` | required | `owner/name` |
| `--name` | `<vm>-<repo>` | runner name; reruns use `--replace`, so one registration stays |
| `--labels` | none | extra labels on top of `self-hosted`, `Linux` and the arch |
| `--version` | latest release | pin a runner version such as `2.320.0` |
| `--memory-max` | `4G` | systemd `MemoryMax` for the service |
| `--cpu-quota` | `200%` | systemd `CPUQuota` for the service |

It is safe to rerun. A configured, running runner is reported as `alreadyReady` and nothing
changes. A stopped one is started, and one with no service gets the service installed. A rerun
after a crash continues from the step that failed. If you pass `--memory-max` or `--cpu-quota`
explicitly, they are applied even to a runner that is otherwise ready: the drop-in is rewritten
and the service restarted, and the result reports `limitsApplied`. Leave them out and a ready
runner is not touched.

- A repo whose visibility is not `private` is refused before BaryoVM connects to the VM, because a
  fork pull request could run its code there. That includes enterprise `internal` repos, which
  every enterprise member can see and fork.
- Also turn on "Require approval for all outside collaborators" for fork pull request workflows
  in the repo's Settings, Actions, General page.
- The runner tarball is checked against the sha256 digest GitHub publishes for the release asset
  before it is extracted. A release with no digest is refused.
- The memory and CPU limits cover the runner and what it runs directly. Containers a job starts
  through Docker (Testcontainers, `docker run`) run under `dockerd` and are not bounded by them.
- `vm runner remove` stops and uninstalls the service, unregisters the runner and deletes its
  directory. If no remove token can be fetched, or GitHub rejects it, it removes the local runner
  anyway and says so, and you have to delete the entry on the repo's Settings, Actions, Runners
  page by hand. If that local removal fails too, the command fails and leaves the directory in place
  for another try.

## Hardening, and seeing what hits the box

A VM with a public SSH port is found within minutes and guessed at continuously.
`vm harden` applies an opinionated, idempotent policy, and `vm threats` tells you
what that policy is absorbing.

```sh
baryovm vm harden oracle --dry-run   # report what would change, write nothing
baryovm vm harden oracle             # apply it
baryovm vm harden oracle --ignore 10.0.0.0/24   # never ban this range
baryovm vm harden oracle --ssh-from 203.0.113.7/32   # limit port 22 to this range

baryovm vm threats oracle                    # the last 24 hours
baryovm vm threats oracle --since "7 days ago"
baryovm vm threats oracle -o json            # for a dashboard or an agent
```

The policy is:

- **`PerSourcePenalties` in sshd**, where the daemon has them (OpenSSH 9.8 and up).
  Each failed authentication costs the source real time, and the penalty accumulates
  to an hour. This is in-daemon, with no log parsing and nothing to install.
- **fail2ban**, with four failures in ten minutes earning an hour's ban, doubling for
  repeat offenders up to a week. It bans at the firewall, so repeat traffic stops
  reaching sshd and stops filling the journal, which is where a real login hides.
- **The surface reductions that cost nothing**: GSSAPI and X11 forwarding off, a
  30 second login grace, root login and password authentication off.

Beyond the policy it audits the host. Each finding is either **fixed** or
**reported**, and a report is never turned into a change behind your back.

Fixed (a dry run prints these as `would-apply`):

- `~/.ssh` and its files made private (700 and 600).
- `authorized_keys.*` backups moved to `~/.baryovm-moved-keys/`, with the new path in
  the output. Nothing is deleted.
- Post-quantum key exchange put first in the sshd drop-in: `mlkem768x25519-sha256`,
  `sntrup761x25519-sha512`, then the algorithms sshd already offered. The drop-in sorts
  before the RHEL `50-redhat.conf` that pulls in the crypto policy, and sshd takes the
  first value, so this works on Oracle Linux without editing the policy. If `sshd -T`
  still shows another algorithm first, it reports that the policy needs a subpolicy. If
  OpenSSH has neither hybrid, it reports that and changes nothing.
- nginx `server_tokens off`, written to `conf.d` and reloaded only if `nginx -t` passes.

Reported only (shown as warnings, counted separately, never changed):

- stale entries in `authorized_keys` (count and comments; no key is removed)
- pending security updates, and whether unattended upgrades are on
- no `default_server` on 443
- a newer kernel installed than the one running (`needs-restarting -r` or
  `/var/run/reboot-required`); nothing is rebooted
- port 22 open to every source in firewalld or ufw

The closing `summary` line says how many findings were changed and how many are left
for you. In `-o json` the report has a `reported` count next to `changes`, and a
reported item has `"action": "report"`.

`--ssh-from <cidr,...>` limits port 22 in firewalld (rich rules, the open `ssh` service
removed) or ufw (`allow from`, the open rule deleted). It refuses to run when none of the
ranges contains the address in `$SSH_CLIENT`, so it cannot lock out the session you are
using, and it refuses when that address is unknown. IPv6 ranges are refused up front
(only `::/0` is accepted), because the lockout check cannot match an IPv6 client against a
prefix. An IPv6 client is accepted only with `::/0`. Without `--ssh-from`, a 22 open to everyone is only reported.

What it deliberately does not do: change the SSH port, disable public key
authentication, or remove an `authorized_keys` entry. It validates the sshd configuration
before reloading, reloads rather than restarts, and rolls the file back if sshd
rejects it, so a mistake cannot lock you out of a machine you reach over SSH.

`vm threats` leads with the successful logins, because those are the lines worth
reading. It also flags two things that separate noise from something worse: a login
that succeeded with a password on a host that should be key-only, and failed attempts
against accounts that actually exist.

That second one comes from sshd rather than from guesswork. sshd writes "invalid user"
only when the account does not exist, so an attempt without that token names a real
account, and the report marks it. Expect `root` to appear on any public host, since it
is on every scanner's list; your own login name appearing is the interesting case.

## Patching and pruning a VM

`vm patch` updates the OS packages and proves the stack came back.

```sh
baryovm vm patch web1 --dry-run   # list pending updates and whether a reboot is required now
baryovm vm patch web1             # update; ask before rebooting (on a terminal only)
baryovm vm patch web1 --reboot    # update and reboot without asking if the host needs it
```

It records the kernel, every container with its health, and nginx first. The update runs
as a detached `systemd-run` unit (`dnf update -y`, or `apt-get upgrade` on Debian), so a
dropped session cannot stop it halfway, and it is polled until a deadline. Whether a
reboot is needed comes from the host: `needs-restarting -r`, or `/var/run/reboot-required`.
After a reboot it waits for SSH, retrying a bounded number of times, and requires a new
boot id. Then every container that was running must be running and healthy, nginx must be
active and `nginx -t` must pass. A container that was already unhealthy before is not a
failure. The report gives the package count, old and new kernel, whether it rebooted, and
the containers that did not come back; any of those makes the command exit non-zero and
name them. It needs `sudo -n`.

`vm prune` frees disk space. It plans by default and deletes nothing.

```sh
baryovm vm prune web1                          # list item, size and reason
baryovm vm prune web1 --apply                  # delete the plan; prints freed space and df before/after
baryovm vm prune web1 --floor 20 --apply       # exit 0 with no changes while 20 GB or more is free
baryovm vm prune web1 --work-dir /srv/src --worktree-dir /srv/wt --apply
```

What it plans: Docker build cache, dangling images, anonymous unused volumes, stopped
containers older than `--stopped-days` (default 14), rollback images past the newest
`--keep-images` per repository (default 2), `bin`, `obj` and `TestResults` beside a
`.csproj` or `.fsproj` under `--work-dir`, clean git worktrees under `--worktree-dir`
whose branch has no commits off the remote, and the npm, go-build, pip and playwright
caches plus editor server versions other than the newest. Root-owned `obj/` is removed
with `sudo -n`.

What it never touches: a named volume, an image any running or stopped container uses, a
`bin/` or `obj/` with no project file beside it, a dirty or unreadable worktree (it uses
`git worktree remove` without `--force`), and anything outside the folders you name. A
host without Docker or git skips that section and says so. `--apply` takes a `flock` on
the VM, so a second apply while one runs exits with a message instead of racing. It needs
only `sh`, `find`, `du`, `docker` and `git` on the VM. `-o json` gives the plan and the
result.

## Scanning a deployed site

```sh
baryovm scan --url https://example.com
baryovm scan --url https://example.com --fail-on medium
baryovm scan --url https://example.com --resolver 1.1.1.1 -o json
```

`scan` runs a ZAP baseline against the site, then a small set of passive edge checks, and
reports both in one list, worst first. `--fail-on` (default `high`) and the `-o json` envelope
apply to the whole list. In JSON, edge findings use the same fields as ZAP findings and add
`check` and `detail`. Those two fields are left out of ZAP findings, so existing consumers see
no change.

The edge checks use exactly five DNS lookups (CAA, TXT, MX, `_dmarc` TXT and A on the target's
host) and three HTTP requests: one GET for `/.well-known/security.txt`, and one GET each to the
first A record's bare IP over HTTPS (no SNI, certificate not verified) and plain HTTP. Nothing
else is sent: no zone transfers, port scans or guessing.

| Check | Finding |
| --- | --- |
| CAA | low when missing, informational with the allowed CAs when present |
| SPF | low when missing, when there are several records, or when a domain with no MX does not end in `-all` |
| DMARC | low when missing or the policy is `none`, informational for `quarantine` and `reject` |
| Bare IP | medium when the IP answers 2xx over HTTPS or over HTTP. A loopback, private, link-local or shared address (10.x, 127.x, 169.254.x, 100.64.0.0/10) is never probed: an informational finding says so |
| security.txt | low when it has no `Contact`, no valid `Expires`, or has expired; informational when missing |
| HSTS | informational, the header value as sent (or "not set"); not judged |

Every lookup and request has a 5 second timeout and the edge checks stop after 30 seconds in
total. Checks that did not finish are reported once, as a low "Edge checks timed out" finding that
names them. A refused or dropped bare-IP connection is not a finding.

`--url` must be an http or https URL with a registered hostname. A bare IP, a name with
non-ASCII characters (give the `xn--` form), credentials such as `https://a@b/`, and a host with
`\` or `%` are refused, for the ZAP scan too.

Lookups use the system resolver. `--resolver 1.1.1.1` (or `ip:port`) sends every lookup,
including CAA, to that server instead. It takes an IP address, not a name.

## Stacks: docker compose over SSH (day-to-day deploys)

Your real workloads (barakoCMS, BaryoClub) run as compose projects, so this is
the main deploy path.

```sh
# Register a compose project living on a VM
baryovm stack add barako --vm oracle --path /opt/barakocms

baryovm stack list
baryovm stack ps barako                                   # compose ps
baryovm stack deploy barako --force-recreate --service api,console
baryovm stack deploy barako --pull                        # pull images, then recreate
baryovm stack pull barako
baryovm stack logs barako --service api --tail 100
baryovm stack remove barako                               # forget the registration

# the stack's env file, without ssh
baryovm stack env set barako MAIL_API_KEY                 # no-echo prompt, or: printf %s "$V" | baryovm stack env set ...
baryovm stack env set barako MAIL_API_KEY --recreate      # then compose up -d --force-recreate
baryovm stack env list barako                             # keys only, values masked
baryovm stack env unset barako MAIL_API_KEY
```

`stack env` edits the stack's `--env-file` (default `.env`) under its directory. The value is never an
argument: it comes from a no-echo prompt or one line of piped stdin, travels on the ssh session's stdin
and is written by a small remote script, so it is on no command line and in no output. The file is
created with its directory if missing and kept at mode 600, through sudo when the stack was added with
`--sudo`. KEY must match `^[A-Za-z_][A-Za-z0-9_]*$`. The value is stored as typed on one line, so it
cannot hold a line break. `list` cuts the keys on the VM, so values never cross the connection.

Running `stack add` again for a stack that already exists updates it in place. `--vm` and `--path`
are still required, and any other flag you pass replaces its value, but settings you do not name
stay as they were, including the update policy from `stack set-update`. So adding a database to an
existing stack is:

```sh
baryovm stack add barako --vm oracle --path /opt/barakocms --db-container deploy-postgres-1 --db-name barako
```

`stack deploy` flags: `--service a,b` (limit to services), `--pull`,
`--force-recreate`, `--no-deps`, `--dry-run` (print the compose commands, no connection).

`stack release --dry-run` prints, in the order they would run, the backup script, the rsync
invocation for each `sync` entry (with `--delete` and the trailing slash as it would be sent), the
builds, the `preDeploy` hooks, the compose up, the TLS commands, the `postDeploy` hooks and the
verification checks. Hooks are listed and never run. It opens no connection, takes no lock,
writes no release record and leaves `fleet.json` alone. Things only the VM can answer (whether the
database container exists, `container_name` conflicts, the tar listing, the saved rollback images,
the `--resume` record) are named in the output as not checked. `--dry-run` cannot be combined with
`--clear`. In `-o json` the plan is in `data`: `commands` (flat, in order), `steps`, `transport`,
`rsync`, `hooks`, `verify`, `tls` and `notes`.

### Release: sync source, build, deploy (config-driven)

`stack release` is the full deploy in one command: **back up → sync source → build
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
# On a first release the database container does not exist yet. The backup is then skipped with one
# line. A container that exists and fails to dump still stops the release.
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
| `exclude` | `bin`, `obj`, `node_modules`, `.next`, `.git` | rsync-style excludes, honoured by both transports (see below) |
| `builds` | none | images to build on the VM after syncing |
| `sudo` | `false` | run the release's remote steps as root: the far side of the rsync, the image builds, `preDeploy`, `postDeploy` and the closing `compose up`. A stack registered `--sudo` turns this on for its own release and a manifest cannot turn it back off |
| `noCompose` | `false` | skip `docker compose up` at the end |
| `requireLocal` | none | paths under `localRoot` that must exist before anything runs. A missing one refuses the release before the backup, the sync and `postDeploy`, and names the path. It catches a missing build artifact, not a stale one |
| `postDeploy` | none | commands to run on the VM after syncing and building |
| `verify` | none | commands run on the VM after `postDeploy` that decide whether the release worked. Every one must exit zero |
| `verifyAttempts` | `5` | maximum number of times the whole verify plan may run, not retries after a first run |
| `verifyDelaySeconds` | `3` | wait between attempts |
| `verifyVisual` | none | opens a page in a headless chromium or chrome on the VM after the curl checks. Keys: `url` (http or https, as seen from the VM), `widths` (1 to 10000 px, up to 20), `screenshotDir` (absolute path on the VM; writes `<release>-<width>.png`), `assertNoHorizontalOverflow` (fails when `scrollWidth` exceeds `innerWidth`, naming the width). Needs `screenshotDir` or the assertion. Each browser run is capped at 60 seconds and shares the `verifyAttempts` and `verifyDelaySeconds` bounds. No image comparison. A VM with no browser fails with a message pointing at `baryovm vm bootstrap` |

#### TLS and the nginx vhost

A manifest with a `tls` block gets its nginx vhost and Let's Encrypt certificate written by
`stack release` (after compose up, before `postDeploy`), or on its own with `stack tls`.

```json
"tls": {
  "hosts": ["app.example.com"],
  "canonical": "app.example.com",
  "redirects": ["www.example.com"],
  "email": "you@example.com",
  "proxy": { "port": 5012, "maxBodySize": "32m", "largeHeaders": true }
}
```

| key | what it does |
|---|---|
| `hosts` | names served, all on the certificate. Plain DNS names only; anything else is refused |
| `canonical` | the name that serves content, default the first host. Other names redirect to it |
| `redirects` | extra names that only redirect to `canonical`, also on the certificate |
| `email` | ACME account contact |
| `root` | static document root, default `remoteRoot`. Ignored with `proxy` |
| `proxy.port` | proxy to `127.0.0.1:<port>`; Host, X-Forwarded-For, X-Forwarded-Proto and the upgrade headers are set by nginx |
| `proxy.maxBodySize` | `client_max_body_size`, for example `32m` |
| `proxy.largeHeaders` | larger proxy buffers, for backoffice logins whose cookies overflow the default |

The sequence is safe to rerun: write a port 80 vhost (only when no certificate exists yet), run
`certbot certonly --webroot` with `--keep-until-expiring`, write the real vhost, `nginx -t`, reload.
The vhost is `/etc/nginx/conf.d/<canonical>.conf`, its first line says BaryoVM wrote it, and a file
at that path without the line is refused unless you pass `--adopt`, which copies it to
`<file>.baryovm-bak-<time>` first. If `nginx -t` rejects the new file the old one is put back and
nginx is not reloaded. The steps run with `sudo -n`, so the SSH user needs passwordless sudo, and
certbot must be installed. `/var/www/letsencrypt` is used as the ACME webroot.

```sh
baryovm stack tls app --dry-run     # print both configs and every command, touch nothing
baryovm stack tls app --adopt       # take over a hand-written vhost
```

#### Moving a hand-run container into a stack

A VM you already have is usually running containers started with `docker run`. If your compose file
gives a service the same `container_name`, compose cannot create it while the old container exists.
`stack deploy` and `stack update` check this before they back up, sync, build or pull. `stack
release` checks it after the sync (compose reads the compose file from the VM, and on a first
release the sync is what puts it there) and before any image build or `compose up`. A stack
directory that does not exist yet is not warned about. The check stops with the container's name
and the command that frees it:

```
container name "umbraco-pwa-demo" is held by a container compose does not manage, probably started with docker run: check it is safe to remove, then run `docker rm -f 'umbraco-pwa-demo'` on the VM and try again
```

Without `--adopt` BaryoVM never removes that container. Check first that its data lives in a volume,
not in the container. If the old container used a named volume, declare it `external: true` in the
compose file. Otherwise compose creates a new, empty, project-prefixed volume and the app starts
with no data.

```yaml
volumes:
  umbraco-pwa-demo-data:
    external: true
```

The whole migration:

1. Run `stack release` or `stack deploy`. It stops on the hand-run container.
2. Add `external: true` for every named volume that container uses, as above.
3. Run it again with `--adopt`:

```sh
baryovm stack release app --adopt
baryovm stack deploy app --adopt
```

`--adopt` removes the container for good, with no way back: `docker rm -f` drops its writable layer
and any anonymous volume, and nothing is backed up first. It prints each container it will remove
(name, image, created time) before it does, then carries on. It never passes `-v`, so volumes stay. Before removing anything it reads the
container's mounts with `docker inspect`. If a named volume is not declared `external: true`, it
refuses, removes nothing, and prints the volume name and the YAML above. There is no flag to skip
that check. Anonymous volumes and bind mounts are not checked. A container that belongs to another
compose project is never adopted. On `stack release` the removal happens after the release record
is written. `--adopt` on `stack release` also takes over a hand-written vhost (see above).

#### Verifying a release

A release that reports success without asking the running site anything cannot tell a working
deploy from one that landed and serves the wrong thing. That is not hypothetical: a demo site
served its framework's "welcome, now install me" screen through two releases, a perfectly valid
200 at the exact URL every listing pointed at, and every command in the deploy had succeeded.

So after `postDeploy`, `stack release` runs a verification plan and **fails the release if it does
not pass**:

- If the manifest sets `verify`, those commands run. Point this at a check script the repository
  already has rather than reimplementing one here.
- Otherwise, if the stack has a `healthUrl`, it becomes a `curl --fail` probe.
- If neither is set, the release warns that it verified nothing and still succeeds. An interactive
  release is not an unattended update, so this is a warning rather than a refusal.

A passing release says which plan ran and on which attempt, for example `verified by the manifest
verify list, 2 check(s) passed on attempt 1`. In JSON it is `data.verify`, with `source`
(`manifest`, `healthUrl` or `none`), `checks` and `attempts`.

The probe runs **on the VM**, not locally. A `healthUrl` is usually loopback, which from your
laptop either fails or, worse, reaches something local and passes.

One rule worth carrying into any check script: a "must not contain" assertion only means something
after a real body has arrived. An empty response, a 502 and a DNS failure all satisfy a negative
grep, so assert the status and a minimum size first.

#### A release that stopped part way

Before its first step that changes anything, `stack release` writes a plain JSON record on the VM,
next to the release directory: `.baryovm-release-<stack>.json`. Read it with `cat`. It holds the
commit (`git rev-parse HEAD` of the local source, when there is one), the backup taken, each step
with its status (`started`, `done`, `failed`) and the failed step with its error. A step is
written as `started` before it runs, so a crash in between reads as "this step may have run". The
write happens under a lock directory on the host, so two releases starting together cannot both
find nothing in progress.

A finished release removes the file and writes `.baryovm-release-<stack>.deployed.json` with the
commit. A host with no record behaves as before.

```sh
baryovm stack release app            # refuses when a record exists, and prints what it did and did not touch
baryovm stack release app --resume   # continue from the first step not done, reusing the earlier backup
baryovm stack release app --clear    # drop the record without running anything
```

A record whose `deploy` or `tls` step did not finish cannot be resumed, since compose may be half
applied. The refusal says so, points at `stack rollback` and names the backup it took, for
`stack restore <name> --file <backup> --yes` if the data needs it. `--resume` also refuses when
the local commit differs from the recorded one. In JSON the refusal carries `data.stateFile`,
`steps`, `failedStep`, `failedError`, `backup`, `backupFile`, `resumable` and `hint`. A successful
release reports `data.commit`.

Clearing a record that cannot be resumed also protects `images-before.json`: the images in it are
the last good set, and the next release or update would otherwise record over them with what the
half-applied release left running. The clear reports `data.rollbackProtected`. A release that
finishes, or a `stack rollback` that puts the images back, lifts the protection.

#### Rolling back the images

```sh
baryovm stack rollback app --list    # show the images recorded before the last release or update
baryovm stack rollback app           # retag them and recreate the containers
```

`stack release` (before `compose up`) and `stack update` (before its first recreate) write
`images-before.json` into the stack's backup directory on the VM, so the record outlives the CLI.
Rollback puts those images back and runs `compose up`. It never touches the database: the matching
backups are printed, and `stack restore --yes` stays the explicit word for the data. In JSON the
record is `data.record`, the backups `data.backups`, and `data.restoredDatabase` is always
`false`. Static sites are not covered.

#### Sync transports and excludes

With local `rsync` on your PATH, `sync` entries go through rsync. Without it they go as a tar
stream over the SSH session (Linux hosts), and `baryovm doctor` names which one a release will
use. The stream is written with Go's `archive/tar` straight into `tar -x` on the host, never held
in memory. Once the extract has exited cleanly, files that are on the host and not local are
removed, so the result matches `rsync --delete`. Two differences: there is no delta transfer, and
files arrive owned by the SSH user (root under `sudo`) rather than with your local owner.
Setuid and setgid bits are dropped.

`exclude` follows rsync's matching under both transports:

- A pattern with no `/` matches a name at any depth: `.env`, `*.map`.
- A pattern with a `/` matches the end of the path at any depth: `secrets/.env` also excludes
  `a/secrets/.env`.
- A leading `/` anchors to the transfer root: `/secrets/.env` matches only the top level. With a
  `sync` entry like `"out"` (no trailing slash) rsync's paths start with `out/`, so anchor as
  `/out/secrets/.env`.
- `*` and `?` stop at `/`, `**` crosses it. A leading `**/` also matches zero directories, so
  `**/.env` excludes the top-level `.env` too. A trailing `/` matches directories only.
- Excluded paths are not sent, are not deleted from the host, and a host directory that holds one
  is not deleted either.

The tar transport supports only that subset. A pattern using character classes (`[ab]`), a
backslash, `***`, a leading `//` or a filter prefix (`!`, `+ `, `- `) is refused with an error before anything
touches the host; simplify it or install rsync.

Pruning is confined to the entry's destination directory. It is refused when that directory, or
one between `remoteRoot` and it, is a symlink on the host. Each removal re-checks, on the host
and just before the delete, that the path's parent still resolves inside the release dir. A
window of an instant remains between that check and the delete, which POSIX tools cannot close,
so do not release into a directory that other users can write to. A symlink to a single file
cannot be a `sync` entry under tar.

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

#### When a release refuses to sync the compose directory

The compose directory is the stack's `--path`, and it holds the `.env`. rsync `--delete` removes any
file on the VM that is not in the local source, so a sync that reaches that directory would delete
the `.env`. `stack release` checks for this after the pre-release backup and before the first sync
entry runs. It stops if any entry lands on the compose directory or on a directory above it.

Where an entry lands follows the trailing slash rule above. With `"remoteRoot": "/opt/app"` and a
stack registered with `--path /opt/app/deploy`:

- `"deploy"` lands on `/opt/app/deploy` and is refused.
- `"out/"` lands on `/opt/app`, which contains `deploy`, and is refused.
- `"api"`, `"web"` and `"deploy-assets"` are allowed. A name that only starts with `deploy` is not
  inside it.

The first case fails with:

```
sync entry "deploy" lands on /opt/app/deploy, which contains the compose directory /opt/app/deploy: rsync --delete would remove its .env. Sync the application directories individually instead of the root that holds them
```

Under `-o json` the same text is the envelope's `error`, with `"action": "stack release"`.

To fix it, list the directories the release needs one by one, leaving the compose directory out:
`"sync": ["api", "web"]` rather than `"sync": ["out/"]`. A `remoteRoot` that does not contain the
compose directory passes too.

What the check does not do:

- It skips a manifest with `"noCompose": true`. A static site's stack directory is its webroot, so
  it is the same path as `remoteRoot` on purpose and there is no `.env` to protect.
- It compares the paths as written, after trimming spaces and one trailing slash. It does not
  resolve symlinks or `..`, and it does not look at the VM, so a destination that reaches the
  compose directory through a symlink is not caught.
- It is the same with or without `--sudo`.

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
    "restorecon -R /var/www/baryo-web || true",
    "nginx -t",
    "systemctl reload nginx"
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

**`sudo` elevates the remote side, not the local one.** The rsync becomes
`--rsync-path="sudo -n rsync"`, because it is the copy of rsync running on the VM that needs to
write into a root-owned path, and the image builds, both hook lists and the closing `compose up`
run as root on the VM for the same reason. The account you SSH as needs passwordless sudo for it.
A stack registered with `stack add --sudo` gets all of this for its release even if the manifest
says nothing, and the fold is one way: `"sudo": false` in the manifest cannot turn it off again.

The `-n` matters. Without it, a host that does want a password writes the prompt into rsync's data
channel, which corrupts the protocol stream, so the transfer hangs or dies with something opaque
instead of telling you sudo needs a password. With `-n` it fails at once and says so.

Three things change when you turn it on.

The receiving rsync is root, so `-a` starts honouring `-o` and `-g`, which it cannot do as an
ordinary user: files arrive with the local source's ownership rather than owned by the account you
SSH as.

Hooks run under `sudo -n "${SHELL:-/bin/sh}" -c`, so sudo's `env_reset` and `secure_path` apply:
`$PATH` is root's `secure_path` and `$HOME` is `/root`, so a hook calling a per-user tool (a dotnet
under your home directory, nvm's node) needs its absolute path. The shell is the one your login
shell would have been, not `sh`, so a hook written in bash keeps working.

And the SSH user needs sudo rights to run a *shell*, not only the individual programs. A sudoers
line granting `NOPASSWD: /usr/bin/systemctl` on its own refuses the whole hook with "a password is
required", which reads like a password problem and is a permissions one. If that is your host, grant
the shell or drop `--sudo` and keep the elevation inside each command.

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
add `--sudo`, and that stack's docker and compose commands, its backup's dump and `.env` copy, and
its release's builds, hooks and remote rsync all run through `sudo -n`. Without it compose fails
with "permission denied" reading the file, even though Docker itself is reachable. Loosening the
file to suit the tool would be the wrong trade. The backup script's own file handling (creating the
backup directory, writing the dump, pruning) stays as the SSH user, so `--backup-dir` has to be a
directory that user can write.

```sh
baryovm stack add baryodev --vm oracle --path /opt/baryo-cms --sudo \
  --db-container baryo-postgres --db-name barako_cms --env-file .env
```

Backup config flags on `stack add`: `--db-container`, `--db-name`, `--db-user`
(default postgres), `--env-file` (relative to the project dir), `--backup-dir`
(default `~/<name>-backups`), `--keep` (default 14). Restore refuses without `--yes`.

`stack backup --dry-run` prints the backup script and `stack restore --dry-run` prints the restore
script, both without connecting. `restore --dry-run` needs no `--yes` (nothing is replaced) and says
which file it would use: the one named by `--file`, or "newest", which the VM picks when the real
restore runs.

Example: recreating the barakoCMS API and the barakoBrew console together is one command:

```sh
baryovm stack deploy barako --force-recreate --service api,console
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

`--auto` refuses four things, all for the same reason: an unattended update has to be able to tell a
healthy start from a crash loop, and to go back when it cannot. It refuses a stack without
`autoUpdate`, a stack without a `healthUrl`, the flag combination `--auto --no-backup`, and a stack
with no database backup configured at all. Set `autoUpdate` on the tiers you are willing to have
change unattended: playground, not production.

A stack that genuinely has no database says so once, and keeps updating:

```sh
baryovm stack set-update barako --no-database
```

That is a recorded decision rather than a silent gap. The refusal exists because a stack with no
`dbContainer` and no `dbName` used to skip the backup entirely and run unattended anyway, which is
what the other three refusals exist to prevent. A `--dry-run` is exempt from both backup refusals,
since it recreates nothing and so has nothing to go back from.

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
baryovm deploy --vm oracle --image app:latest --name app -e KEY=value --dry-run
```

`--dry-run` prints the docker commands to stderr and in the result's `data`, and opens no
connection. Env values are masked as `***` in that output.

## Provision + deploy in one (untested, billable)

```sh
# Preview only, no AWS calls:
baryovm vm provision web1 --provider lightsail --key ~/.ssh/id_ed25519 --dry-run

# The one button: provision (if new) -> install Docker -> deploy
baryovm up web1 --provider lightsail --key ~/.ssh/id_ed25519 \
  --image nginx:alpine --container site -p 80:80

# On an already-registered VM, up just bootstraps + deploys:
baryovm up oracle --image app:latest --container app -p 80:8080

# Limit who can reach the ports (Lightsail firewall):
baryovm vm provision web1 --key ~/.ssh/id_ed25519 \
  --ssh-from 203.0.113.7/32 --http-from cloudflare --https-from cloudflare
```

`--ssh-from`, `--http-from` and `--https-from` take CIDRs (comma separated or
repeated). A port with no flag stays open to `0.0.0.0/0`, as before. `cloudflare`
stands for Cloudflare's published IPv4 and IPv6 ranges, fetched from
cloudflare.com/ips-v4 and ips-v6 when the command runs. If the fetch fails or
returns an empty list, the command stops before creating anything, and it never
falls back to an empty or open list. The dry run prints how many ranges each
port gets. The same flags work on `up`.

## Guarding commits, pushes and PR text

`baryovm git` and `baryovm gh` run the real tool after checking for keys. Agents
and scripts use these instead of the raw commands.

```sh
baryovm git commit -m "add login"      # scans the staged diff, and the message
baryovm git commit -am "fix"           # also scans the working diff
baryovm git push -u origin main        # checks the host, scans unpushed commits
baryovm gh pr create --title t --body-file body.md
baryovm gh release create v1 --notes-file notes.md
baryovm guard install-hooks            # pre-commit and pre-push hooks, same checks
```

Checked:

- `git commit`: the staged diff, the working diff with `-a` or paths, the
  message from `-m`, and the message file from `-F` or `--file`.
- `git push`: the host of every push URL of the remote, then every commit not on
  the remote. A delete (`--delete`, `-d`, `:branch`) skips the commit scan but
  still gets the host check.
- `gh pr`, `gh issue` and `gh release`, for `create`, `edit`, `comment` and
  `review` where gh has them: `--title`, `--body`, `-b`, `--body-file`, `-F`,
  `--notes`, `-n` and `--notes-file`. A repo flag such as `-R o/r` before the
  verb is fine.

Every other git and gh command passes straight through with the tool's own
output and exit code. Flags after `git` or `gh` belong to that tool, so these
two take none of their own; set `BARYOVM_OUTPUT=json` for a JSON refusal.

The scanner looks for Google `AIza` keys, GitHub tokens, AWS access keys, Slack
tokens, Stripe live keys, npm tokens, Anthropic (`sk-ant-`) and OpenAI (`sk-`,
`sk-proj-`) keys, PGP private key blocks and other private key blocks. A refusal
exits 1, names `file:line (rule)`, never prints the value, and does not run git
or gh. Put `allow-secret` in a comment on a line to exempt a test fixture. A body
or message from stdin (`-F -`) is refused because it cannot be checked; write it
to a file. `git push` scans only commits not already on the remote.

To limit where a repository may push, list the allowed hosts. With none set,
every host is allowed. A push to any other host is refused before git contacts
it. If the setting cannot be read (a broken git config, as opposed to unset),
the command is refused rather than allowed.

```sh
git config --add baryovm.allowedHosts github.com
export BARYOVM_ALLOWED_HOSTS=github.com,dev.azure.com   # alternative; "*.corp.example" works
```

`guard install-hooks` writes `pre-commit`, `commit-msg` and `pre-push` hooks into the current
repository so commits made without the wrapper are checked too. It leaves a hook
that is not BaryoVM's alone unless you pass `--force`, which keeps it as
`<name>.baryovm-old`. The hooks fail when `baryovm` is not on PATH.

## Prerequisites

```sh
baryovm doctor          # report the local tools a release runs, plus cloud creds
baryovm doctor --fix    # install the ones BaryoVM knows how to install
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
