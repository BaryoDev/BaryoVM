package scan

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

// fakeResolver answers from canned maps and records every lookup.
type fakeResolver struct {
	txt   map[string][]string
	mx    map[string][]string
	a     map[string][]string
	caa   map[string][]CAA
	calls []string
	err   error
}

func (f *fakeResolver) LookupTXT(_ context.Context, n string) ([]string, error) {
	f.calls = append(f.calls, "TXT "+n)
	return f.txt[n], f.err
}
func (f *fakeResolver) LookupMX(_ context.Context, n string) ([]string, error) {
	f.calls = append(f.calls, "MX "+n)
	return f.mx[n], f.err
}
func (f *fakeResolver) LookupA(_ context.Context, n string) ([]string, error) {
	f.calls = append(f.calls, "A "+n)
	return f.a[n], f.err
}
func (f *fakeResolver) LookupCAA(_ context.Context, n string) ([]CAA, error) {
	f.calls = append(f.calls, "CAA "+n)
	return f.caa[n], f.err
}

// fakeFetcher answers from canned replies keyed by URL and records every request.
type fakeFetcher struct {
	resp  map[string]FetchResponse
	errs  map[string]error
	calls []FetchRequest
}

func (f *fakeFetcher) Fetch(_ context.Context, r FetchRequest) (FetchResponse, error) {
	f.calls = append(f.calls, r)
	if err := f.errs[r.URL]; err != nil {
		return FetchResponse{}, err
	}
	if resp, ok := f.resp[r.URL]; ok {
		return resp, nil
	}
	return FetchResponse{Status: 404, Header: http.Header{}}, nil
}

var fixedNow = time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)

func runEdge(t *testing.T, res Resolver, f Fetcher) map[string]Finding {
	t.Helper()
	fs, err := RunEdge(context.Background(), "https://example.com/", EdgeOptions{Resolver: res, Fetcher: f, Now: func() time.Time { return fixedNow }})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]Finding{}
	for _, f := range fs {
		out[f.Name] = f
	}
	return out
}

func want(t *testing.T, got map[string]Finding, name string, sev Severity, detailHas string) {
	t.Helper()
	f, ok := got[name]
	if !ok {
		t.Fatalf("missing finding %q, got %v", name, keys(got))
	}
	if f.severity != sev || f.Severity != sev.String() {
		t.Errorf("%q severity = %s, want %s", name, f.Severity, sev)
	}
	if !strings.Contains(f.Detail, detailHas) {
		t.Errorf("%q detail = %q, want it to contain %q", name, f.Detail, detailHas)
	}
}

func keys(m map[string]Finding) []string {
	var k []string
	for n := range m {
		k = append(k, n)
	}
	return k
}

func TestEdgeDomainWithNoRecords(t *testing.T) {
	got := runEdge(t, &fakeResolver{}, &fakeFetcher{})
	want(t, got, "CAA record missing", SeverityLow, "any certificate authority")
	want(t, got, "SPF record missing", SeverityLow, "v=spf1")
	want(t, got, "DMARC record missing", SeverityLow, "_dmarc")
	want(t, got, "No A record", SeverityInformational, "skipped")
	want(t, got, "security.txt missing", SeverityInformational, "404")
	want(t, got, "HSTS header", SeverityInformational, "not set")
	if len(got) != 6 {
		t.Errorf("findings = %v", keys(got))
	}
}

func TestEdgeCAAPresent(t *testing.T) {
	res := &fakeResolver{caa: map[string][]CAA{"example.com": {
		{Tag: "issue", Value: "letsencrypt.org"},
		{Tag: "issuewild", Value: ";"},
		{Tag: "iodef", Value: "mailto:a@example.com"},
	}}}
	got := runEdge(t, res, &fakeFetcher{})
	want(t, got, "CAA record present", SeverityInformational, "issue letsencrypt.org")
	if d := got["CAA record present"].Detail; !strings.Contains(d, "issuewild (none allowed)") {
		t.Errorf("detail = %q", d)
	}
}

func TestEdgeSPF(t *testing.T) {
	cases := []struct {
		name string
		txt  []string
		mx   []string
		want string
		sev  Severity
	}{
		{"hard fail no mx", []string{"v=spf1 -all"}, nil, "SPF record present", SeverityInformational},
		{"soft fail no mx", []string{"V=SPF1 ~all"}, nil, "SPF does not end in -all", SeverityLow},
		{"soft fail with mx", []string{"v=spf1 mx ~all"}, []string{"mail.example.com."}, "SPF record present", SeverityInformational},
		{"two records", []string{"v=spf1 -all", "v=spf1 ~all"}, nil, "Multiple SPF records", SeverityLow},
		{"unrelated txt", []string{"google-site-verification=x"}, nil, "SPF record missing", SeverityLow},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := &fakeResolver{txt: map[string][]string{"example.com": c.txt}, mx: map[string][]string{"example.com": c.mx}}
			want(t, runEdge(t, res, &fakeFetcher{}), c.want, c.sev, "")
		})
	}
}

