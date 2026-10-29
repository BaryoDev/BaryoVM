package scan

import "testing"

func TestParseTargetAccepts(t *testing.T) {
	cases := map[string]string{
		"https://example.com":              "example.com",
		"http://Example.COM/":              "example.com",
		"https://example.com./path?q=1#f":  "example.com",
		"https://app.example.co.uk:8443/x": "app.example.co.uk",
		"https://xn--mnchen-3ya.de/":       "xn--mnchen-3ya.de",
		"https://my_host.example.com/":     "my_host.example.com",
	}
	for in, want := range cases {
		got, err := ParseTarget(in)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("%q: host = %q, want %q", in, got, want)
		}
	}
}

func TestParseTargetRejects(t *testing.T) {
	cases := map[string]string{
		"userinfo":          "https://a@b/",
		"userinfo and dot":  "https://example.com@evil.com/",
		"userinfo password": "https://user:pw@example.com/",
		"bare ipv4":         "https://192.0.2.1/",
		"bare ipv4 http":    "http://192.0.2.1:8080/",
		"bare ipv6":         "https://[2001:db8::1]/",
		"idn":               "https://münchen.de/",
		"idn cyrillic":      "https://аpple.com/",
		"backslash":         `https://example.com\@evil.com/`,
		"percent host":      "https://exa%6dple.com/",
		"ftp scheme":        "ftp://example.com/",
		"no scheme":         "example.com",
		"empty":             "",
		"leading space":     " https://example.com",
		"newline":           "https://example.com/\nHost: x",
		"single label":      "https://localhost/",
		"empty label":       "https://a..com/",
		"dash label":        "https://-a.example.com/",
		"no host":           "https:///path",
	}
	for name, in := range cases {
		if h, err := ParseTarget(in); err == nil {
			t.Errorf("%s: %q accepted, host %q", name, in, h)
		}
	}
}
