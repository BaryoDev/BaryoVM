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
- Every command supports `-o json` for machine consumers (MAUI/MCP/agents). One JSON document per run, success or failure, with the
  command name in `action`, even for a bad flag. The shape is in [docs/JSON.md](docs/JSON.md).

## What is built but untested

- Provisioning a new Lightsail VM (`vm provision`, `up`). It is billable, so it
  prints a plan and creates nothing until you pass `--yes`. It is tested with fakes only,
  never against a real account. OCI is stubbed.

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

## Checking a VM's health

`vm health` asks a machine the questions an owner should ask monthly. It is read only: it changes
nothing on the host.

```sh
baryovm vm health oracle
baryovm vm health --all -o json
```

Each line is a check with a verdict of `ok`, `warn`, `fail`, `unknown` or `skip`:

| Check | Looks at | Verdict |
| --- | --- | --- |
| `disk-root` | `df` on `/` | warn at 80% used, fail at 90% |
| `disk-docker` | `df` on the docker root, plus image and volume sizes from `docker system df` | same thresholds |
| `updates` | pending updates, security and other (`dnf check-update` and `--security`, or `apt list --upgradable`) | any security update fails, more than 25 others warn |
| `reboot` | `needs-restarting -r`, or `/var/run/reboot-required`; uptime in the detail | a required reboot warns |
| `containers` | restarting, dead, unhealthy, or exited with a non-zero code or under `restart: always`; running ones that restarted more than 5 times warn | any problem fails |
| `unattended-updates` | `APT::Periodic::Unattended-Upgrade`, or a dnf-automatic timer that applies updates | off warns |

The limits are flags, shown here with their defaults:

```sh
baryovm vm health oracle --disk-warn 80 --disk-fail 90 --max-updates 25 --max-restarts 5
```

`--disk-warn` and `--disk-fail` are percent used, both 1 to 100, with warn below fail.
`--max-updates` is how many non-security updates may be pending, and `--max-restarts` is how many
restarts a running container may have; more than that warns. `--max-updates 0` warns on any
pending update. A negative count is refused. A bad value is refused before any connection is opened.
The limits used are echoed in `thresholds` in the JSON report.

It needs `sudo -n` for docker and the package manager. A check it cannot answer (no docker, no
sudo, no supported package manager) is `unknown` or `skip` with the reason, and the rest still run.
`apt list` reads the host's cached index, which this command does not refresh. The command exits
non-zero when any check is a `fail`. With `--all`, an unreachable VM is a failed report and the
run goes on to the next one; a Windows VM is skipped.

In `-o json`, a single VM puts one report in `data` (`vm`, `verdict`, `packageManager`, `checks`),
and `--all` puts a list of reports there. Each check has `name`, `verdict`, `detail`, optional
`items` (the containers at fault) and `metrics` (numbers such as `usedPercent`, `security`,
`other`, `uptimeDays`).

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

### Onboarding a VM that is already running things

`stack discover` lists the compose projects running on a VM, so you do not start with raw ssh and
`docker ps`:

```sh
baryovm stack discover oracle
baryovm stack discover oracle --sudo                  # docker needs sudo -n for the SSH user
baryovm stack discover oracle --adopt                 # register them, asking about each on a terminal
baryovm stack discover oracle --adopt --yes -o json   # no prompts: required without a terminal
```

It runs `docker compose ls --format json`, then `docker compose config` for each project to count
services and look for a database. For each project it prints the directory, the service count, the
postgres container and the `stack add` command that would register it, every value quoted for a
shell. Without `--adopt` it changes nothing, locally or on the VM.

The postgres container is a guess: the first service whose image name contains `postgres`. It can be
a replica or the wrong database, so check it before backups rely on it. A project that uses several
compose files is listed but not adopted, since `stack add` takes one file. A project already
registered (same VM and directory) is skipped and reported as `already-registered`, and so is one
whose name another stack already uses. Under `-o json` the data is a list of projects with `name`,
`dir`, `services`, `postgresGuess`, `addCommand`, `state` and `note`. `--adopt` needs `--yes` when
there is no terminal or the output is JSON.

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

