# Changelog

Notable changes to BaryoVM. Follows [Keep a Changelog](https://keepachangelog.com) and
[semantic versioning](https://semver.org).

While the version is below 1.0 the CLI surface may still move, and a release that refuses something
previously accepted is called out here rather than left to be discovered.

## [Unreleased]

### Added

- **`vm harden` can ban nginx scanners.** `--nginx-ban-probes` writes a `baryovm-nginx-probes`
  filter and jail in their own files (`jail.local` is not touched). The jail watches every absolute
  `access_log` path in `nginx -T` and bans sources whose request path asks for `.env`, `.git`,
  `.aws`, `/proc/self/environ` and similar, never `/.well-known/` and never from the query string.
  PHP and WordPress paths are added only with `--nginx-no-php` (you say no site serves PHP) and
  only when `nginx -T` has no `fastcgi_pass`. It is opt in because
  another site can make a visitor's browser request such a path, and because behind a CDN or load
  balancer without `set_real_ip_from` it would ban the proxy (private ranges are never banned, and
  the run reports the missing `set_real_ip_from`). Without the flag the run only reports the jail.
  `--nginx-drop-probes` also answers those requests with 444 through a map and one include line
  per `server {` block, with every touched file backed up, `nginx -t` checked, and all files
  restored on failure. Without it, the run reports how many server blocks lack the include. A
  server block in a shape the step does not edit is reported, and a rejected jail, a rolled-back
  drop or a failed reload ends the run with an error.

### Fixed

- **`stack restore` checks the dump and saves the current database before it drops anything.** It
  runs `pg_restore -l` on every postgres dump (and `tar -tzf` on files archives) first, so a corrupt
  dump stops the restore with nothing changed. It then dumps the current database to
  `pre-restore-<timestamp>.dump` in the backup dir and prints the path. If a step fails after the
  drop, the output gives the safety dump path and the exact command that restores it. A set that
  fails part way is still not rolled back.
- **`stack release` no longer calls a failed lock a held lock.** When the release lock cannot be
  created (permission denied in a root-owned parent of `remoteRoot` with `sudo` off, a read-only
  filesystem), the error names the directory and the OS message and says to set `sudo` or use a
  writable `remoteRoot`. Only a lock that exists says another release is starting.

## [0.6.0] - 2026-11-02

Ship, prove and repo commands, guarded `git`, `gh`, publish and cloud wrappers, static sites, podman,
backup strategies, verify specs, project files, and a GitHub Action.

This release refuses things earlier versions accepted. `vm provision` and `up --provider` need
`--yes` to create anything. A release manifest's paths and `file:` references must stay inside its
own directory, and `env:` references must start with `BARYOVM_` (the default project key variable
is now `BARYOVM_DEPLOY_KEY_PATH`). An unknown subcommand now exits non-zero.

### Added

- **Keys held by ssh-agent.** When the file at a VM's key path cannot be parsed as a private key,
  BaryoVM reads the `.pub` beside it, asks the agent on `SSH_AUTH_SOCK` for the key with the same
  public key, and signs through the agent. This covers a passphrase-protected key loaded with
  `ssh-add`, a FIDO security key, and a macOS Touch ID (Secure Enclave) key handle. `fleet.json`
  still holds only the path. A key that parses is used directly, as before. If the agent does not
  hold the key, the error says `run ssh-add`. Provisioning, which reads the public half of the same
  key, takes the same path. rsync's ssh gets the same path with `-i`, and OpenSSH signs through the
  agent, so `stack release` and `site deploy` work with these keys too (checked with a Touch ID key
  against an Oracle Linux 9 VM).
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
- **`baryovm site add <domain> --vm <vm> --root <dir>` and `baryovm site deploy <domain> <dir>` provision and update a static site.** `site add` creates the webroot and the ACME challenge directory, writes an nginx server block from a template (HTTP to HTTPS redirect, TLS, `try_files` for a static export, a year-long cache on hashed assets, `no-cache` on HTML, gzip scoped to the static locations), gets a certificate with certbot in webroot mode, runs `nginx -t` and reloads, and relabels the webroot with `restorecon` where SELinux is on. The order is HTTP-only block, certificate, full block; a failing step puts the previous server block back, and the error and `data.failedStep` name the step. An existing site is refused without `--force`; with `--force` on a live HTTPS site the HTTP-only stage is skipped so HTTPS stays up, and a leftover `.baryovm-prev` from an interrupted run stops the command so the original is never overwritten. `site deploy` syncs a directory into the recorded webroot with the same rsync as `stack release`, then runs `nginx -t` and reloads. Sites are recorded in `fleet.json`. DNS is not handled. Tested with fakes through the cobra commands, never against a real VM. ([#18])
- **`baryovm status`.** One read-only screen for every registered stack: whether its containers are up, the commit of the last finished release, a release that failed or stopped part way, and the backups on the VM, plus the verify assertions with `--verify`. Each VM is asked once over one SSH connection, VMs are queried concurrently (`--concurrency`) with a per-VM `--timeout`, and a VM that does not connect shows as unreachable with the reason while the other rows still print, and one that runs out of time keeps the stacks it already answered. A column that is not configured shows `-`, one that could not be read shows `?` with the reason. A forced-command VM is sent its `status` request only and shows as limited. `-o json` carries the same report. `--vm` and `--stack` scope it. ([#27])
- **Ship.** `baryovm ship <name>` runs the release gate, tags and pushes, watches the workflow run, asserts the publish step ran and was not skipped, polls the registry (NuGet or npm) until every package resolves, checks the published artifact's metadata against the spec's assertions, then creates the GitHub release. BaryoVM never publishes and holds no registry credential; the GitHub token comes from `--token-ref`. `--dry-run` runs every check and changes nothing. A rerun after an interrupted watch finds the tag at HEAD and does not tag again. The assertions and the values seen are saved under `$BARYOVM_HOME/ship/` and returned by `-o json`. ([#28], [#51])
- **Prove.** `baryovm prove <name>` prints the release record `ship` saved as one page: what shipped, what was published with each artifact assertion and the value seen, and whether the deployment was verified. It reads only the local record, prints anything missing as not checked, exits non-zero on a failed assertion or step, and refuses a dry run or a missing record. `--version` picks a release, the default is the highest by semver. ([#29])
- **A stack can run on podman, and `vm bootstrap` no longer assumes systemd.** `stack add --runtime
  podman` (also on `deploy`, `up`, `stack discover` and `vm bootstrap`, and as `runtime` in
  `remote.json`) makes deploy, update, release builds, ps, logs, backup, restore and the drill run
  `podman` and `podman compose`. The runtime is a fixed set (`docker` or `podman`); anything else is
  refused. Bootstrap detects apt-get, dnf, yum, apk or pacman, installs podman from the distro
  package, and skips the systemctl step with a `NO_SYSTEMD` line on a host without systemd.
  `USAGE.md` lists which compose commands are covered and which are unverified on podman-compose.
  The TLS helpers, health, patch, prune, scan and runner stay on docker. ([#35])
- **`stack restore --drill` proves a backup restores.** It loads the newest dump (or `--file`) into a
  throwaway postgres container on the VM, checks the restore had no errors and the table count is
  above zero, and removes the container, also when the drill fails. The live database and its
  container are only read for their image name, so `--drill` needs no `--yes`. In `-o json` the
  result is in `data`. ([#38])
- **`stack backup --pull` keeps a copy off the VM.** After the backup it copies that run's files (the dumps and the config copy, never a `.partial`) over the same SSH connection to `~/.baryovm/backups/<stack>/`. The directory is `0700`, files are `0600`, and an existing directory that group or others can write is refused. `-o json` lists the pulled paths and sizes in `data.pulled`. Refused on a forced-command VM. Object storage is not part of this. ([#39])
- **`stack update --auto` can tell someone when it fails.** `--notify <url>`, or `BARYOVM_NOTIFY_URL`
  so the URL can be a CI secret, posts the JSON result envelope to a webhook when an unattended run
  fails or rolls back. `--notify-on always` posts every run. https only (http for localhost), 10 second
  timeout, one retry, bounded body. A failed notification makes the exit non-zero and shows in
  `data.notify`, but never hides the update result. The URL is never printed, only scheme and host.
  `docs/examples/scheduled-update.yml` is a GitHub Actions cron workflow to copy, and USAGE.md has an
  "Unattended updates" section. ([#40])
- **`vm health` reports the monthly questions about a machine.** Read only, over SSH with `sudo -n`.
  Checks disk use on `/` and the docker root, pending updates split into security and other, whether
  a reboot is required, containers that are restarting or exited unexpectedly, and whether automatic
  security updates are enabled, each with an ok, warn, fail, unknown or skip verdict. `--all` covers
  every registered VM. Exits non-zero on any fail, and any pending security update is one. In `-o
  json` the reports are in `data`. The limits are flags: `--disk-warn` (80), `--disk-fail` (90),
  `--max-updates` (25) and `--max-restarts` (5). Bad values are refused before any connection, and
  the limits used are in `thresholds` in the JSON report. ([#41])
- **`baryovm stack discover <vm>` inventories a VM that already runs compose projects.** It reads `docker compose ls` and each project's config, then prints the directory, service count, a postgres container guess (first service whose image name contains postgres, marked as a guess) and the `stack add` command per project, every value quoted for a shell. Read-only by default. `--adopt` registers the projects through the same code as `stack add`, asking per project on a terminal and needing `--yes` without one or under `-o json`; projects already registered are skipped and reported. `--sudo` runs docker through `sudo -n`. `-o json` returns one result with a list of projects. ([#47])
- **`baryovm repo init <owner/repo> --template <name>` scaffolds release CI, and `baryovm repo verify <owner/repo>` checks that trusted publishing works.** `init` writes the workflows and furniture (CODEOWNERS, issue templates, CONTRIBUTING) into a local checkout, applies branch protection with the required checks through the GitHub API, and prints the owner, repository, workflow file and environment to enter on nuget.org or npmjs.com. It prints a plan and does nothing without `--yes`, never overwrites a file without `--force`, and leaves branch protection that already exists alone unless `--replace-protection` is given. It refuses any path that leaves the checkout through a symlinked folder. It does not create the registry policy, which has no API on either registry; the output spells out the order (push the workflow, create the policy by hand, then verify). `verify` runs the workflow's dry run and reports whether the OIDC exchange step succeeded, reading only the run it started. Two templates are built in, `dotnet-nuget` and `node-npm`, based on the umbraco-pwa and dopaminejs workflows, with every action pinned to a commit sha. `--templates-dir` adds or replaces templates. The token comes from `--token-ref`, `GITHUB_TOKEN`, `GH_TOKEN` or `gh auth token`. ([#52])
- **Release gate.** `baryovm stack publish <name>` refuses to tag unless the tree is clean on the default branch and synced, the version is not tagged or published, the changelog has a section for that exact version, the NuGet release notes mention it, and the `requireDeployed` stack released this commit. Each refusal names its cause. Then it tags and pushes the tag. `stack set-publish` stores the spec on the stack, and a project file carries it too. ([#51], [#28])
- **A stack can name a managed database and snapshot it before a release, through the AWS or Azure CLI you are signed into.** `baryovm stack set-managed <name>` stores an AWS profile and region, or an Azure subscription and resource group, plus the database identifier. Names only: the CLI's own sign-in authenticates and nothing secret reaches `fleet.json`. `stack release` then runs `aws rds create-db-snapshot` and `aws rds wait db-snapshot-available`, or `az postgres flexible-server backup create`, before the VM backup, and a failed call stops the release. `--no-backup` skips it, `--dry-run` lists the calls. Calls run as an argv with every name checked. `-o json` carries each call's argv, exit code and output in `data.cloud`. `baryovm doctor` reports a missing or signed-out `aws` or `az` for any stack that uses one and names the command to run. The reachability check and the `vm ping` and `stack ps` display are not built. ([#81])
- **Project files.** `baryovm project release` releases the stacks of a committed, secret-free `baryovm.project.json` in order, with no `vm add` or `stack add`, and stops at the first failed release or verify. The file uses `fleet.json`'s own field names and structs (plus `project` and `order`), so a team or a CI job shares one definition. A literal key, token or password in any field is refused with the field named, a VM needs a pinned `hostKey` or `--strict-host-keys`, and unknown fields round-trip. `baryovm project export` writes the file from a registered fleet. The project file is not merged with `fleet.json`. ([#84], [#26], [#91], [#43])
- **Stacks can deploy from a recipe in the app's own repository.** `baryovm stack add NAME --recipe PATH` (or `stack release NAME --recipe PATH`) reads a versioned `baryovm.recipe.json` and `stack release` brings its services up one at a time, waiting on each `healthUrl`. If a service fails, every service touched so far goes back to the previous compose file; a service the previous recipe did not have is removed. Names, images, ports and paths from a recipe are checked against a strict shape and quoted on every remote command, and a recipe with an unknown `schema` is refused. Inputs are bound with `--input NAME=env:VAR` or `file:PATH`; a secret input takes a reference only and never a default, and `cmd:` is refused. A stack without a recipe releases as before. Not built yet: `files`, `routes`, `backup` and service `build` in a recipe release, `--dry-run`, `--resume`, and a generic `stack domain add`. A recipe release takes the managed snapshot and the VM backup first, holds the host lock, and uses the stack's container runtime. The barako examples in the help text and code comments are now generic. ([#88])
- **A stack can store one verify spec that release, update and rollback all read.** `verify` in `fleet.json` is a list of checks (a path on the stack's own origin with `status`, `minBytes`, `contains`, `absent`, `jsonMatch`, or a `command`) plus attempts, delay and a total timeout. `absent` is judged only after status and size pass, so a placeholder page that answers 200 fails when a marker is missing, and a 502 is reported as a 502. Redirects are not followed and a check cannot name another host. Attempts and time are capped, and a timeout fails with its own message. Unknown keys are refused. `baryovm stack set-verify <name> --file spec.json` stores it (`--clear` removes it) after checking it. A stack with only `healthUrl` verifies as before, and a manifest `verify` still wins for a release. `stack update --auto` accepts a spec in place of a `healthUrl`. ([#89])
- **A stack backs up a list of strategies, each with its dump and its restore.** `backup` in `fleet.json` takes `postgres-container` (today's behaviour), `postgres-remote` (`pg_dump` against a managed database from a throwaway client container, password only from the stack's env file or a password file on the VM, never on a command line), `command` (dump and restore shell lines) and `files` (a tar). A release runs every strategy before anything changes and stops at the first failure. `"backup": []` means nothing to back up and replaces `noDatabase`; `stack update --auto` accepts it and refuses a stack that declared nothing. Existing `dbContainer`, `dbName`, `dbUser` and `noDatabase` still load and behave as before, and `stack add --db-container` now writes the list form. `baryovm stack set-backup <name> --file backup.json` stores and checks a list. `provider-snapshot` and `volume` kinds are not built. ([#90], [#80])
- **Secrets reach a deploy by reference, never on a command line.** One reference type (`env:NAME`, `file:PATH`, `cmd:<command>` for a signed-in CLI such as `op read ...` or `gh auth token`) is resolved on your machine at use time and never stored. `deploy` and `up` gain `--env-ref KEY=<reference>`. `deploy -e KEY=value` still works, but values now travel on the SSH session's stdin into an owner-only (0600) env file that `docker run --env-file` reads and the VM removes afterwards, so they no longer show in `ps` on the VM. Release manifest build `args` are sent the same way (the command carries `--build-arg NAME` only), accept `env:` and `file:` references, and a build can declare BuildKit `secrets`. The project file's `env:` key references use the same type, and `cmd:` is refused in any committed file. A reference that does not resolve stops the command before anything is provisioned or contacted, and no value appears in errors, JSON output or dry-run output. ([#93], [#14], [#84])
- **baryovm can run behind an SSH forced command, and no longer looks like it worked when a forced command swallowed it.** `baryovm vm remote install <vm>` puts a root-owned wrapper on the VM and prints the `authorized_keys` line (it never edits that file). The wrapper, `baryovm remote serve`, reads `baryovm-remote v1 <op> <stack>` with no shell and accepts only `update`, `backup`, `status` and `logs` for stacks listed in the VM's root-owned `/etc/baryovm/remote.json`; it refuses to run if that file or its directory is writable by group or others. `exec` and `release` are not offered. A VM marked `remote: "forced"` (`vm add --remote forced`, `vm remote set <vm> forced`) sends those requests for `stack update`, `backup`, `ps` and `logs` and refuses every other command. For any other VM, baryovm now echoes a random token before sending shell and fails with the cause if it does not come back. ([#98])
- **`baryovm repo move <stack> --to <url>` moves a stack's repository to another Git host.** It prints a plan and does nothing without `--yes`. With `--yes` it checks the destination is empty or identical, exports issues and pull requests with comments to JSON, pushes every branch and tag without force, compares both ref lists and stops if they differ, then repoints `origin` on checkouts found on the registered VMs (a read-only token only through `--read-cred`, sent on stdin) and on the local clone, which keeps the old remote as `old-origin`. Crontab lines and systemd units that run from those checkouts are listed, not edited. The old repository is never deleted or archived; the commands for that are printed last. ([#137])
- **`baryovm git` and `baryovm gh` refuse keys and pushes to the wrong host.** `git commit` scans the staged diff (and the working diff with `-a` or paths), the message and any `-F` message file. `git push` checks every push URL of the remote against `baryovm.allowedHosts` in git config (or `BARYOVM_ALLOWED_HOSTS`) before any network call, then scans every commit not on the remote; deletes skip the commit scan. A policy that cannot be read refuses instead of allowing. `gh pr`, `gh issue` and `gh release` (create, edit, comment, review) scan `--title`, `--body`, `--body-file`, `--notes` and `--notes-file`. The scanner knows Anthropic and OpenAI keys and PGP private key blocks. A refusal names file and line, never the value, and exits 1. Everything else passes through with the tool's own output and exit code. `guard install-hooks` writes pre-commit, commit-msg and pre-push hooks that run the same checks. ([#140])
- **`baryovm publish npm` and `baryovm publish nuget` refuse a package that holds a key or goes to the wrong registry.** `publish npm` reads `npm pack --dry-run --json`, scans every file it would ship, resolves the registry (publishConfig, `--registry`, `.npmrc`, npm default) and checks it against `baryovm.allowedRegistries` in git config (or `BARYOVM_ALLOWED_REGISTRIES`); a `private` package resolving to the public registry is refused with the registry named. `publish nuget` scans every entry of the `.nupkg` and checks `--source` (default nuget.org). Oversized files are refused, not skipped, and an unreadable policy refuses. Otherwise npm and dotnet run with your arguments unchanged. ([#141])
- **`baryovm cloud wrangler`, `baryovm cloud firebase` and `baryovm cloud gcloud` check a deploy before the real CLI runs.** `wrangler deploy` is bundled with `--dry-run --outdir` and the bundle, `wrangler.toml` and `--var` arguments are scanned; a key in `vars` is refused and `wrangler secret put` is named. `firebase deploy` prints the project id, refuses one not in `baryovm.allowedCloudProjects` (or `BARYOVM_ALLOWED_CLOUD_PROJECTS`) before it runs, and scans the files `firebase.json` deploys. `gcloud` passes through but refuses `iam service-accounts keys create` and similar commands whose output file lands inside a git working tree. A target that cannot be determined refuses. ([#142])
- **`baryovm git` and `baryovm gh` apply seven more rules, each off per repository.** Protected branches (no direct or force push to the default branch or listed branches), destructive commands (`gh repo delete`, `gh release delete`, tag delete, `git reset --hard` with uncommitted work, `git clean -fdx` need the name typed back or `--confirm <name>`), files that never go in git (`.env`, key files, service account JSON, oversized files), no AI attribution in commit and PR text, explicit repository visibility, a gate script before push, and an audit log entry with a `target` for each guarded command. Git aliases are expanded and checked, `-c alias.*` and `--config-env` are refused, and `git commit --pathspec-from-file` is refused. Switch a rule off with `git config --add baryovm.rules.off <rule>`. ([#144])

  Review fixes: a push whose destination cannot be told (glob or empty refspec, detached HEAD, remote.<name>.push, push.default=matching) is refused while protected-branches is on. The gate needs a recorded pass for every commit pushed, not only HEAD. Every shell alias (`!...`) is refused unless listed in `baryovm.allowShellAliases`. Audit targets drop credentials even when the URL does not parse.
- **More typed `data` in `-o json`, fields added only.** `stack ps` adds `containers` (name, service,
  state, health, image, ports) read from `docker compose ps --format json`, `stack logs` adds
  `entries` (service, line), `stack backups` adds `entries` (path, file, taken) and `stack update`
  adds `result` with real booleans and arrays beside its existing string fields. ([#11])
- **Docs for three readers.** `docs/indie.md` (a five-minute quickstart), `docs/sme.md` (a server that already runs things: discover, backups, restore drill, updates), `docs/agency.md` (client VMs, project file, what to hand a client) and `docs/troubleshooting.md` (by symptom, with the tool's own error text), linked from the README. None has been run on a fresh VM yet. ([#46])
- **Contributor terms.** `CLA.md` states that contributions ship under MPL-2.0 with the MPL patent grant, contributors keep their copyright, and there is nothing to sign. Every Go file now carries the MPL-2.0 notice, and a test keeps it that way. ([#86])
- **A GitHub Action in this repository.** `uses: BaryoDev/BaryoVM@v0.6.0` installs a release on a Linux
  runner, verifies its archive against `checksums.txt`, and runs the arguments in `command`.
  `version` picks the release and defaults to the latest.

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
- **`-o json` prints exactly one document, always naming the command.** A failing command used to
  print its own failure and then a second one under the action `baryovm`; now the second is dropped
  and a failure raised before the handler wrote anything keeps the command name. A bad flag
  (`vm list --bogus -o json`) answers in JSON instead of styled text on stderr, and `--help` or a
  group with no subcommand puts the help text in `data.usage` instead of bare text on stdout.
  `docs/JSON.md` writes the envelope down. ([#10])
- **`vm provision` and `up --provider` print a plan by default and need `--yes` to create anything.**
  Without `--yes` they show the provider, region, size, ports, the cost where BaryoVM knows it
  (Lightsail's default bundle) and the result of the hostname check, and change nothing. A real
  create needs `--yes` and a passing hostname check: when `--http-from` or `--https-from` limits a
  web port, every hostname named with `--host` must resolve inside the allowed ranges, and a limit
  with no `--host` is refused. This refuses something earlier versions accepted: `vm provision`
  without `--dry-run` used to create the VM at once, so scripts must add `--yes`. README, USAGE.md
  and SECURITY.md now say what the code does. Tested with fakes through the cobra commands, never
  against a real cloud. ([#16], [#106])
- **A release manifest's `localRoot` and `sync` paths are confined to its directory.** Each must resolve, after symlinks, inside the folder holding the manifest, so a manifest from another branch can no longer name `~` and `.ssh` and copy a private key to the VM. Refused at load (so `--dry-run` refuses too), and a `sync` entry that is a symlink pointing out is refused before rsync runs. A manifest that has to sit apart from its source needs `baryovm stack add --release-root <dir>`, set once by the operator and stored on the stack, or passed to `project release --release-root`. A manifest or project file cannot set it. The rsync trailing-slash rule is unchanged. ([#158])
- **Committed secret references are confined.** An `env:` reference in a release manifest or project file must name a variable starting with `BARYOVM_`, and a `file:` reference must be a relative path that stays inside the file's own directory, symlinks included. A manifest from another branch can no longer make a release read `~/.ssh/id_ed25519` or `AWS_SECRET_ACCESS_KEY` and send it to the build. Refused at load, so `--dry-run` reports it. The default project key variable is now `BARYOVM_DEPLOY_KEY_PATH`. References typed on the command line are unchanged.

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

[Unreleased]: https://github.com/BaryoDev/BaryoVM/compare/v0.6.0...HEAD
[0.6.0]: https://github.com/BaryoDev/BaryoVM/compare/v0.5.0...v0.6.0
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
[#10]: https://github.com/BaryoDev/BaryoVM/issues/10
[#11]: https://github.com/BaryoDev/BaryoVM/issues/11
[#14]: https://github.com/BaryoDev/BaryoVM/issues/14
[#16]: https://github.com/BaryoDev/BaryoVM/issues/16
[#18]: https://github.com/BaryoDev/BaryoVM/issues/18
[#26]: https://github.com/BaryoDev/BaryoVM/issues/26
[#27]: https://github.com/BaryoDev/BaryoVM/issues/27
[#28]: https://github.com/BaryoDev/BaryoVM/issues/28
[#29]: https://github.com/BaryoDev/BaryoVM/issues/29
[#35]: https://github.com/BaryoDev/BaryoVM/issues/35
[#38]: https://github.com/BaryoDev/BaryoVM/issues/38
[#39]: https://github.com/BaryoDev/BaryoVM/issues/39
[#40]: https://github.com/BaryoDev/BaryoVM/issues/40
[#41]: https://github.com/BaryoDev/BaryoVM/issues/41
[#43]: https://github.com/BaryoDev/BaryoVM/issues/43
[#46]: https://github.com/BaryoDev/BaryoVM/issues/46
[#47]: https://github.com/BaryoDev/BaryoVM/issues/47
[#51]: https://github.com/BaryoDev/BaryoVM/issues/51
[#52]: https://github.com/BaryoDev/BaryoVM/issues/52
[#80]: https://github.com/BaryoDev/BaryoVM/issues/80
[#81]: https://github.com/BaryoDev/BaryoVM/issues/81
[#84]: https://github.com/BaryoDev/BaryoVM/issues/84
[#86]: https://github.com/BaryoDev/BaryoVM/issues/86
[#88]: https://github.com/BaryoDev/BaryoVM/issues/88
[#89]: https://github.com/BaryoDev/BaryoVM/issues/89
[#90]: https://github.com/BaryoDev/BaryoVM/issues/90
[#91]: https://github.com/BaryoDev/BaryoVM/issues/91
[#93]: https://github.com/BaryoDev/BaryoVM/issues/93
[#98]: https://github.com/BaryoDev/BaryoVM/issues/98
[#137]: https://github.com/BaryoDev/BaryoVM/issues/137
[#140]: https://github.com/BaryoDev/BaryoVM/issues/140
[#141]: https://github.com/BaryoDev/BaryoVM/issues/141
[#142]: https://github.com/BaryoDev/BaryoVM/issues/142
[#144]: https://github.com/BaryoDev/BaryoVM/issues/144
[#158]: https://github.com/BaryoDev/BaryoVM/issues/158
