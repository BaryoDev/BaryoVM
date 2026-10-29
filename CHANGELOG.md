# Changelog

Notable changes to BaryoVM. Follows [Keep a Changelog](https://keepachangelog.com) and
[semantic versioning](https://semver.org).

While the version is below 1.0 the CLI surface may still move, and a release that refuses something
previously accepted is called out here rather than left to be discovered.

## [Unreleased]

### Added

- **`--dry-run` on `stack release`, `stack deploy`, `stack backup`, `stack restore`, `vm bootstrap`
  and `deploy`.** Each prints the commands it would run and opens no connection. `stack release
  --dry-run` shows the rsync invocations (with `--delete` and the trailing slash), builds, hooks,
  backup and compose up, and never runs a hook, takes the release lock or writes a record. `stack
  restore --dry-run` needs no `--yes` and names the backup it would use. In `-o json` the plan is in
  `data`. ([#34])
- **`stack release` records its progress and can resume.** A JSON file on the host, written under
  the host lock before the first step that changes anything, holds the commit, the backup taken, the
  steps done and the failed step with its error. A finished release removes it and records the
  deployed commit. A release that finds one refuses and says what the earlier run did and did not
  touch. `--resume` continues from the first step not done with the same backup, and `--clear`
  removes the record. A half-applied deploy is not resumable and points at `stack restore`. ([#136])
- **`stack rollback` goes back to the previous images.** `stack release` and `stack update` save the
  running images in `images-before.json` next to the backups before the first start. `stack rollback
  <name>` retags them and brings the stack up, and `--list` shows what is recorded. It never touches
  the database: it prints the matching backups and leaves `stack restore --yes` as the explicit
  step. ([#37]) Clearing a half-applied release with `stack release --clear` protects that record
  until a release finishes or `stack rollback` runs, so the next release or update cannot replace the
  last good images.
- **Adopt a hand-run container.** `stack release --adopt` and `stack deploy --adopt` remove a
  hand-run container that holds a `container_name` the compose file wants, after printing its name,
  image and created time. If it uses a named volume the compose file does not declare `external:
  true`, the adoption is refused, nothing is removed, and the volume name and the YAML to add are
  printed. There is no bypass flag. ([#7])
- **`verifyVisual` in the release manifest.** Opens the page in a headless browser on the VM at each
  listed width, saves a screenshot per release and width, and fails the release naming any width
  where the page scrolls sideways. ([#70])
- **`requireLocal` in the release manifest.** Lists paths under `localRoot` that must exist. A
  missing one refuses the release before the backup, the sync and `postDeploy`, and names the path.
  ([#68])
- **Source ranges for the SSH and web ports.** `vm harden --ssh-from <cidr,...>` limits port 22 in
  firewalld or ufw and refuses a list that excludes the address you are connecting from. `vm
  provision` takes per-port source ranges for the Lightsail firewall, including a `cloudflare`
  preset fetched at run time; a failed fetch aborts instead of opening the port. Unset ports stay
  open to `0.0.0.0/0` as before. ([#106])
- **Passive edge checks in `scan`, and `--resolver`.** After the ZAP baseline, `scan` checks CAA,
  SPF, DMARC, whether the bare IP serves a site over HTTPS (no SNI) or HTTP, `security.txt` per RFC
  9116, and reports the HSTS header as a value. Findings join the ZAP findings in one list with the
  same severities, `--fail-on` and `-o json` envelope; edge findings add `check` and `detail`. It
  sends five DNS lookups and three HTTP requests, each with a timeout inside a 30 second cap.
  `--resolver <ip[:port]>` pins the DNS server. The bare IP probe skips non-global addresses and the
  HTTP dialer refuses them, and text from the network is stripped of control characters and capped
  at 300 characters. `--url` now refuses a bare IP, a non-punycode name and a URL with credentials.
  ([#107])
  `scan` now refuses IP and single-label targets (`localhost`, raw IPs) that were passed straight to
  ZAP before, and `--fail-on low` or `informational` now counts the DNS posture findings.
- **`vm prune <name>`.** Plans disk cleanup by default (item, size, reason) and deletes nothing
  until `--apply`, which prints the space freed and `df` before and after. Never touches named
  volumes, images any container uses, `bin/` without a project file, or dirty worktrees. `--floor
  <GB>` does nothing while enough space is free, and apply takes a `flock` on the VM. ([#138])
- **`vm patch <name>`.** Updates OS packages on a VM as a detached systemd unit, decides the reboot
  from the host (`needs-restarting -r` or `/var/run/reboot-required`), reboots only with `--reboot`
  or a prompt, then checks every container, nginx and `nginx -t` against a snapshot taken first. A
  container that was already unhealthy is not a failure; one that does not return makes the command
  exit non-zero and names it. `--dry-run` lists pending updates. ([#146])
- **CI security checks.** The CI workflow now runs `govulncheck`, `gosec` and `golangci-lint` in a
  separate `security` job, each pinned to a version. A small `.golangci.yml` keeps the default
  linters; findings on the tree are fixed or suppressed one line at a time with a reason. ([#45])
- **`tls` block in the release manifest, and `stack tls <name>`.** Writes the site's nginx vhost
  (static root or a proxy to a local port) and gets the certificate with certbot, then runs
  `nginx -t` and reloads. Safe to rerun. A vhost not written by BaryoVM is refused unless `--adopt`,
  and a config that fails `nginx -t` is rolled back without a reload. `--dry-run` prints the configs
  and commands. ([#49])
- **`stack env set | list | unset`.** Edit a stack's env file on the VM without ssh. The value comes
  from a no-echo prompt or piped stdin and travels on the ssh session's stdin, never on a command
  line. The file is created if missing and kept at mode 600. `--recreate` runs
  `compose up -d --force-recreate` after a write. `-o json` carries keys, never values. ([#147])

### Changed

- **`vm harden` audits more, and separates what it fixes from what it only reports.** It now fixes
  `~/.ssh` permissions, moves `authorized_keys.*` backups aside, puts the post-quantum key exchange
  first when OpenSSH has one (including the Oracle Linux crypto policy path), and sets nginx
  `server_tokens off`. It reports without changing stale keys, pending updates, unattended upgrades,
  a missing `default_server` on 443, a kernel waiting for a reboot, and port 22 open to everyone.
  Reported items show as warnings with `"action": "report"`, are counted in `reported`, and the run
  ends with a changed versus reported summary. ([#67])
- **`stack release` says which verify plan ran.** The success line names the manifest `verify` list
  or the stack `healthUrl` and the attempt it passed on. In JSON it is `data.verify` with `source`,
  `checks` and `attempts`. A release with nothing to verify keeps its warning. ([#122])
- **`stack release` checks container names after the sync.** The check read the compose file from
  the VM before the sync, so a first release could not run it. It now runs after the sync and before
  any image build or `compose up`, and a colliding `container_name` is refused. `stack deploy` and
  `stack update` are unchanged. ([#150])
- An unknown subcommand of a command group (`vm nosuch`, `stack nosuch`) now fails with `unknown
  command "nosuch" for "baryovm vm"` and a non-zero exit, with the JSON error envelope under `-o
  json`, instead of printing help and exiting 0. ([#120])
- `vm bootstrap` now runs its Docker install with `sudo -n`, so a host that wants a sudo password
  fails fast instead of hanging on a prompt.

### Fixed

- **A first `stack release` with a database configured no longer fails on the backup.** When the
  database container does not exist yet, the pre-release backup is skipped with one line. A
  container that exists and fails to dump still stops the release, and the error names the cause
  ("no container named X") instead of the script. A stack directory that is not there yet no longer
  draws a container-name warning. ([#121])
- **Harden fixes.** `vm harden --ssh-from` now removes a bare `ufw allow <port>` and the open ssh
  service or port in every active firewalld zone, and reports instead of "applied" when an open rule
  remains. Lightsail refuses a limited port that names only IPv4 or only IPv6 ranges, since the API
  docs do not say an empty family is closed. ([#106])

## [0.5.0] - 2026-10-20

BaryoVM can now tell a Windows host from a Linux one, install a GitHub Actions runner, scan a
deployed site, release without rsync, and keeps a local log of what it ran.

### Added

- **Windows hosts, `vm exec` only.** `vm add` detects whether a host is Linux or Windows and records
  it in `fleet.json` as `os`, plus `shell` (cmd or powershell) for Windows. `--os linux|windows`
  skips detection, and `--shell` goes with `--os windows`. On Windows, `vm exec` runs its command as
  PowerShell and returns the exit code and plain-text errors under either default shell. Every
  other command refuses a Windows VM before connecting. Entries without `os` read as Linux, so
  existing fleets are unchanged. ([#111])
- **`vm runner <vm> --repo owner/name`.** Installs a GitHub Actions self-hosted runner on a VM as a
  systemd service under a `github-runner` user, with `MemoryMax` and `CPUQuota` caps (`4G` and
  `200%` by default). `vm runner remove` uninstalls and unregisters it. A repo that is not private
  is refused before connecting. Tokens come from the local `gh`, go over stdin and are never stored.
  The runner tarball is checked against GitHub's published digest. Safe to rerun. ([#116])
- **`scan --url <target>`.** Runs the OWASP ZAP baseline (passive: crawl and report, no attack
  payloads) in Docker against a deployed site and lists findings worst first. `--fail-on <severity>`
  exits non-zero at or above it. The image is pinned to `zaproxy/zap-stable:2.17.0`, and a scan is
  killed inside its container after 10 minutes by default.
- **`action.yml`.** A composite GitHub Action that downloads a release, checks its checksum and runs
  one baryovm command. The command is split on whitespace and not evaluated by a shell.
- **Activity log.** Every command except `vm list`, `stack list`, help, version and completion
  appends one JSON line to `~/.baryovm/activity.jsonl` (0600): time, version, action, VM or stack,
  result, duration and host. Flag values and error text are never recorded. `BARYOVM_ACTIVITY=off`
  turns it off. See `docs/activity-log.md`. ([#126])
- **Static release works without rsync.** When local rsync is missing, `stack release` writes a
  tar archive with Go and streams it into `tar -x` on the host over the SSH session, then removes
  files deleted locally, only inside the entry's destination directory and only after a clean
  extract. rsync stays the default when it exists. The transport is printed and is in the JSON
  result as `data.transport`. Excludes follow rsync's matching under both; a pattern the tar
  transport cannot match is refused rather than guessed. Linux hosts only. ([#127], [#42])
- Generated Cobra command reference pages under `docs/commands`, with a CI check to keep them
  synchronized with the CLI.

### Changed

- **Host certificates, revoked keys and unreadable known_hosts files are refused.** Each of these
  used to fall through to trust on first use, so a host on a VM's address could replace the
  recorded key with its own. BaryoVM now records and checks plain host keys only, and a known_hosts
  line it cannot parse stops the connection and names the file to fix. This refuses something 0.4.0
  accepted.
- An `os` value in `fleet.json` that is neither `linux` nor `windows` is refused, with the command
  that fixes it, instead of being run as Linux.
- `doctor` no longer fails a machine for lacking rsync, and has a "release transport" line saying
  which one a release will use. This supersedes the 0.2.x note below that rsync is required.

### Fixed

- **A known host is no longer reported as changed when it offers several key types.** BaryoVM now
  asks for the key types it has on record for the host first, so a host recorded with ED25519 (for
  example by `ssh`) is not compared against the ECDSA key Go preferred. ([#118])
- The "verify it" hint after a new host key, and the strict-mode error, pass the port to
  `ssh-keyscan` with `-p`. The hint used `host:22`, which `ssh-keyscan` cannot resolve, and the
  strict-mode line dropped a non-default port.

## [0.4.0] - 2026-09-21

BaryoVM now checks which machine it is talking to. Until this release every SSH connection accepted
whatever host key it was offered.

There was no 0.3.1 release. The fixes planned for it are in this one.

### Changed

- **SSH host keys are verified.** The first connection to a host records its key in
  `~/.baryovm/known_hosts` (written 0600) and prints the fingerprint when the command finishes. Every
  later connection must offer the same key. A changed key stops the command and prints the stored
  and offered fingerprints. Keys you already trust in `~/.ssh/known_hosts` are read as a second
  source, never written. This refuses something earlier versions accepted: a VM rebuilt with a new
  key now needs `baryovm vm forget-key <name>` once. There is no flag that turns the check off for
  every host. ([#12])

### Added

- **`vm forget-key <name-or-host>`.** Drops one recorded host key, so the next connection learns
  the new one. Takes a fleet name or `host:port`. ([#12])
- **Strict host keys for CI.** `--strict-host-keys`, or `BARYOVM_STRICT_HOST_KEYS=1`, makes an
  unknown host an error instead of a key to learn, since a CI runner has no first use to trust. The
  error carries the offered fingerprint and the `ssh-keyscan` line that records it. The flag wins
  over the environment only when it is given. A changed key is still reported as changed.
- **The recipe format, as a package.** `internal/recipe` parses and validates a recipe: services,
  static files, routes, a list of backup strategies each with its restore, and the inputs it needs,
  where a secret input may not carry a default. An unknown schema version or field is refused. No command reads a recipe
  yet; the README section "Where it's going: recipes" describes the plan.
- **`vm exec`.** Run a one-off command on a registered VM over the SSH key already
  in `fleet.json`, so checking state does not mean dropping back to raw `ssh`.
  The remote command goes after `--`, so its flags (`df -h`) reach the VM instead
  of being read as CLI help. There is no `--sudo` flag (put `sudo -n` in the
  remote command). A non-zero remote exit becomes a non-zero CLI exit, and
  `-o json` keeps stdout, stderr and exitCode as separate fields. ([#23])

### Fixed

- **`fleet.json` keeps fields this binary does not know.** An older binary dropped any field it had
  no struct field for the next time it saved the file, for example on an unrelated `stack add`, so
  a downgrade could silently remove `releaseFile`, `autoUpdate` or `noDatabase` from every stack.
  Unknown fields are now read and written back. This protects fields added from here on; binaries
  already installed still drop what they do not know.
- **The self-assign hint now answers a bare claim.** "Taking this", "Picking this up", "Grabbing
  this one" and "On it" got no hint, and "Working on this" matched only as the whole comment, so
  "Working on this, PR tomorrow" was ignored. The pattern is now covered by a table of phrasings in
  `internal/workflows`. (#72)
- **Re-running `stack add` no longer clears a stack's update policy.** It replaced the whole
  record, so `autoUpdate`, `healthUrl`, `updateServices` and `noDatabase` were lost, along with any
  flag not passed again. That mattered because `stack backup` tells you to register a missing
  database with `stack add`, and following it turned off unattended updates. Re-adding now changes
  only the fields its flags name. (#63)
- **A compose up no longer fails on a container an earlier recreate left renamed.** Compose renames
  the container it is replacing to `<id>_<name>`, and when an earlier run left one of those behind,
  the next `stack release` failed with a name conflict and a public site served 502 for two
  minutes. `stack release`, `stack deploy` and `stack update` now remove that leftover and try the up
  once more. A conflict on any other container name still fails as before. (#57)
- **A container name taken by a hand-run container is caught before any work.** When the compose
  file sets a `container_name` that a container started with `docker run` already holds, compose up
  failed at the end of `stack release`, after the backup, the rsync and a full image build.
  `stack release` now checks the names first, and `stack deploy` and `stack update` check before
  pulling or backing up. The error names the container and the `docker rm -f` that frees the name.
  Nothing is removed for you, and containers this compose project owns are not affected. (#1)

## [0.3.0] - 2026-09-10

Two new commands, an install that does not assume a Go toolchain, and the class of bug where a
setting was honoured in some code paths and ignored in others.

`vm harden` and `vm threats` came out of a real incident rather than a plan: the box this is
developed on was taking 494 invalid-user attempts from 46 sources in fourteen hours.

Three of the fixes below are the same shape. A field existed, some call sites read it and others
did not, and each gap was reported separately as its own bug. They are one fix now, with the
decision made in one place so a new call site cannot quietly skip it.

### Added

- **Homebrew on macOS.** `brew install arnelirobles/tap/baryovm`, or `brew tap arnelirobles/tap`
  once and then `brew install baryovm`. It ships as a cask, since GoReleaser deprecated the older
  formula block, which means Linux Homebrew is not covered and the release archive is the Linux
  path. The unprefixed `brew install baryovm` needs homebrew-core, which asks a self-submitting
  owner for 90 forks, 90 watchers or 225 stars, so it is not the target yet. The README now leads
  with brew, then the archive with checksum verification, then `go install` last, because needing
  a Go toolchain to try a deployment tool rules out most of the people it is for. ([#5], [#32])
- **`CONTRIBUTING.md`, a code of conduct, and issue templates.** The contributing guide leads with
  the rule that matters most here: a change to command construction needs a test that reads the
  generated string, because every remote operation ends as text sent to a shell on someone's
  server, and the failure mode is a command that looks right and means something else. ([#2])



- **`stack set-update --no-database`, so a stack with no database can still update unattended.** It
  pairs with the refusal below: a stack that genuinely has nothing to back up records that once,
  deliberately, instead of being silently exempt from a safety rule.


- **`vm harden` and `vm threats`.** A VM with a public SSH port is found within minutes: the box
  this was written on took 494 invalid-user attempts from 46 sources in 14 hours. `vm harden`
  applies an idempotent policy, sshd `PerSourcePenalties` where the daemon supports them plus
  fail2ban with escalating bans, and `vm threats` reports what is arriving and, first, what
  actually got in. The policy will not change the port, disable public key authentication or touch
  `authorized_keys`, validates the config before reloading and rolls back if sshd rejects it, so it
  cannot lock you out of a machine you only reach over SSH. `-o json` on both, as everywhere else.

- **`preDeploy` commands, which run before `compose up`.** A guard in `postDeploy` runs after the
  thing it guards has already taken effect: the umbraco-pwa stack compares its committed compose
  file against the one the VM runs, and in `postDeploy` that comparison happens after the drifted
  file has brought the stack up. The release goes red over a stack that is already running the
  configuration the check exists to refuse, which is a post-mortem rather than a gate. Same
  `remoteRoot` handling as `postDeploy`. ([#59])

### Fixed

- **`stack release` sent rsync to port 22 no matter what port the VM was registered on.** `RsyncCmd`
  never received the `Port` field that `vm add` sets and that `VM.Target()` and `sshx.Dial` honour
  everywhere else, so a VM on `--port 2222` had its command sessions on 2222 and its file sync on 22.
  The key path is quoted now as well, using rsync's own convention. rsync parses the `-e` value
  itself rather than handing it to a shell, which was checked by pointing `-e` at a program that
  printed its argv, so the shell escape a first attempt used made rsync exit with
  `Missing trailing-'` instead of fixing anything. ([#9])
- **`stack logs` answered an empty log and a failed look with the same bytes.** `{"output": ""}` was
  what you got for a healthy container whose app writes to a file, for a stack that was not running,
  and for a read that did not reach Docker. The result now carries a state (`read`, `silent`,
  `not-running`, `unknown`), a line count and a note, so a machine consumer can tell them apart
  rather than a human inferring it. `stack backups` had the same shape and gets the same treatment.
  The other commands' envelopes are unchanged, and a test pins them byte for byte. ([#19])
- **`doctor` did not check the tools a release actually runs.** It checked the docker binary, which
  nothing local uses, since every docker call BaryoVM makes is remote over SSH, and it missed `rsync`
  and `ssh`, which `stack release` shells out to on the operator's own machine. A release without
  them failed after the pre-release backup had already run. rsync and ssh are required now, docker is
  reported without failing the machine, and `--fix` installs rsync where it can and says why when it
  cannot. The dead AWS installer is gone. ([#17], [#42])
- **`stack update --auto` ran unattended on a stack with no way back.** It already refused a stack
  without `autoUpdate`, without a `healthUrl`, and `--auto --no-backup`, all so an unattended update
  keeps a way back. A stack with no `dbContainer` and no `dbName` fell through all three: the backup
  was skipped and the update ran anyway. It is refused, `--no-database` is the deliberate way out,
  and the flag refusal is reported before the configuration one so following the advice does not lead
  to a second refusal. ([#13])
- **A stack's `--sudo` registration reaches every remote command its release runs.** The prefix was
  written at each call site by hand, so `stack release` built its images as the SSH user while the
  `compose up` beside it ran as root ([#8]), and `preDeploy` and `postDeploy` ran unprivileged, which
  is where `restorecon`, `nginx -t` and `systemctl reload` live ([#36]). One helper writes the prefix
  now and one field decides: `release.Load` folds the stack's registration into the manifest, and the
  rsync, the image builds, both hook lists, the pre-release backup and the closing `compose up` all
  read it. Hooks run as `sudo -n "${SHELL:-/bin/sh}" -c '<cmd>'`, so a compound command is elevated whole instead of
  up to its first `&&`. `stack deploy` has passed the setting through since 0.2.x, so the behaviour
  ([#48]) reports is already correct on this branch's base; it is pinned by a test now rather than
  left to regress.

  Two consequences for a stack registered `--sudo`. The receiving rsync is root, so `-a` starts
  honouring `-o` and `-g`, and synced files can arrive with the local source's ownership rather than
  the SSH user's. And hooks run in root's environment (`env_reset`, `secure_path`), so a hook calling
  a per-user tool needs its absolute path. A manifest saying `"sudo": false` cannot turn the
  registration back off.

[#2]: https://github.com/BaryoDev/BaryoVM/issues/2
[#5]: https://github.com/BaryoDev/BaryoVM/issues/5
[#8]: https://github.com/BaryoDev/BaryoVM/issues/8
[#9]: https://github.com/BaryoDev/BaryoVM/issues/9
[#13]: https://github.com/BaryoDev/BaryoVM/issues/13
[#17]: https://github.com/BaryoDev/BaryoVM/issues/17
[#19]: https://github.com/BaryoDev/BaryoVM/issues/19
[#32]: https://github.com/BaryoDev/BaryoVM/issues/32
[#36]: https://github.com/BaryoDev/BaryoVM/issues/36
[#42]: https://github.com/BaryoDev/BaryoVM/issues/42
[#48]: https://github.com/BaryoDev/BaryoVM/issues/48


- **`verify` and `postDeploy` commands run from `remoteRoot`.** They ran from the SSH login shell's
  home, so a command written against the repository, such as `sh scripts/check-live-demo.sh`, exited
  127 and the release reported `released but failed verification, the stack may need attention` for
  a stack that was healthy. Every other path in a manifest is already relative to the repository:
  `sync` entries, and a build's `dockerfile` and `context`. These two were the exceptions and nothing
  said so. The workaround was to write the `cd` into the manifest, which duplicated `remoteRoot` and
  gave no warning when the two disagreed. Found by adding a `verify` block to a stack that had none.
  ([#56])

## [0.2.1] - 2026-08-27

### Fixed

- **A static site can be released again.** 0.2.0's compose-directory guard refused every
  `noCompose` stack: a static site's stack directory is its webroot, so `remoteRoot` and the stack
  directory are legitimately the same path, and the guard read that as a manifest about to delete a
  compose directory's `.env`. There is no compose file and no `.env` to protect. Found by releasing
  a real static site with the new binary, which is the only way it could have been found: every
  test written for the guard described a compose stack. ([#55])

## [0.2.0] - 2026-08-27

### Added

- **`stack update` pulls, verifies and rolls back.** An update that cannot tell a healthy start
  from a crash loop is worse than no update, so this backs up first, recreates, waits for the
  stack's `healthUrl`, and puts the previous images back if it does not come up. `--auto` refuses
  any stack without `autoUpdate`, without a `healthUrl`, or combined with `--no-backup`. An
  unchanged stack is never recreated, so a nightly job is not a nightly restart.
- **`stack release` verifies the deploy worked.** Previously it printed `release done` having asked
  the running site nothing, which cannot tell a working deploy from one that landed and serves the
  wrong thing. The manifest gains `verify`; without it a stack `healthUrl` becomes a `curl --fail`
  probe. Both run on the VM, because a `healthUrl` is usually loopback and from a laptop would
  either fail or reach something local and pass. ([#50], [#53])
- **Static sites release too, not only compose stacks.** `noCompose` says up front that there is
  nothing to bring up, so a static site does not fail at `docker compose up` after the sync has
  already landed. `postDeploy` runs the things a static site needs afterwards, restoring an SELinux
  context or reloading nginx, in order, stopping at the first failure. `sudo` runs the **remote**
  rsync as root for a root-owned webroot. ([#22])
- Contributors can claim an issue by commenting `/take`.

### Fixed

- **`stack update` works on stacks whose `.env` is root-owned**, which is the correct ownership for
  a file holding a database password and previously made the stack un-updatable.

### Changed

- **A release now refuses a manifest whose `sync` would let `--delete` reach the compose
  directory.** The README has described this exclusion as a guarantee for some time and it was only
  ever a convention: nothing stopped a manifest listing that directory, and `rsync --delete` would
  then remove its `.env`. This is a refusal of something previously accepted, so a manifest relying
  on it will now fail, which is the point. Sync the application directories individually rather
  than the root that holds them. ([#53])

### Documentation

- `README.md` gains a sequence diagram of what a release actually does, and a section on why this
  beats improvising a deploy conversationally.
- `CLAUDE.md`, recording the conventions this repository already followed but had never written
  down. ([#30])
- Em dashes removed throughout, and kept out since. ([#24])

## [0.1.0] - 2026-07-17

First tagged release. Registers VMs you already own and drives their Docker Compose stacks over
plain SSH, agentless: deploy, release, backup, restore, logs, with `-o json` on every command.

[Unreleased]: https://github.com/BaryoDev/BaryoVM/compare/v0.5.0...HEAD
[0.5.0]: https://github.com/BaryoDev/BaryoVM/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/BaryoDev/BaryoVM/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/BaryoDev/BaryoVM/compare/v0.2.1...v0.3.0
[0.2.1]: https://github.com/BaryoDev/BaryoVM/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/BaryoDev/BaryoVM/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/BaryoDev/BaryoVM/releases/tag/v0.1.0
[#22]: https://github.com/BaryoDev/BaryoVM/pull/22
[#24]: https://github.com/BaryoDev/BaryoVM/pull/24
[#30]: https://github.com/BaryoDev/BaryoVM/pull/30
[#50]: https://github.com/BaryoDev/BaryoVM/issues/50
[#53]: https://github.com/BaryoDev/BaryoVM/pull/53
[#55]: https://github.com/BaryoDev/BaryoVM/pull/55
[#56]: https://github.com/BaryoDev/BaryoVM/issues/56
[#59]: https://github.com/BaryoDev/BaryoVM/issues/59
[#23]: https://github.com/BaryoDev/BaryoVM/issues/23
[#12]: https://github.com/BaryoDev/BaryoVM/issues/12
[#111]: https://github.com/BaryoDev/BaryoVM/issues/111
[#147]: https://github.com/BaryoDev/BaryoVM/issues/147
[#116]: https://github.com/BaryoDev/BaryoVM/issues/116
[#118]: https://github.com/BaryoDev/BaryoVM/issues/118
[#126]: https://github.com/BaryoDev/BaryoVM/issues/126
[#127]: https://github.com/BaryoDev/BaryoVM/issues/127
[#49]: https://github.com/BaryoDev/BaryoVM/issues/49
[#7]: https://github.com/BaryoDev/BaryoVM/issues/7
[#34]: https://github.com/BaryoDev/BaryoVM/issues/34
[#37]: https://github.com/BaryoDev/BaryoVM/issues/37
[#45]: https://github.com/BaryoDev/BaryoVM/issues/45
[#67]: https://github.com/BaryoDev/BaryoVM/issues/67
[#68]: https://github.com/BaryoDev/BaryoVM/issues/68
[#70]: https://github.com/BaryoDev/BaryoVM/issues/70
[#106]: https://github.com/BaryoDev/BaryoVM/issues/106
[#107]: https://github.com/BaryoDev/BaryoVM/issues/107
[#120]: https://github.com/BaryoDev/BaryoVM/issues/120
[#121]: https://github.com/BaryoDev/BaryoVM/issues/121
[#122]: https://github.com/BaryoDev/BaryoVM/issues/122
[#136]: https://github.com/BaryoDev/BaryoVM/issues/136
[#138]: https://github.com/BaryoDev/BaryoVM/issues/138
[#146]: https://github.com/BaryoDev/BaryoVM/issues/146
[#150]: https://github.com/BaryoDev/BaryoVM/issues/150