func TestEdgeDMARC(t *testing.T) {
	cases := []struct {
		rec  string
		want string
		sev  Severity
	}{
		{"v=DMARC1; p=reject; rua=mailto:a@example.com", "DMARC policy reject", SeverityInformational},
		{"v=DMARC1; p=quarantine", "DMARC policy quarantine", SeverityInformational},
		{"v=DMARC1; p=none", "DMARC policy none", SeverityLow},
		{"v=DMARC1; rua=mailto:a@example.com", "DMARC record has no valid policy", SeverityLow},
	}
	for _, c := range cases {
		t.Run(c.want, func(t *testing.T) {
			res := &fakeResolver{txt: map[string][]string{"_dmarc.example.com": {c.rec}}}
			want(t, runEdge(t, res, &fakeFetcher{}), c.want, c.sev, c.rec)
		})
	}
}

func secTxt(body string) *fakeFetcher {
	return &fakeFetcher{resp: map[string]FetchResponse{
		"https://example.com/.well-known/security.txt": {
			Status: 200,
			Header: http.Header{"Strict-Transport-Security": {"max-age=31536000; includeSubDomains; preload"}},
			Body:   []byte(body),
		},
	}}
}

func TestEdgeSecurityTxtAndHSTS(t *testing.T) {
	good := "Contact: mailto:sec@example.com\nExpires: 2027-01-01T00:00:00Z\n"
	got := runEdge(t, &fakeResolver{}, secTxt(good))
	want(t, got, "security.txt present", SeverityInformational, "mailto:sec@example.com")
	// HSTS is a value, reported verbatim and never judged.
	want(t, got, "HSTS header", SeverityInformational, "max-age=31536000; includeSubDomains; preload")

	cases := []struct{ name, body, want string }{
		{"no contact", "Expires: 2027-01-01T00:00:00Z\n", "security.txt has no Contact"},
		{"no expires", "Contact: mailto:a@example.com\n", "security.txt has no Expires"},
		{"expired", "Contact: mailto:a@example.com\nExpires: 2026-01-01T00:00:00Z\n", "security.txt has expired"},
		{"bad date", "Contact: mailto:a@example.com\nExpires: soon\n", "security.txt Expires is not a date"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := runEdge(t, &fakeResolver{}, secTxt(c.body))
			want(t, got, c.want, SeverityLow, "")
			if _, ok := got["security.txt present"]; ok {
				t.Error("a broken security.txt must not also be reported present")
			}
		})
	}
}

func TestEdgeBareIP(t *testing.T) {
	res := &fakeResolver{a: map[string][]string{"example.com": {"93.184.216.34", "93.184.216.35"}}}
	f := &fakeFetcher{resp: map[string]FetchResponse{
		"https://93.184.216.34/": {Status: 200},
		"http://93.184.216.34/":  {Status: 301},
	}}
	got := runEdge(t, res, f)
	want(t, got, "Bare IP serves a site over HTTPS", SeverityMedium, "200")
	if _, ok := got["Bare IP serves a site over HTTP"]; ok {
		t.Error("a redirect is not a served site")
	}

	// Connection refused is the good answer and produces no finding.
	f = &fakeFetcher{errs: map[string]error{
		"https://93.184.216.34/": errors.New("connection refused"),
		"http://93.184.216.34/":  errors.New("connection refused"),
	}}
	got = runEdge(t, res, f)
	for n := range got {
		if strings.HasPrefix(n, "Bare IP") {
			t.Errorf("unexpected finding %q", n)
		}
	}
}

func TestEdgeRequestBudget(t *testing.T) {
	res := &fakeResolver{a: map[string][]string{"example.com": {"93.184.216.34"}}}
	f := &fakeFetcher{}
	runEdge(t, res, f)

	wantLookups := []string{"CAA example.com", "TXT example.com", "MX example.com", "TXT _dmarc.example.com", "A example.com"}
	if strings.Join(res.calls, ",") != strings.Join(wantLookups, ",") {
		t.Errorf("lookups = %v, want %v", res.calls, wantLookups)
	}
	wantURLs := []string{"https://example.com/.well-known/security.txt", "https://93.184.216.34/", "http://93.184.216.34/"}
	var gotURLs []string
	for _, c := range f.calls {
		gotURLs = append(gotURLs, c.URL)
	}
	if strings.Join(gotURLs, ",") != strings.Join(wantURLs, ",") {
		t.Errorf("requests = %v, want %v", gotURLs, wantURLs)
	}
	for _, c := range f.calls {
		if c.SkipVerify != (c.URL == "https://93.184.216.34/") {
			t.Errorf("SkipVerify for %s = %v", c.URL, c.SkipVerify)
		}
	}
}

