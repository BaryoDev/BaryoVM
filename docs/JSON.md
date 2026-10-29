# JSON output

Every command takes `-o json` (or `-ojson`, `--output=json`) and writes exactly one JSON document
to stdout. Progress and warnings go to stderr, or nowhere in JSON mode.

## Envelope

```json
{ "ok": true, "action": "stack ps", "message": "app", "data": {}, "error": "" }
```

- `ok`: false when the command failed. The exit status is non-zero then too.
- `action`: the command path without the program name (`stack ps`, `vm exec`). A failure keeps the
  action of the command that failed, including flag parse errors. `baryovm` only appears when the
  failure is not tied to a command, such as an unknown top-level command.
- `message`: a short human line, only on some successes.
- `data`: the payload, shaped per command below. Omitted when a command has none.
- `error`: the reason, only when `ok` is false.

One document per run, whatever fails: a handler's own failure document wins, and the fallback in
`cli.Execute` writes one only when the handler wrote none. Strings carry remote output as valid
JSON strings, so bytes that are not UTF-8, or a stray `{`, stay inside the string.

`--help` and a group run with no subcommand under `-o json` give `data.usage` with the help text.

Fields are only ever added. Read the ones you need and ignore the rest.

## Payloads

| Command | `data` |
| --- | --- |
| `version` | a map of strings |
| `vm list`, `stack list` | array of the registered VMs or stacks |
| `vm add`, `vm provision`, `stack add` | the VM or stack record |
| `vm ping` | a map of strings |
| `vm exec` | `{command, stdout, stderr, exitCode}` |
| `vm bootstrap`, `deploy`, `up` | report objects |
| `vm health` | one report per VM: `{vm, verdict, checks[]}` |
| `doctor` | array of `{name, present, detail, hint, optional}`; `ok` is false when a required one is missing |
| `stack ps` | `{output, containers[]}`. `containers` is `{name, service, state, health, image, ports[]}` with `ports` as `{host, published, target, protocol}`. Left out when compose cannot print JSON. |
| `stack logs` | `{output, lines, state, note, entries[]}`. `entries` is `{service, line}`, with `service` the label compose prints before the bar (the container name), empty for a line with none. |
| `stack backups` | `{output, backups[], count, note, entries[]}`. `entries` is `{path, file, taken}`; `taken` comes from the file name, in the VM's clock, no zone. |
| `stack update` | `{updated, rolledBack, services, checked, health, skipped}` as strings (the old shape), plus `result`, the same outcome with real booleans and arrays. |
| `stack pull`, `stack deploy`, `stack backup`, `stack restore` | `{output}`, the text compose or the script printed |
| `stack release` | `{transport, commit, verify{source, checks, attempts}}`; on failure, the release state |
| `stack release --dry-run`, other `--dry-run` | the plan: `{target, steps[], commands[], notes[]}` |

Not yet typed: `stack pull` and `stack deploy` still return the raw output only, and
`stack backups` has no size per file.
