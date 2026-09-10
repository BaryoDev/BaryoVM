# Contributing to BaryoVM

BaryoVM builds shell commands and runs them on machines you own, over SSH. A mistake here does
not fail a unit test, it runs on a live host. That shapes everything below.

## Build and test

```sh
go build ./...                 # everything compiles
go vet ./...                   # must be clean
go test -race ./...            # what CI runs
test -z "$(gofmt -l .)"        # must be silent; see below
```

`gofmt -l` lists unformatted files and **exits zero anyway**, so `gofmt -l . && ...` passes on
code it just told you is wrong. Test that its output is empty, or read the list yourself.

All four, before you open a pull request. They are fast, and CI runs them in that order.

No Go toolchain to hand? The four gates run in a container against the checkout:

```sh
docker run --rm -v "$PWD":/w -w /w golang:1.25 sh -c \
  'test -z "$(gofmt -l .)" && go vet ./... && go build ./... && go test -race ./...'
```

## The rule that matters most

**A change to command construction needs a test that reads the generated string.**

Every remote operation ends as text sent to a shell on someone's server. The failure mode is not
a crash, it is a command that looks right and means something else. Real examples from this
repository:

- Elevating a command prefix rather than the whole command left the subcommand outside the quoted
  shell: `sudo -n sh -c '... docker compose' ps -q 'api'`. It reads correctly in a diff.
- `cd <dir> && sudo -n docker compose ...` fails at the `cd`, as the unprivileged user, before
  sudo is ever reached, on exactly the root-owned directory that made sudo necessary.
- A quoting helper that used the shell's escape for a tool that does its own parsing produced a
  string rsync rejected.

None of those would have been caught by testing behaviour through a mock. They were caught by
asserting the exact string. So assert the exact string.

## Prove a fix, do not assert it

For a bug fix, write the test first, or revert your change and watch the test fail before you
put it back. A test that passes with and without the fix proves nothing and looks like coverage,
which is worse than no test at all.

Say so in the pull request: what you reverted, and what the failure said.

## Branches and commits

Branch: `fix/short-description`, `feature/short-description`, `chore/short-description`.

Commit subjects are short, imperative and in plain words: `send rsync to the VM's port`, not
`fix(release): correct port handling`. A body only where the diff does not explain the why. No
attribution trailers, no `Co-Authored-By`, no em dashes anywhere in commits, pull requests or
documentation.

## What a good pull request looks like

- One concern. Two unrelated fixes are two pull requests.
- The smallest change that closes the issue. No refactoring you were not asked for, no new
  abstraction for a single caller.
- Says what it does *not* do, and what you deliberately left alone.
- `-o json` still emits one envelope. That surface is a contract: an MCP server and an app are
  meant to drive the same commands, so a change that only fixes the human-readable path is half
  a fix.
- Updates `USAGE.md` for any new command or flag, and adds a `CHANGELOG.md` entry under
  `[Unreleased]` for anything a user would notice.

## Things worth knowing before you change them

`sudo` is always `sudo -n`. Under `-o json` there is no terminal to answer a password prompt, so
a bare `sudo` hangs until the session times out where `-n` fails immediately and says why.

The tool holds no secrets. It stores SSH key *paths*, never key material, and never reads
application secrets except when a backup copies a stack's `.env` into a backup directory on the
user's own VM. See [SECURITY.md](SECURITY.md).

Rationale comments in this codebase are records of things that went wrong. If you change code
that carries one, read it first, and correct it rather than leaving it if your change makes it
untrue. A comment that no longer describes the code is worse than no comment.

## Reporting a bug

Say what you ran, what happened, and what you expected. For anything involving a remote command,
paste the command BaryoVM generated: `-o json` prints it in most failure envelopes, and it is
usually the whole answer.

Security issues do not go in the issue tracker. See [SECURITY.md](SECURITY.md).
