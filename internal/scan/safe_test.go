package scan

import (
	"errors"
	"strings"
	"testing"
)

func TestEdgeSkipsNonGlobalARecord(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "100.64.0.1", "0.0.0.0", "172.16.5.5", "192.168.1.1"} {
		res := &fakeResolver{a: map[string][]string{"example.com": {ip}}}
		f := &fakeFetcher{}
		got := runEdge(t, res, f)
		want(t, got, "Bare IP checks skipped", SeverityInformational, ip)
		for _, c := range f.calls {
			if strings.Contains(c.URL, ip) {
				t.Errorf("%s: sent a request to %s", ip, c.URL)
			}
		}
		if len(f.calls) != 1 || f.calls[0].URL != "https://example.com/.well-known/security.txt" {
			t.Errorf("%s: want only the security.txt request, got %v", ip, f.calls)
		}
	}
}

func TestGuardDialRefusesNonGlobal(t *testing.T) {
	for _, addr := range []string{"10.0.0.1:80", "127.0.0.1:443", "169.254.169.254:80", "[::1]:443", "[fd00::1]:80", "100.64.0.9:80"} {
		if err := guardDial("tcp", addr, nil); !errors.Is(err, errNonGlobal) {
			t.Errorf("%s: err = %v, want errNonGlobal", addr, err)
		}
	}
	for _, addr := range []string{"93.184.216.34:443", "[2606:2800:220:1::1]:443"} {
		if err := guardDial("tcp", addr, nil); err != nil {
			t.Errorf("%s: err = %v, want nil", addr, err)
		}
	}
}

func TestEdgeCleansNetworkText(t *testing.T) {
	long := strings.Repeat("a", 1000)
	res := &fakeResolver{txt: map[string][]string{
		"example.com": {"v=spf1 \x1b[2J\nevil " + long + " -all"},
	}}
	got := runEdge(t, res, &fakeFetcher{})
	for _, f := range got {
		for _, r := range f.Name + f.Detail {
			if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
				t.Errorf("%q kept control character %U", f.Name, r)
			}
		}
		if n := len([]rune(f.Detail)); n > maxTextRunes+3 {
			t.Errorf("%q detail is %d runes, want at most %d", f.Name, n, maxTextRunes+3)
		}
	}
	if s := cleanText("a\x1b[2J\nb\u009bc"); s != "a[2Jbc" {
		t.Errorf("cleanText = %q", s)
	}
	if n := len([]rune(cleanText(long))); n != maxTextRunes+3 {
		t.Errorf("capped length = %d", n)
	}
}
