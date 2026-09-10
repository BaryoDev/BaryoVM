# Changelog

Notable changes to BaryoVM. Follows [Keep a Changelog](https://keepachangelog.com) and
[semantic versioning](https://semver.org).

While the version is below 1.0 the CLI surface may still move, and a release that refuses something
previously accepted is called out here rather than left to be discovered.

## [Unreleased]

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

[Unreleased]: https://github.com/BaryoDev/BaryoVM/compare/v0.2.1...HEAD
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
