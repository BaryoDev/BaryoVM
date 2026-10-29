package scan

import (
	"errors"
	"net"
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

func TestIsGlobalIPRefusesEmbeddedAndReservedRanges(t *testing.T) {
	for _, ip := range []string{
		"64:ff9b::a9fe:a9fe", "64:ff9b:1::a9fe:a9fe", "2002:a9fe:a9fe::1", "2002:0a00:0001::1",
		"2001:0:4136:e378:8000:63bf:3fff:fdd2", "2001:db8::1", "fec0::1", "::ffff:127.0.0.1",
		"240.0.0.1", "255.255.255.255", "192.0.2.1", "198.51.100.7", "203.0.113.7", "192.88.99.1",
	} {
		if isGlobalIP(net.ParseIP(ip)) {
			t.Errorf("%s treated as global", ip)
		}
	}
	for _, ip := range []string{"93.184.216.34", "64:ff9b::5db8:d822", "2002:5db8:d822::1", "2606:2800:220:1::1"} {
		if !isGlobalIP(net.ParseIP(ip)) {
			t.Errorf("%s refused, want global", ip)
		}
	}
}

func TestCleanTextDropsBidiAndZeroWidth(t *testing.T) {
	in := "ok\u202Egnp.exe\u200B\u2066x\u2028y" + string(rune(0xFEFF))
	if got, want := cleanText(in), "okgnp.exexy"; got != want {
		t.Errorf("cleanText(%q) = %q, want %q", in, got, want)
	}
}
