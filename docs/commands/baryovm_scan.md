## baryovm scan

Run an OWASP ZAP baseline (passive) scan against a URL

### Synopsis

Runs the OWASP ZAP baseline scan (passive: no attack payloads) against a
deployed site and reports findings worst-first. With --fail-on, it exits
non-zero when a finding is at or above that severity, so it can gate a
release in a pipeline. Needs Docker on the machine that runs it.

It also runs passive edge checks on the host: CAA, SPF and DMARC records,
whether the bare IP serves a site over HTTPS (no SNI) or plain HTTP,
security.txt (RFC 9116), and the HSTS header as a value. They send five DNS
lookups and three HTTP requests, and their findings join the ZAP findings.
The URL must use a DNS name (no user@, no IP address, IDN as punycode).

Scan only targets you are authorised to reach.

```
baryovm scan --url <target> [flags]
```

### Options

```
      --fail-on string    exit non-zero when a finding is at or above this severity: high | medium | low | informational (default "high")
  -h, --help              help for scan
      --resolver string   DNS server for the edge checks, an IP with optional port (default: the system resolver)
      --timeout int       scan timeout in seconds (default 600)
      --url string        target URL to scan (required)
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm](baryovm.md)	 - Provision VMs and deploy apps to your own boxes
