// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package scan

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

// Edge check names, used in Finding.Check.
const (
	CheckCAA     = "caa"
	CheckSPF     = "spf"
	CheckDMARC   = "dmarc"
	CheckBareIP  = "bare-ip"
	CheckSecTxt  = "security.txt"
	CheckHSTS    = "hsts"
	CheckTimeout = "edge"
)

// Defaults for the edge checks. The total cap covers every lookup and request
// together, so a resolver that never answers cannot hold the run open.
const (
	DefaultEdgeTimeout   = 30 * time.Second
	DefaultEdgeOpTimeout = 5 * time.Second
)

// EdgeOptions configures RunEdge. Resolver and Fetcher are the seams: tests pass
// canned ones, the cli passes NetResolver and HTTPFetcher.
type EdgeOptions struct {
	Resolver Resolver
	Fetcher  Fetcher
	// Timeout caps the whole run. OpTimeout caps each lookup or request.
	Timeout   time.Duration
	OpTimeout time.Duration
	// Now is the clock for the security.txt Expires check.
	Now func() time.Time
}

// RunEdge runs the passive edge checks for the host in targetURL and returns
// their findings. It sends five DNS lookups (CAA, SPF TXT, MX, DMARC TXT, A) and
// three HTTP requests (security.txt, and the bare IP over HTTPS and over plain
// HTTP), nothing else. No zone transfer, no port scan, no guessing of names.
//
// A check that cannot finish inside its time limit is reported once, in an
// "Edge checks timed out" finding, instead of failing the run.
func RunEdge(ctx context.Context, targetURL string, opts EdgeOptions) ([]Finding, error) {
	host, err := ParseTarget(targetURL)
	if err != nil {
		return nil, err
	}
	if opts.Resolver == nil || opts.Fetcher == nil {
		return nil, errors.New("scan: edge checks need a resolver and a fetcher")
	}
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultEdgeTimeout
	}
	if opts.OpTimeout <= 0 {
		opts.OpTimeout = DefaultEdgeOpTimeout
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	total, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	e := &edge{total: total, opts: opts}
	e.run(host)
	if len(e.timedOut) > 0 {
		e.add(CheckTimeout, "Edge checks timed out", SeverityLow, "timed out: "+strings.Join(e.timedOut, ", "))
	}
	return e.out, nil
}

type edge struct {
	total    context.Context
	opts     EdgeOptions
	out      []Finding
	timedOut []string
}

func (e *edge) add(check, name string, sev Severity, detail string) {
	e.out = append(e.out, Finding{
		Name:     cleanText(name),
		Severity: sev.String(),
		Count:    1,
		Check:    check,
		Detail:   cleanText(detail),
		severity: sev,
	})
}

// op runs one lookup or request under its own time limit and inside the total
// cap. It reports whether the operation completed without error. A timeout is
// recorded for the single timeout finding; any other error becomes an
// informational "<label> check failed" finding.
func (e *edge) op(check, label string, f func(ctx context.Context) error) bool {
	if e.total.Err() != nil {
		e.timedOut = append(e.timedOut, label)
		return false
	}
	ctx, cancel := context.WithTimeout(e.total, e.opts.OpTimeout)
	defer cancel()
	err := f(ctx)
	if err == nil {
		return true
	}
	if isTimeout(err) || ctx.Err() != nil {
		e.timedOut = append(e.timedOut, label)
		return false
	}
	e.add(check, label+" check failed", SeverityInformational, err.Error())
	return false
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	var de *net.DNSError
	return errors.As(err, &de) && de.IsTimeout
}