// hangResolver never answers until its context ends.
type hangResolver struct{}

func (hangResolver) LookupTXT(ctx context.Context, _ string) ([]string, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (hangResolver) LookupMX(ctx context.Context, _ string) ([]string, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (hangResolver) LookupA(ctx context.Context, _ string) ([]string, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (hangResolver) LookupCAA(ctx context.Context, _ string) ([]CAA, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestEdgeNeverAnsweringResolverFinishesWithinCap(t *testing.T) {
	// Five lookups at 100ms each would be 500ms; the 300ms cap must win.
	start := time.Now()
	fs, err := RunEdge(context.Background(), "https://example.com", EdgeOptions{
		Resolver:  hangResolver{},
		Fetcher:   &fakeFetcher{},
		Timeout:   300 * time.Millisecond,
		OpTimeout: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if el := time.Since(start); el > 1500*time.Millisecond {
		t.Fatalf("took %s, cap was 300ms", el)
	}
	var found *Finding
	for i := range fs {
		if fs[i].Name == "Edge checks timed out" {
			found = &fs[i]
		}
		if strings.HasSuffix(fs[i].Name, "check failed") {
			t.Errorf("a timeout must not be reported as a failure: %q", fs[i].Name)
		}
	}
	if found == nil {
		t.Fatalf("no timeout finding in %v", fs)
	}
	if found.severity != SeverityLow || found.Check != CheckTimeout {
		t.Errorf("timeout finding = %+v", found)
	}
	for _, l := range []string{"CAA", "SPF", "MX", "DMARC", "A record"} {
		if !strings.Contains(found.Detail, l) {
			t.Errorf("detail %q misses %s", found.Detail, l)
		}
	}
}

func TestEdgeLookupErrorIsAFinding(t *testing.T) {
	fs, err := RunEdge(context.Background(), "https://example.com", EdgeOptions{
		Resolver: &fakeResolver{err: errors.New("server failure")},
		Fetcher:  &fakeFetcher{},
	})
	if err != nil {
		t.Fatal(err)
	}
	var n int
	for _, f := range fs {
		if strings.HasSuffix(f.Name, " check failed") {
			n++
			if f.Detail != "server failure" || f.severity != SeverityInformational {
				t.Errorf("bad failure finding %+v", f)
			}
		}
	}
	if n != 5 {
		t.Errorf("failure findings = %d, want 5", n)
	}
}

func TestRunEdgeRejectsBadTarget(t *testing.T) {
	res := &fakeResolver{}
	f := &fakeFetcher{}
	if _, err := RunEdge(context.Background(), "https://a@b.com/", EdgeOptions{Resolver: res, Fetcher: f}); err == nil {
		t.Fatal("want error")
	}
	if len(res.calls) != 0 || len(f.calls) != 0 {
		t.Error("a rejected target must send nothing")
	}
	if _, err := RunEdge(context.Background(), "https://example.com", EdgeOptions{}); err == nil {
		t.Error("want error without seams")
	}
}

// CAA is inherited down the tree (RFC 8659) and DMARC falls back to the organisational domain
// (RFC 7489), so a subdomain with no record of its own is not unprotected.
func TestEdgeCAAAndDMARCClimbToTheParentDomain(t *testing.T) {
	res := &fakeResolver{
		caa: map[string][]CAA{"example.com": {{Tag: "issue", Value: "letsencrypt.org"}}},
		txt: map[string][]string{"_dmarc.example.com": {"v=DMARC1; p=reject"}},
	}
	fs, err := RunEdge(context.Background(), "https://www.shop.example.com/", EdgeOptions{Resolver: res, Fetcher: &fakeFetcher{}, Now: func() time.Time { return fixedNow }})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Finding{}
	for _, f := range fs {
		got[f.Name] = f
	}
	want(t, got, "CAA record present", SeverityInformational, "letsencrypt.org")
	want(t, got, "DMARC policy reject", SeverityInformational, "")
	for _, c := range res.calls {
		if c == "CAA com" || c == "TXT _dmarc.com" {
			t.Errorf("climbed above two labels: %s", c)
		}
	}
	// SPF stays on the exact host.
	for _, c := range res.calls {
		if strings.HasPrefix(c, "TXT ") && c != "TXT www.shop.example.com" && !strings.HasPrefix(c, "TXT _dmarc.") {
			t.Errorf("unexpected TXT lookup: %s", c)
		}
	}
}
