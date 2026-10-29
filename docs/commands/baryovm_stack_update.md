## baryovm stack update

Pull newer images and recreate, only if they come up healthy

### Synopsis

Pulls the stack's images and, when any actually changed, backs up the database,
recreates the affected services, and waits for the stack's health URL. If it does not
come back, the previous images are restored and the stack is checked again.

An unchanged stack is left alone: no recreate, no backup, no downtime.

--auto is the form a scheduler runs. It refuses any stack not marked autoUpdate, any stack
with no healthUrl, and any stack with no database backup configured, since an unattended
update that cannot tell a healthy start from a crash loop is worse than no update at all.
A stack that genuinely has no database says so with `stack set-update --no-database`.
--dry-run is exempt from both backup refusals: it recreates nothing, so it has nothing
to go back from.

--notify (or BARYOVM_NOTIFY_URL) posts the JSON result envelope to a webhook when an --auto
run fails or rolls back, so a 3am failure reaches a person. https only (http for localhost).
The URL usually holds a token, so only its scheme and host are ever printed.

```
baryovm stack update <name> [flags]
```

### Examples

```
  baryovm stack update playground --dry-run
  baryovm stack update playground
  baryovm stack update playground --auto    # what cron runs
  baryovm stack update playground --auto --notify "$WEBHOOK_URL" -o json
```

### Options

```
      --auto                    unattended: refuse stacks not marked autoUpdate
      --dry-run                 report what would update, change nothing
      --health-attempts int     health checks before declaring failure (default 20)
      --health-delay duration   wait between health checks (default 3s)
  -h, --help                    help for update
      --no-backup               skip the pre-update database backup
      --notify string           with --auto, POST the result envelope to this webhook on failure (or set BARYOVM_NOTIFY_URL)
      --notify-on string        when to notify: failure or always (default "failure")
      --service strings         limit to these services (defaults to the stack's updateServices)
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm stack](baryovm_stack.md)	 - Manage docker compose stacks on your VMs