A build `args` value is a literal or a reference (`env:NAME`, `file:PATH`; `cmd:` is not accepted in a
manifest, since a committed file must not run a program). Either way the value is sent on stdin and
exported in the build's own shell, and the command carries only `--build-arg NAME`, so it is not in
`ps` on the VM. A build arg is still baked into image history if the Dockerfile uses it in a `RUN`.
For a value that must stay out of the image, add `"secrets": { "npmrc": "env:NPM_TOKEN" }` to the
build and read it with `RUN --mount=type=secret,id=npmrc` (BuildKit). Existing manifests keep working.

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
- Otherwise, if the stack has a `verify` spec (below), its checks run.
- Otherwise, if the stack has a `healthUrl`, it becomes a one-check spec and a `curl --fail` probe.
- If none is set, the release warns that it verified nothing and still succeeds. An interactive
  release is not an unattended update, so this is a warning rather than a refusal.

A passing release says which plan ran and on which attempt, for example `verified by the manifest
verify list, 2 check(s) passed on attempt 1`. In JSON it is `data.verify`, with `source`
(`manifest`, `verify`, `healthUrl` or `none`), `checks` and `attempts`.

The probe runs **on the VM**, not locally. A `healthUrl` is usually loopback, which from your
laptop either fails or, worse, reaches something local and passes.

##### The stack verify spec

`healthUrl` only proves a page answers. A stack can store a fuller spec instead, in `fleet.json`
under `verify`, and `stack release`, `stack update` and `stack rollback` all read the same one:

```json
{
  "baseUrl": "http://127.0.0.1:8080",
  "checks": [
    {"path": "/", "status": 200, "minBytes": 200,
     "contains": ["baryodev-pwa.js"], "absent": ["Welcome to your Umbraco installation"]},
    {"path": "/health", "jsonMatch": {"status": "Healthy"}},
    {"command": "sh scripts/check.sh"}
  ],
  "attempts": 20, "delaySeconds": 3, "timeoutSeconds": 300
}
```

Set it with `baryovm stack set-verify <name> --file spec.json`, or remove it with `--clear`. The
file is checked before it is saved.

- A check is a URL check (`path`) or a `command`, never both. A command runs under `timeout` (the spec `timeoutSeconds`, default 300 seconds) in the stack
  directory and passes on exit 0.
- A URL check fetches `path` on the origin of `baseUrl` (or of `healthUrl` when there is no
  `baseUrl`). There is no full-URL field, a path must start with a single `/`, and redirects are
  not followed, so nothing in a spec can send the probe to another host.
- Assertions run in order: `status` (default 200), `minBytes`, `contains`, `jsonMatch` (dotted
  paths), then `absent`. `absent` is judged only after the earlier ones pass, so a 502 or an empty
  body is reported as what it is and never as "the placeholder text is not there".
- A key the spec does not know is refused, so a mistyped assertion cannot quietly check nothing.
- Attempts are capped at 100, the delay at 1 minute, and the total time at `timeoutSeconds`
  (at most 30 minutes). Without `timeoutSeconds` the time is attempts x (delay + 10s), at least 5
  minutes, so `--health-attempts 60 --health-delay 10s` gets about 20. Running out of time fails
  with a timeout message, never a pass.
- A stack with only a `healthUrl` verifies exactly as before. The manifest `verify` list still wins
  for a release.

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

### Project files: one fleet definition for a team and for CI

`fleet.json` is one file on one machine. It holds an absolute key path and an absolute path to each
release manifest, so a second person, or a CI job, cannot use it. A project file is the shareable
half: a committed, secret-free `baryovm.project.json` that `project release` reads on its own.

It uses the same field names as `fleet.json` (`vms`, `keyPath`, `stacks`, `vm`, `dir`, `releaseFile`,
`healthUrl`, and the rest), parsed into the same structs, plus `project` and `order`:

```json
{
  "project": "rckoronadal",
  "vms": [
    { "name": "client-vm", "host": "203.0.113.10", "user": "deploy",
      "keyPath": "env:DEPLOY_KEY_PATH", "hostKey": "ssh-ed25519 AAAA..." }
  ],
  "stacks": [
    { "name": "api",     "vm": "client-vm", "dir": "/opt/barako/api",     "releaseFile": "api.release.json" },
    { "name": "console", "vm": "client-vm", "dir": "/opt/barako/console", "releaseFile": "console.release.json" }
  ],
  "order": ["api", "console"]
}
```

