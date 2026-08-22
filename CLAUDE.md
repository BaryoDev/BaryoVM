# CLAUDE.md

Guidance for AI assistants (and new contributors) working in this repository.

## What BaryoVM is

A single-binary Go CLI that registers Linux VMs you already own and drives the
Docker Compose stacks on them **over plain SSH**, agentless. It gives you the
day-to-day parts of a PaaS (deploy, release, update, backup, restore, logs)
with no control plane to host.

Two rules shape almost every design decision here:

1. **The CLI is the single source of truth.** Every command supports `-o json`
   and emits one stable envelope, because the planned MAUI app and MCP server
   are meant to drive this same surface. Never add a capability that is only
   reachable through human-formatted output.
2. **The tool holds no secrets.** It stores SSH key *paths*, never key
   material, and never reads or copies app secrets except when a backup copies
   a stack's `.env` into a backup dir on the user's own VM. See `SECURITY.md`.

Status: early-stage. The SSH, compose, release, update and backup/restore paths
are used daily. Lightsail provisioning is billable and effectively experimental.

## Commands

```sh
go build ./...          # build everything
go vet ./...            # must be clean
go test -race ./...     # what CI runs
gofmt -l .              # must print nothing; CI fails on any output
go run ./cmd/baryovm --help

# build the binary
go build -o /tmp/baryovm ./cmd/baryovm

# release dry run (needs goreleaser locally)
goreleaser release --snapshot --clean
```

CI (`.github/workflows/ci.yml`) runs, in order: gofmt check, `go vet`,
`go build`, `go test -race`. Run all four before pushing; they are cheap.

Tagging `vX.Y.Z` triggers `.github/workflows/release.yml`, which runs GoReleaser
(`.goreleaser.yaml`) to publish linux/darwin amd64+arm64 archives. The version
is injected via ldflags into `internal/cli.Version`, so do not hardcode it
anywhere else.

## Layout

```
cmd/baryovm/main.go        five lines; calls cli.Execute()
internal/cli/              cobra command surface (one file per command area)
internal/sshx/             the SSH engine everything remote goes through
internal/fleet/            local inventory, ~/.baryovm/fleet.json
internal/ui/               terminal styling, spinners, and the JSON envelope
internal/bootstrap/        install Docker on a VM, idempotently
internal/deploy/           run a single container over SSH
internal/compose/          drive `docker compose` on a VM
internal/release/          config-driven rsync + remote build + compose up
internal/update/           health-gated update with rollback
internal/backup/           pg_dump + config backup/restore over SSH
internal/toolchain/        install missing local CLIs instead of erroring
internal/provider/         IVmProvider seam
internal/provider/lightsail/  AWS Lightsail implementation
```

The dependency direction is strictly one way:

```
cmd -> internal/cli -> engine packages (compose, release, update, backup,
                       deploy, bootstrap) -> internal/sshx
                    -> internal/fleet (state)
                    -> internal/ui (output)
```

Engine packages never import `internal/cli`, and never print. They return
strings and errors; the cli layer decides how to render them. Keep it that way:
it is what makes the engine testable without a host.

## The conventions that matter

### Every command emits `ui.Result`

```go
ui.Emit(ui.Result{OK: true, Action: "stack release", Message: "app released", Data: st})
```

`Action` is the command path as a human would type it (`vm ping`, `stack
backup`). In JSON mode the envelope goes to **stdout**; all human chrome
(titles, details, spinners, success lines) goes to **stderr** and is suppressed
entirely. A new command is not finished until it emits a result on both the
success and the failure path.

Long operations wrap in `ui.Step("doing the thing", func() error { ... })`,
which shows a spinner on a TTY, runs silently in JSON mode, and turns into a
`✓`/`✗` line afterwards.

### Everything remote goes through `sshx`

Dial with `sshx.Dial(vm.Target())`, run with `c.Run(cmd)`, and **single-quote
every interpolated value with `sshx.Quote`**. This is the injection boundary and
it is covered by tests in `internal/sshx/sshx_test.go`. A remote command built
with `fmt.Sprintf` and a bare `%s` is a bug even when the value looks safe.

Known gap, deliberately marked: `sshx.Dial` uses `ssh.InsecureIgnoreHostKey()`
with a `TODO(security)` for a TOFU known_hosts store. Do not quietly remove the
TODO; if you fix it, fix it properly.

### Command builders are separate from execution

`Stack.PullCmd`, `Stack.ConfigCmd`, `Manifest.BuildCmd`, `Manifest.RsyncCmd`,
`Config.bkVar` and friends build a command string (or `*exec.Cmd`) and return
it. That is why most of this repo can be tested with no SSH connection and no
Docker daemon. When adding remote behaviour, build the command in a pure
function and keep `c.Run` at the edge.

The same idea at a larger scale is `update.Runner`: the decision logic in
`update.Run` (what is stale, back up first, roll back on failure) talks to an
interface, and `update.SSHRunner` is the real implementation. Tests drive it
with `fakeRunner`, asserting on the *order* of calls.

### Sudo is `sudo -n`, always

Several stacks keep a root-owned `.env` (correct, it holds a DB password), so
compose, docker and rsync may all need elevation. Three separate places carry a
`Sudo` flag: `fleet.Stack.Sudo` (persisted), `compose.Stack.Sudo` /
`backup.Config.Sudo` (per-operation), and `release.Manifest.Sudo` (the *remote*
rsync, via `--rsync-path=sudo -n rsync`).

