## baryovm scan

Run an OWASP ZAP baseline (passive) scan against a URL

### Synopsis

Runs the OWASP ZAP baseline scan (passive: no attack payloads) against a
deployed site and reports findings worst-first. With --fail-on, it exits
non-zero when a finding is at or above that severity, so it can gate a
release in a pipeline. Needs Docker on the machine that runs it.

Scan only targets you are authorised to reach.

```
baryovm scan --url <target> [flags]
```

### Options

```
      --fail-on string   exit non-zero when a finding is at or above this severity: high | medium | low | informational (default "high")
  -h, --help             help for scan
      --timeout int      scan timeout in seconds (default 600)
      --url string       target URL to scan (required)
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm](baryovm.md)	 - Provision VMs and deploy apps to your own boxes