```sh
baryovm project release                          # reads ./baryovm.project.json
baryovm project release --file deploy/baryovm.project.json --no-backup
```

What the file may and may not hold:

- `releaseFile` is relative to the directory holding the project file, so the file means the same on
  every checkout. An absolute path or `~` is refused. (`envFile` is unchanged: it is the file on the
  VM, relative to `dir`.)
- `keyPath` is a reference: `env:NAME` (the variable holds the path to the key, which is how a CI
  runner passes a key it wrote from a secret) or a path. A relative path is read from the project
  file's directory. A literal private key, token or password in any field, including fields this
  version does not know, is refused with the field named, before anything connects. The error never
  repeats the value.
- `hostKey` is the VM's public host key (`ssh-keyscan -t ed25519 <host>`, then check the
  fingerprint). It is public, so it is committed.
- `order` lists the stacks to release. Leave it out and every stack is released in the order written.

`project release` stops at the first failed release or verify and leaves later stacks untouched. The
`-o json` report lists every stack as `released`, `failed` or `skipped`, so a partial release reads as
partial. Each stack goes through the same code as `stack release` (backup first, sync, build,
compose up, verify), so a safeguard added to one is in the other.

It never trusts a host on first use. Each VM needs a pinned `hostKey`, or the run needs
`--strict-host-keys` (or `BARYOVM_STRICT_HOST_KEYS=1`) with the key already in known_hosts. A pinned
key is held for that run only and never written to disk, so a committed file cannot change what the
machine trusts. If `$BARYOVM_HOME/known_hosts` or `~/.ssh/known_hosts` already holds a different key
for the host, the release stops, as any changed key does. A fresh runner has no recorded keys, so the
pinned key is the only one it will accept.

The project file is not merged with `fleet.json`. `project release` does not read it, write it, or
let it override the file, so a person and a CI job release exactly what the file says. Stacks you
register by hand with `stack add` are not seen by `project release`, and the project's are not seen
by `stack release`.

#### Moving an existing fleet to a project file

```sh
baryovm project export --out deploy/baryovm.project.json --name rckoronadal \
  --stack api --stack console
```

`--stack` picks the stacks and sets their order (default: all of them). Export turns manifest paths
under the output directory into relative ones, replaces the key path with `env:DEPLOY_KEY_PATH`
(`--key-env` names another variable; with several VMs each gets `DEPLOY_KEY_PATH_<NAME>`), and pins a
host key found in known_hosts. It reads the result back through the same parser `project release`
uses, so a file it writes is a file release accepts. It refuses, naming the field, a manifest outside
the output directory (move it, or write the file higher up) and a stack with no manifest. A VM with
no known host key is exported without one and a warning: add `hostKey` before releasing from CI.
Export will not replace an existing file without `--force`.

In CI, with only the project file, the key as a secret and the binary:

```yaml
- run: echo "$DEPLOY_KEY" > "$RUNNER_TEMP/deploy_key" && chmod 600 "$RUNNER_TEMP/deploy_key"
  env: { DEPLOY_KEY: "${{ secrets.DEPLOY_KEY }}" }
- run: baryovm project release
  env: { DEPLOY_KEY_PATH: "${{ runner.temp }}/deploy_key" }
```

This is how two people share a fleet: commit the project file, and each person (or runner) supplies
only their own key path.

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

### Provisioning a static site (`site add`, `site deploy`)

`site add` sets a static site up on a VM you already own, end to end. DNS is not part of it: the
name must resolve to the VM before the certificate can be issued.

```sh
baryovm site add example.com --vm web1 --root /var/www/example.com --email ops@example.com --dry-run
baryovm site add example.com --vm web1 --root /var/www/example.com --email ops@example.com
npm run build                                   # produces out/
baryovm site deploy example.com ./out
```