func (e *edge) run(host string) {
	var (
		caa           []CAA
		txt, dmarcTXT []string
		mx            []string
		ips           []string
		caaOK, spfOK  bool
		mxOK, dmarcOK bool
		aOK           bool
	)
	caaOK = e.op(CheckCAA, "CAA", func(ctx context.Context) (err error) {
		caa, err = e.opts.Resolver.LookupCAA(ctx, host)
		return err
	})
	spfOK = e.op(CheckSPF, "SPF", func(ctx context.Context) (err error) {
		txt, err = e.opts.Resolver.LookupTXT(ctx, host)
		return err
	})
	mxOK = e.op(CheckSPF, "MX", func(ctx context.Context) (err error) {
		mx, err = e.opts.Resolver.LookupMX(ctx, host)
		return err
	})
	dmarcOK = e.op(CheckDMARC, "DMARC", func(ctx context.Context) (err error) {
		dmarcTXT, err = e.opts.Resolver.LookupTXT(ctx, "_dmarc."+host)
		return err
	})
	aOK = e.op(CheckBareIP, "A record", func(ctx context.Context) (err error) {
		ips, err = e.opts.Resolver.LookupA(ctx, host)
		return err
	})

	if caaOK {
		e.checkCAA(caa)
	}
	if spfOK {
		e.checkSPF(txt, mx, mxOK)
	}
	if dmarcOK {
		e.checkDMARC(dmarcTXT)
	}
	e.checkSecurityTxt(host)
	if aOK {
		e.checkBareIP(ips)
	}
}

func (e *edge) checkCAA(recs []CAA) {
	var cas []string
	for _, r := range recs {
		if r.Tag == "issue" || r.Tag == "issuewild" {
			v := strings.TrimSpace(strings.SplitN(r.Value, ";", 2)[0])
			if v == "" {
				v = "(none allowed)"
			}
			cas = append(cas, r.Tag+" "+v)
		}
	}
	if len(recs) == 0 {
		e.add(CheckCAA, "CAA record missing", SeverityLow, "no CAA record: any certificate authority may issue for this domain")
		return
	}
	detail := "no issue or issuewild property"
	if len(cas) > 0 {
		detail = strings.Join(cas, ", ")
	}
	e.add(CheckCAA, "CAA record present", SeverityInformational, detail)
}

// records returns the TXT strings that begin with the given version tag, case
// insensitive, as RFC 7208 and RFC 7489 specify.
func records(txt []string, prefix string) []string {
	var out []string
	for _, t := range txt {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(t)), prefix) {
			out = append(out, strings.TrimSpace(t))
		}
	}
	return out
}

func (e *edge) checkSPF(txt, mx []string, mxOK bool) {
	spf := records(txt, "v=spf1")
	switch {
	case len(spf) == 0:
		e.add(CheckSPF, "SPF record missing", SeverityLow, "no TXT record starting v=spf1")
	case len(spf) > 1:
		e.add(CheckSPF, "Multiple SPF records", SeverityLow, strings.Join(spf, " | "))
	default:
		rec := spf[0]
		hasAll := false
		for _, tok := range strings.Fields(strings.ToLower(rec)) {
			if tok == "-all" {
				hasAll = true
			}
		}
		if mxOK && len(mx) == 0 && !hasAll {
			e.add(CheckSPF, "SPF does not end in -all", SeverityLow, rec+" (no MX record, so no mail is expected from this domain)")
			return
		}
		e.add(CheckSPF, "SPF record present", SeverityInformational, rec)
	}
}

func (e *edge) checkDMARC(txt []string) {
	dm := records(txt, "v=dmarc1")
	if len(dm) == 0 {
		e.add(CheckDMARC, "DMARC record missing", SeverityLow, "no TXT record starting v=DMARC1 at _dmarc")
		return
	}
	rec := dm[0]
	policy := ""
	for _, tag := range strings.Split(rec, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(tag), "=")
		if ok && strings.EqualFold(strings.TrimSpace(k), "p") {
			policy = strings.ToLower(strings.TrimSpace(v))
		}
	}
	switch policy {
	case "quarantine", "reject":
		e.add(CheckDMARC, "DMARC policy "+policy, SeverityInformational, rec)
	case "none":
		e.add(CheckDMARC, "DMARC policy none", SeverityLow, rec+" (monitor only, nothing is rejected)")
	default:
		e.add(CheckDMARC, "DMARC record has no valid policy", SeverityLow, rec)
	}
}

const hstsHeader = "Strict-Transport-Security"

