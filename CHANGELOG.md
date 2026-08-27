# Changelog

Notable changes to BaryoVM. Follows [Keep a Changelog](https://keepachangelog.com) and
[semantic versioning](https://semver.org).

While the version is below 1.0 the CLI surface may still move, and a release that refuses something
previously accepted is called out here rather than left to be discovered.

## [Unreleased]

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