`site add` creates the webroot and the ACME challenge directory (`/var/www/letsencrypt`), writes
`/etc/nginx/conf.d/<domain>.conf` from a template, gets the certificate with certbot in webroot
mode, runs `nginx -t`, and reloads. The server block redirects HTTP to HTTPS, serves a static export
with `try_files $uri $uri.html $uri/ =404`, gives `/_next/static/` and `/assets/` a year-long
immutable cache, and sends `Cache-Control: no-cache` for everything else, HTML included. gzip is
switched on inside those static locations only (JS, CSS, SVG, wasm, JSON), never in the `http`
block, so a proxied location on the same host is not compressed. Where `selinuxenabled` says SELinux
is on, the webroot is relabelled with `restorecon -R`. It is detected, never assumed.

The order is fixed by the certificate: an HTTP-only block first so certbot's challenge is served,
then the full block. Any failing step puts the previous server block back (or removes the new file),
reloads nginx if it had already been reloaded with the new one, and names the step in the error and
in `data.failedStep`. A site that already exists, in the local inventory or as a file on the VM, is
refused unless you pass `--force`. `--email` sets the ACME contact; without it certbot registers
with none.

Every command that needs root runs as `sudo -n`, so the SSH user needs passwordless sudo, including for a
shell. Domain and root are checked before any command is built: the domain is lowercase letters,
digits, hyphens and dots, and the root is an absolute path with no `..` that is not `/` or a system
directory. Like `stack tls`, `site add` needs no `--yes`: it acts on a VM you own and is undone by
removing one file, which is not the billable, hard-to-reverse case `vm provision` guards.

The site is recorded in `fleet.json` (`sites`), the way a stack is. `site deploy` reads the webroot
from there, so it only knows sites added from this machine. It syncs the contents of `<dir>` with
the same rsync as `stack release` (host key check, `sudo -n rsync` on the far side, `--delete`),
then relabels, runs `nginx -t` and reloads. An empty or missing directory is refused, because
syncing it would empty the webroot. Both commands take `--dry-run` and support `-o json`.

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
baryovm stack restore baryoclub --drill                  # prove the newest dump restores
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

### Backup strategies

A stack's backup is a list of strategies, each declaring how it is dumped and how it is restored.
`stack add --db-container ... --db-name ...` writes a list with one `postgres-container` strategy.
A `fleet.json` written before this (with `dbContainer`, `dbName`, `dbUser`, `noDatabase`) still loads:
those fields read as the first strategy, or as an empty list for `noDatabase`, and run the same
scripts as before. For anything else, store a JSON array with `baryovm stack set-backup <name> --file
backup.json` (`--clear` removes it). Unknown keys and unknown kinds are refused when it is stored.

```json
[
  { "kind": "postgres-container", "container": "db", "database": "app" },
  { "kind": "postgres-remote", "host": "mydb.postgres.database.azure.com", "database": "app",
    "user": "admin", "envFile": ".env", "passwordVar": "DB_PASSWORD" },
  { "kind": "command", "dump": "redis-cli --rdb -", "restore": "cat > /data/dump.rdb" },
  { "kind": "files", "paths": ["/srv/app/uploads"] }
]
```

- `postgres-container`: `container`, `database`, `user` (default postgres). `pg_dump` inside the container.
- `postgres-remote`: a database that is not on the VM, such as RDS, Azure Database for PostgreSQL or
  Cloud SQL. `host`, `database`, `port` (5432), `user` (postgres), `image` (`postgres:17`, pin it to
  the server's major version). `pg_dump` runs in a throwaway client container on the VM. The password
  comes from the stack's env file on the VM (`envFile`, relative to the project dir, `passwordVar`
  names the key, default `PGPASSWORD`) or from a libpq password file on the VM (`passFile`, absolute).
  It is handed to the container with `--env-file` or a read-only mount, never on a command line. The
  env file must be plain `KEY=value` lines; add `PGSSLMODE=require` there if the provider needs it.
  Restore uses `pg_restore --clean --if-exists --no-owner`.
- `command`: `dump` writes the backup to stdout, `restore` reads it from stdin. Run through
  `sudo -n` as a whole line when the stack has `--sudo`, like release hooks.
- `files`: `paths`, absolute. A tar, restored under `/`.

`restore` brings back the whole set that shares the timestamp of the file named by `--file` (the
newest set by default) and checks every file exists before it replaces anything. A release and `stack
backup` run every strategy under one timestamp and stop at the first failure. On a first release a
`postgres-container` strategy whose container is not there yet is skipped, as before. The
`--drill` needs a `postgres-container` strategy.