func (e *edge) checkSecurityTxt(host string) {
	var resp FetchResponse
	ok := e.op(CheckSecTxt, "security.txt", func(ctx context.Context) (err error) {
		resp, err = e.opts.Fetcher.Fetch(ctx, FetchRequest{URL: "https://" + host + "/.well-known/security.txt"})
		return err
	})
	if !ok {
		return
	}

	// HSTS is read off this response: it is a value to report, not to judge.
	hsts := strings.TrimSpace(resp.Header.Get(hstsHeader))
	if hsts == "" {
		hsts = "not set"
	}
	e.add(CheckHSTS, "HSTS header", SeverityInformational, hsts)

	if resp.Status != 200 {
		e.add(CheckSecTxt, "security.txt missing", SeverityInformational,
			fmt.Sprintf("/.well-known/security.txt answered %d", resp.Status))
		return
	}
	var contact, expires string
	for _, line := range strings.Split(string(resp.Body), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "\xef\xbb\xbf"))
		k, v, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "contact":
			if contact == "" {
				contact = strings.TrimSpace(v)
			}
		case "expires":
			if expires == "" {
				expires = strings.TrimSpace(v)
			}
		}
	}
	problems := 0
	if contact == "" {
		problems++
		e.add(CheckSecTxt, "security.txt has no Contact", SeverityLow, "RFC 9116 requires a Contact field")
	}
	switch {
	case expires == "":
		problems++
		e.add(CheckSecTxt, "security.txt has no Expires", SeverityLow, "RFC 9116 requires an Expires field")
	default:
		t, err := time.Parse(time.RFC3339, expires)
		switch {
		case err != nil:
			problems++
			e.add(CheckSecTxt, "security.txt Expires is not a date", SeverityLow, expires)
		case !t.After(e.opts.Now()):
			problems++
			e.add(CheckSecTxt, "security.txt has expired", SeverityLow, "Expires: "+expires)
		}
	}
	if problems == 0 {
		e.add(CheckSecTxt, "security.txt present", SeverityInformational, "Contact: "+contact+", Expires: "+expires)
	}
}

func (e *edge) checkBareIP(ips []string) {
	if len(ips) == 0 {
		e.add(CheckBareIP, "No A record", SeverityInformational, "bare IP checks skipped")
		return
	}
	ip := ips[0]
	// The A record is attacker-controllable DNS. Probing 127.0.0.1 or the cloud
	// metadata address from the user's machine would be a request forgery, so a
	// non-global address is reported and left alone.
	if !isGlobalIP(net.ParseIP(ip)) {
		e.add(CheckBareIP, "Bare IP checks skipped", SeverityInformational,
			ip+" is not a globally routable address (loopback, private, link-local, shared or unspecified), so it was not probed")
		return
	}
	for _, p := range []struct {
		label, url string
		skip       bool
	}{
		{"HTTPS", "https://" + ip + "/", true},
		{"HTTP", "http://" + ip + "/", false},
	} {
		var resp FetchResponse
		ok := e.op(CheckBareIP, "Bare IP over "+p.label, func(ctx context.Context) (err error) {
			resp, err = e.opts.Fetcher.Fetch(ctx, FetchRequest{URL: p.url, SkipVerify: p.skip})
			return err
		})
		// A refused or reset connection is the good answer: nothing is served.
		if !ok && !e.wasTimeout("Bare IP over "+p.label) {
			e.dropLastFailure()
		}
		if ok && resp.Status >= 200 && resp.Status < 300 {
			e.add(CheckBareIP, "Bare IP serves a site over "+p.label, SeverityMedium,
				fmt.Sprintf("%s answered %d", p.url, resp.Status))
		}
	}
}

func (e *edge) wasTimeout(label string) bool {
	for _, l := range e.timedOut {
		if l == label {
			return true
		}
	}
	return false
}

// dropLastFailure removes the "<label> check failed" finding that op just added
// for a bare-IP request. For the bare-IP probes an error means nothing answered.
func (e *edge) dropLastFailure() {
	if n := len(e.out); n > 0 && strings.HasPrefix(e.out[n-1].Name, "Bare IP over ") &&
		strings.HasSuffix(e.out[n-1].Name, " check failed") {
		e.out = e.out[:n-1]
	}
}
