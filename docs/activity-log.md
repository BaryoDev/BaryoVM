# Activity log

Every command except `vm list`, `stack list`, help, version and completion appends one JSON line to
`$BARYOVM_HOME/activity.jsonl` (default `~/.baryovm/activity.jsonl`). The file is created with mode
`0600` and only ever appended to. Each line goes out in one `write` on an `O_APPEND` file, so two
processes running at once leave two whole lines.

```json
{"ts":"2026-10-20T08:15:02Z","version":"0.4.0","action":"stack release","vm":"oracle","stack":"site","ok":true,"duration_ms":18423,"host":"arnel-mbp"}
```

| Field | Meaning |
| --- | --- |
| `ts` | UTC, RFC 3339 |
| `version` | the BaryoVM version that ran |
| `action` | the command path, for example `stack release` |
| `vm` | the VM, when the command names one (or the stack's VM, if it is registered) |
| `stack` | the stack, when the command names one |
| `ok` | `true` when the command succeeded |
| `duration_ms` | wall time of the command |
| `error` | the error message, on failure only |
| `host` | the operator's machine name |

The field names are stable. `vm`, `stack` and `error` are left out when empty.

Nothing else is recorded. No flag values, env files, tokens, or arguments. `vm exec` records that it
ran and on which VM, never the command, and a failed `vm exec` records only the remote exit status.

## Opt out

Set `BARYOVM_ACTIVITY=off`. If the log cannot be written, BaryoVM prints one warning and the command
keeps its own exit code.

## Growth

The file is append-only and never rotated by BaryoVM. Rotate or delete it whenever you like; the
next command starts a new one.