`"backup": []` says the stack has nothing to back up. `stack update --auto` accepts that (it replaces
`noDatabase`, which still works) and refuses a stack that declared nothing at all. Not built yet:
`provider-snapshot` and `volume` kinds, and where backups are kept (#39).

`stack backup --dry-run` prints the backup script and `stack restore --dry-run` prints the restore
script, both without connecting. `restore --dry-run` needs no `--yes` (nothing is replaced) and says
which file it would use: the one named by `--file`, or "newest", which the VM picks when the real
restore runs.

`stack restore <name> --drill` proves a backup restores. It starts a throwaway postgres container on
the VM (same image as the stack's database container, no network, no mounts), `pg_restore`s the
newest dump (or `--file`) into it with `--no-owner --no-privileges`, checks that the restore had no
errors and that the table count is above zero, then removes the container. The container is removed
on failure too (an EXIT trap on the VM, and a second removal from the client). The live database and
its container are only read for their image name, so `--drill` needs no `--yes` and rejects one. It
emits a result on both paths; in `-o json` success `data` holds `file`, `tables`, `container`,
`removed` and `liveTouched` (always false). `--drill --dry-run` prints the script without connecting.

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
baryovm stack set-verify barako --file verify.json   # store a verify spec, or --clear
baryovm stack set-backup barako --file backup.json   # store backup strategies, or --clear

baryovm stack update barako --dry-run   # report what is pending, change nothing
baryovm stack update barako             # apply, verify, roll back if it does not come up
baryovm stack update barako --auto      # what a scheduler runs
```

What it does: pull, compare each container against what its reference now points at, and stop if
nothing is stale. An unchanged stack is never recreated, so a nightly job is not a nightly restart.
When something is stale it backs up the database, recreates only the affected services, and polls the
health URL (or the stack verify spec) from the VM. If health does not come back, it points the references at the images that were
running before and brings those back up, then checks again and says which of the two states it ended in.

`--auto` refuses four things, all for the same reason: an unattended update has to be able to tell a
healthy start from a crash loop, and to go back when it cannot. It refuses a stack without
`autoUpdate`, a stack with neither a `healthUrl` nor a `verify` spec, the flag combination `--auto --no-backup`, and a stack
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

#### Unattended updates

A schedule that depends on a laptop being open is not maintenance. Run it from CI: a scheduled
GitHub Actions workflow has an always-on runner, and the agentless model needs nothing on the VM.
Copy [docs/examples/scheduled-update.yml](docs/examples/scheduled-update.yml) into
`.github/workflows/`. It installs baryovm, pins the VM's host key, and runs
`stack update <name> --auto --notify "$NOTIFY_URL" -o json` on a cron.

A failed unattended run has to reach a person. `--notify <url>` (or the environment variable
`BARYOVM_NOTIFY_URL`, which keeps a token out of the process list) posts the JSON result envelope to a
webhook when an `--auto` run fails or rolls back. `--notify-on always` posts every run, `failure` is
the default. Rules:

- https only. Plain http is accepted for `localhost`, `127.0.0.1` and `::1`, for tests.
- 10 second timeout, one retry at most (on a network error or a 5xx), redirects are not followed, and
  the body is capped at 64 KiB (an oversized envelope loses its `data`, not its verdict).
- `--notify` needs `--auto`. The environment variable is ignored without it.
- The URL usually holds a token, so it is never printed. Output shows scheme and host only.
- If the notification fails, the exit code is non-zero and the envelope carries `data.notify` with
  `ok: false` and the reason. It never replaces the update's own result or error.

The posted body is the same envelope `-o json` prints, so it carries whatever stable error code the
envelope has. The workflow file was written to match the commands above but has not been run on a live
runner.

Verified against a stack whose image was swapped for one that exits on start: the update was applied,
the health check failed, the previous image was restored and the site was serving again, and
separately, that a genuine new image is kept when it comes up healthy.

## Single-container deploy

```sh
baryovm deploy --vm oracle --image nginx:alpine --name site -p 80:80
baryovm deploy --vm oracle --image app:latest --name app \
  -p 127.0.0.1:8080:8080 -e KEY=value --pull
baryovm deploy --vm oracle --image app:latest --name app -e KEY=value --dry-run

# secrets by reference: resolved on this machine, never stored
baryovm deploy --vm oracle --image app:latest --name app \
  --env-ref API_KEY=env:APP_API_KEY \
  --env-ref DB_PASSWORD=file:~/.secrets/db \
  --env-ref GH_TOKEN="cmd:gh auth token"
```

Secret references (`--env-ref KEY=<reference>`, also on `up`) come in three kinds and nothing else:
`env:NAME` (a local environment variable), `file:PATH` (a local file, trailing newline trimmed) and
`cmd:<command>` (the stdout of a signed-in CLI such as `op read op://vault/item/field` or
`gh auth token`). A `cmd:` reference is split into arguments and run directly, with no shell, and a
30 second timeout; its stderr is never shown. A reference that does not resolve stops the command
before anything is provisioned or contacted, and the error names the reference, never a value.

Whether the value comes from `-e` or from a reference, it does not go on the remote command line.
It is sent over the SSH session's stdin into a private file the VM creates with `mktemp` (mode 0600),
`docker run --env-file` reads it, and the file is removed when the command ends, however it ends.
Nothing secret shows in `ps` on the VM. A value cannot contain a line break. The container's
environment is still visible to `docker inspect` on the VM, as it always was.

`--dry-run` prints the docker commands to stderr and in the result's `data`, and opens no
connection. Env values are masked as `***` in that output, and references are checked for syntax but
never resolved, so a dry run reads no secret.

## Provision + deploy in one (untested, billable)

```sh
# Plan only (the default): shows what would be created and the cost, creates nothing:
baryovm vm provision web1 --provider lightsail --key ~/.ssh/id_ed25519

# Really create it (billable):
baryovm vm provision web1 --provider lightsail --key ~/.ssh/id_ed25519 --yes

# The one button: provision (if new) -> install Docker -> deploy. Without --yes
# it prints the plan and stops.
baryovm up web1 --provider lightsail --key ~/.ssh/id_ed25519 --yes \
  --image nginx:alpine --container site -p 80:80

# On an already-registered VM, up just bootstraps + deploys:
baryovm up oracle --image app:latest --container app -p 80:8080

# Limit who can reach the ports (Lightsail firewall):
baryovm vm provision web1 --key ~/.ssh/id_ed25519 \
  --ssh-from 203.0.113.7/32 --http-from cloudflare --https-from cloudflare \
  --host example.org --yes
```

`--yes` is required to create anything. If `--http-from` or `--https-from` limits
a web port, `--yes` also needs the hostname check to pass: name every hostname the
VM will serve with `--host` (repeatable). Each must resolve, and only to addresses
inside the allowed ranges, because a name that reaches the VM directly would be cut
off by the firewall. A limit with no `--host`, an unresolvable name or a direct
address refuses the create and names the host. With both web ports open to everyone
the check is not needed. `--dry-run` is still accepted and wins over `--yes`.

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

Option parsing follows the tools: git long options may be abbreviated (an
ambiguous abbreviation is refused), grouped short flags are read letter by
letter, a gh flag group with a letter the subcommand does not have is refused,
and a relative `-F` path is refused when any git global flag other than `-C` is
given, or `GIT_DIR`, `GIT_WORK_TREE`, `GIT_COMMON_DIR` or any `GIT_CONFIG*`
variable is set, since each can move where git reads the file from; pass an
absolute path. For `gh pr`, `gh issue` and `gh release` the only flags allowed before the
command are `-R <repo>`, `--repo <repo>` and `--repo=<repo>`.

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

#### Rules beyond keys

`baryovm git` and `baryovm gh` also apply the rules below. Every rule is on by default and each can be
switched off for one repository: `git config --add baryovm.rules.off <rule>` (or
`BARYOVM_RULES_OFF=rule,rule`). A refusal names the rule and how to pass it. Keys are read with one
`git config --get-regexp` call, so a broken config refuses instead of switching rules off.

Fail-closed details: with protected-branches on, a push whose destination cannot be told (a glob or empty
refspec, a detached HEAD, `remote.<name>.push`, `push.default=matching`) is refused; name the branch to
pass it. The gate needs a recorded pass for every commit pushed. Every `!` git alias is refused unless
listed with `git config --add baryovm.allowShellAliases <alias>`.

| Rule | What it does | Settings |
| --- | --- | --- |
| `protected-branches` | Refuses a push, force push or delete of the default branch (from `refs/remotes/<remote>/HEAD`, else `main` and `master`) and of listed branches. `--force-with-lease` is allowed only on branches the policy lists. `--all` and `--mirror` are refused. | `baryovm.protectedBranches`, `baryovm.forceLeaseBranches` |
| `destructive` | `gh repo delete`, `gh release delete`, `git push --delete` of a tag, `git reset --hard` with uncommitted work and `git clean -fdx` print what goes and need the target name typed back. With no terminal, or under `BARYOVM_OUTPUT=json`, there is no prompt: pass `--confirm <name>` (consumed by baryovm, never sent to git or gh). | none |
| `files` | Refuses `.env` and `.env.*` (not `.env.example`), `*.pem`, `*.p12`, `*.jks`, `*.keystore`, JSON with `"type": "service_account"` and files over the size limit. Checked on the staged files at commit and on the pushed range at push, and by the pre-commit hook. | `baryovm.maxFileSize` (default 10M), `baryovm.allowFiles` (path or name) |
| `text` | Refuses AI attribution (`Co-Authored-By` naming an AI tool, `Generated with ...`) in commit messages, `--trailer`, and the text `gh pr`, `gh issue` and `gh release` publish; the commit-msg hook too. House style (no em dash, subject up to 72 characters, no trailing period) only when the repository opts in. | `baryovm.houseStyle true` |
| `visibility` | `gh repo create` needs `--public`, `--private` or `--internal`, and the one the policy names. Nothing is defaulted. | `baryovm.visibility` |
| `gate` | When a gate script is named, `git push` runs it first, or skips it when it already passed on the same HEAD. A pass is recorded in `baryovm.gatepass`. | `baryovm.gate` |
| `audit` | Each guarded command, its target and the result go to the activity log (`target` field). Targets lose URL credentials and key-like values; error text is a fixed string. | none |

Aliases cannot get round the guard: `git ci` with `alias.ci=commit` is checked as a commit, a `!` shell
alias that runs `git commit` or `git push` is refused, an alias that cannot be resolved is refused,
and `-c alias.*=` and `--config-env` before the subcommand are refused. `git commit
--pathspec-from-file` is refused (pass the paths as arguments), since the guard cannot see that file.

### Publishing packages

`baryovm publish npm` and `baryovm publish nuget` check a package before it leaves the machine, then
run `npm publish` or `dotnet nuget push` with your arguments unchanged.

```sh
baryovm publish npm --access public     # npm publish --access public
baryovm publish nuget bin/App.1.0.0.nupkg -s https://api.nuget.org/v3/index.json -k "$KEY"
```

- npm: `npm pack --dry-run --json` lists what would ship (only its JSON is read) and every file is
  scanned with the same key rules as `git`. The registry comes from `publishConfig.registry` first,
  then from npm itself: `npm config get registry` (and `npm config get @scope:registry` for a scoped
  package) run with every option you gave (abbreviated and unknown flags included),
  from the directory you run it in, so project, user, global and builtin config and environment variables count as npm
  counts them. If npm cannot answer, or answers with something that is not a URL, the command is
  refused. A package with `"private": true` that resolves to a public registry is refused with the
  registry named. A tarball argument and workspace flags are refused because they cannot be checked.
- NuGet: the `.nupkg` is opened as a zip and every entry is scanned. `--source` (`-s`) and
  `--symbol-source` (`-ss`) are checked against the registry list. While a registry list is set, the
  push is refused unless `--source` is given (the default comes from NuGet.Config, which is not read),
  refused with `--configfile`, and refused unless symbols are handled: pass `--no-symbols` (`-n`), or
  `--symbol-source` with a feed URL on the list, because dotnet otherwise pushes a `.snupkg` to an
  implicit symbol source. With no list set, none of this applies.
- Nested archives (zip, gzip, 7z, xz, rar, bzip2, found by their first bytes) in a package are refused,
  naming the entry, because their contents cannot be scanned. Content with NUL bytes is also scanned
  with them removed, which finds a key stored as UTF-16 in a .NET assembly.
- `--ignore-scripts` is passed on to `npm pack`. Without it, pack runs the lifecycle scripts, and files
  that `prepublishOnly` or `prepare` write only at publish time are not scanned.
- NuGet options are read as `-s url`, `-s=url` and `-s:url`; a repeated `--source` or
  `--symbol-source` is refused.
- A file or entry over 64 MiB, or a package over 512 MiB in total, is refused, not skipped.

To limit where a repository may publish, list the allowed registry hosts (a full URL is reduced to its
host). With none set, every registry is allowed. A registry outside the list is refused before
anything is sent, and a name from NuGet.Config cannot be resolved, so pass the feed URL. A folder
source is always allowed. If the setting cannot be read, the command is refused.

```sh
git config --add baryovm.allowedRegistries registry.npmjs.org
export BARYOVM_ALLOWED_REGISTRIES=registry.npmjs.org,api.nuget.org   # alternative; "*.corp.example" works
```

A refusal exits 1, names `file:line (rule)` and never prints the value. Flags belong to npm and dotnet;
set `BARYOVM_OUTPUT=json` for a JSON refusal. OTP and Trusted Publishing stay with you.

### Deploying to the cloud

`baryovm cloud wrangler`, `baryovm cloud firebase` and `baryovm cloud gcloud` check a deploy before the
real CLI runs, then run it with your arguments unchanged. Put options after the command.

```sh
baryovm cloud wrangler deploy --env production
baryovm cloud firebase deploy --only hosting --project my-project-id
baryovm cloud gcloud iam service-accounts keys create ~/.config/gcloud/key.json --iam-account sa@p.iam.gserviceaccount.com
```

- wrangler: for `deploy`, `publish` and `versions upload`, `wrangler ... --dry-run --outdir <scratch>`
  builds the Worker and every bundle file is scanned. `wrangler.toml` (or `.json`, `.jsonc`, or the
  `--config` file) and a `--var NAME:value` argument are scanned too. A key in `vars` is refused and the
  message points at `wrangler secret put NAME`. If the dry run fails, the deploy is refused.
  The `--assets` directory and the `directory` under `[assets]` in the config are scanned too (assets
  are not in the dry-run bundle), and so are `--site` and the `[site] bucket`; assets or a site in the config with no readable directory are refused. A `--dry-run` you give is dropped and never skips the
  checks. If no config is in the current directory but one is in a parent (the search goes to the file system root, past the git root),
  the deploy is refused: run from that directory. A failed
  dry run shows only the last 4 lines of its output. `pages deploy <dir>` and `pages publish <dir>` scan
  the directory. Other commands pass through.
- firebase: for `deploy` and `hosting:channel:deploy`, the project id is read from `--project` (an id or
  an alias in `.firebaserc`), then the `.firebaserc` default, and printed. If it cannot be determined,
  the deploy is refused. The files in each hosting `public` directory and functions source from
  `firebase.json` are scanned, minus that entry's `ignore` globs. A hosting `source` (web frameworks)
  is refused because what it builds cannot be listed. An entry with `predeploy` is refused, because the
  build would run after the scan: build first and remove `predeploy`.
- gcloud: everything passes through except commands that write a key or secret to a file
  (`iam service-accounts keys create <file>`, `secrets versions access --out-file`,
  `kms decrypt --plaintext-file`). If the file is inside a git working tree, the command is refused. A
  key command that uses `--flags-file` is refused, since the file can set the output path. A
  key command that cannot be read, or a path git cannot place, is refused. The command words are found
  anywhere among the arguments, and every word after them is checked as a possible file, so an unknown
  option cannot hide the command.

To limit which cloud projects a repository may deploy to, list the project ids. With none set, any
project is allowed. While a list is set, `firebase deploy` needs `--project`, because the active project
from `firebase use` is kept outside the repository and cannot be read. If the setting cannot be read,
the command is refused.

```sh
git config --add baryovm.allowedCloudProjects my-project-id
export BARYOVM_ALLOWED_CLOUD_PROJECTS=my-project-id,my-staging-id   # alternative
```

A refusal exits 1, names `file:line (rule)` and never prints the value. Set `BARYOVM_OUTPUT=json` for a
JSON refusal.

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
