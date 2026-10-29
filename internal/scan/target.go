// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package scan

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// ParseTarget validates a scan URL and returns the host the edge checks will
// resolve and probe. The URL reaches DNS and raw HTTP requests, so it is held
// to a strict shape instead of being passed on and hoped about:
//
//   - scheme http or https
//   - no userinfo: https://a@b/ reads as host "b" to a parser and as host "a"
//     to a person skimming it, and a scanner must not guess which was meant
//   - no backslash, whitespace, control character or percent escape in the
//     authority, which are the usual ways to make two parsers disagree
//   - a DNS name, not an IP address: the edge checks are about the name's
//     records and what the bare IP of that name answers
//   - ASCII only: an internationalised name must be given as punycode
//     (xn--...), so what is looked up is exactly what was typed
func ParseTarget(raw string) (string, error) {
	if strings.TrimSpace(raw) != raw || raw == "" {
		return "", errors.New("scan: target URL is empty or has surrounding whitespace")
	}
	for _, r := range raw {
		if r < 0x20 || r == 0x7f || r == ' ' {
			return "", errors.New("scan: target URL contains a control character or space")
		}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("scan: parse target URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("scan: target URL scheme must be http or https, got %q", u.Scheme)
	}
	if u.User != nil {
		return "", errors.New("scan: target URL must not contain userinfo (user@host)")
	}
	// Everything before the first path, query or fragment delimiter is the
	// authority; check it as typed, not as the parser normalised it.
	authority := strings.TrimPrefix(raw, u.Scheme+"://")
	if i := strings.IndexAny(authority, "/?#"); i >= 0 {
		authority = authority[:i]
	}
	if strings.ContainsAny(authority, `\%@`) {
		return "", errors.New("scan: target URL host contains a backslash, escape or @")
	}
	host := u.Hostname()
	if host == "" {
		return "", errors.New("scan: target URL has no host")
	}
	for i := 0; i < len(host); i++ {
		if host[i] >= 0x80 {
			return "", errors.New("scan: internationalised host names must be given as punycode (xn--...)")
		}
	}
	if net.ParseIP(host) != nil || strings.Contains(host, ":") {
		return "", errors.New("scan: target must be a DNS name, not an IP address")
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if len(host) > 253 || !validHostname(host) {
		return "", fmt.Errorf("scan: %q is not a valid host name", host)
	}
	return host, nil
}

func validHostname(h string) bool {
	labels := strings.Split(h, ".")
	if len(labels) < 2 {
		return false
	}
	for _, l := range labels {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
		for i := 0; i < len(l); i++ {
			c := l[i]
			ok := c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_'
			if !ok {
				return false
			}
		}
	}
	return true
}