The `-n` is not optional. Without it a host that wants a password writes the
prompt into rsync's data channel and corrupts the protocol stream, so the
transfer hangs or dies opaquely instead of saying what is wrong.

Never "solve" a permissions problem by loosening a secrets file.

### Safety defaults are the product

Do not weaken these without a very good reason:

- `stack release` and `stack update` back up the database **before** touching
  anything (a backup taken after a start-time migration already contains it).
- `stack restore` refuses without `--yes`.
- `stack update --auto` refuses a stack without `autoUpdate`, without a
  `healthUrl`, or combined with `--no-backup`. An unattended update that cannot
  tell a healthy start from a crash loop is worse than no update.
- `autoUpdate` defaults to false, and `stack set-update --auto` is a separate,
  deliberate command rather than a flag on `stack add`.
- An unchanged stack is never recreated, so a nightly job is not a nightly
  restart.
- Billable provisioning (`vm provision`, `up`) is expected to be exercised with
  `--dry-run`.
- The compose dir (with its `.env`) is never in a release manifest's `sync`
  list, so `--delete` cannot wipe secrets.

### Comments explain why, not what

This codebase is unusually heavy on rationale comments, and that is on purpose:
most of them record an incident. Read them before changing the code they sit
on, and when you fix something found against a real host, leave the same kind
of note. Examples worth reading before touching the relevant area:

- `internal/compose/compose.go` on why image references come from `compose
  config` and not `ps` (a digest-resolved tag reports as a bare `sha256`, which
  is useless as a rollback target since `docker tag` needs a name).
- `internal/compose/compose.go` on `PullUpdatable` and
  `--ignore-pull-failures` (stacks that mix registry images with locally built
  ones would otherwise be permanently un-updatable).
- `internal/release/release.go` on rsync's trailing-slash rule (`"out"` copies
  the directory, `"out/"` copies its contents; getting it wrong plus `--delete`
  empties a webroot).
- `internal/update/update.go` on the whole shape of the update, which exists
  because a CMS release crash-looped on start.

### Writing style

- **No em dashes anywhere** in code, comments, docs, CLI strings or commit
  messages. Commit `0c35d6e` removed 130 of them; do not reintroduce any.
  Rewrite the sentence, or use a colon, which is the convention Go error
  strings already follow.
- Error strings are lowercase and say what to do next, e.g.
  `no VM named %q: register it with \`baryovm vm add %s ...\``.
- Commit messages are plain sentences in the imperative or descriptive mood
  (`stack update: pull, verify, and roll back if it does not come up`), with a
  body explaining the reasoning. No conventional-commit prefixes.

### Licence headers

Non-test source files carry the three-line MPL-2.0 notice at the top, above the
package doc comment. Include it in any new file. A few recent files
(`internal/update/update.go`, `internal/cli/stack_update.go`) are missing it;
adding it while you are in there is welcome.

## State

`~/.baryovm/fleet.json`, overridable with `BARYOVM_HOME` (which is how the tests
isolate themselves: `t.Setenv("BARYOVM_HOME", t.TempDir())`). Written `0600`,
with the directory `0700`. It holds `VMs` and `Stacks`; see `internal/fleet`.

Adding a field to `fleet.Stack` means three edits, and the middle one is easy to
miss: the struct tag, the flag on the relevant cobra command, **and copying the
flag variable into the struct literal**. A flag bound but never copied compiles
and vets clean because the variable *is* used, by the binding. This happened
with `--sudo`; `TestStackSettingsSurviveARoundTrip` in `internal/fleet` exists
to catch it, so extend that test with any new behaviour-changing field.

## Common tasks

**Add a CLI command.** Write `newXxxCmd() *cobra.Command` in the matching
`internal/cli/*.go` file (or a new one for a new area), register it in
`newRoot()` or the parent group, wrap the work in `ui.Step`, and emit a
`ui.Result` on both paths. Use the existing helpers: `requireVM`,
`runStackOp`, `runStackBackup`, `backupConfig`.

**Add remote behaviour.** Put it in the engine package that owns the concept,
as a command-building function plus a thin runner. Add a test that asserts the
generated command string.

**Add a cloud provider.** Implement `provider.Provider` in
`internal/provider/<name>/`, and wire it into the switch in `newProvider`
(`internal/cli/provision.go`). Keep heavy cloud SDKs out of the core packages.
Cloud auth uses the in-process Go SDKs reading `~/.aws` / `~/.oci`; do not shell
out to a cloud CLI.

## Docs to keep in sync

- `README.md`, the overview and the command table.
- `USAGE.md`, the full command reference. **Any new command or flag belongs
  here.**
- `SECURITY.md`, if you change what is stored or how remote commands are built.
- `docs/VISION.md` is a **historical planning record**, not a spec. It
  describes a .NET control plane, an MCP server and a MAUI app that do not
  exist. Do not implement from it, and do not "fix" it to match reality; its
  banner already says so.

## Branch and PR workflow

Work on a feature branch and push with `git push -u origin <branch>`. Do not
open a PR unless asked. `.github/workflows/self-assign.yml` lets contributors
claim an issue by commenting `/take`, and answers prose requests with a hint;
`flag-solicitation.yml` labels comments that read like a price quote. Neither
blocks anything.
