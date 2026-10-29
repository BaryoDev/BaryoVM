// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package guard

import (
	"fmt"
	"net/url"
	"strings"
)

// PolicyKey is the git config key the policy is read from, set once per host:
//
//	git config --add baryovm.allowedHosts github.com
const PolicyKey = "baryovm.allowedHosts"

// Policy says which hosts a repository may push to. An empty list means no policy is set and every
// host is allowed, so a repository that never opted in is not locked out. A path or file:// remote
// has no host and is always allowed: it never reaches the network.
type Policy struct {
	AllowedHosts []string
}

// ParsePolicy builds a Policy from raw values, each one a host or a list split on commas and
// whitespace. A host may start with "*." to allow every subdomain.
func ParsePolicy(values []string) Policy {
	var p Policy
	for _, v := range values {
		for _, h := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' }) {
			p.AllowedHosts = append(p.AllowedHosts, strings.ToLower(h))
		}
	}
	return p
}

// Restricted reports whether any host list is set.
func (p Policy) Restricted() bool { return len(p.AllowedHosts) > 0 }

// Allows reports whether host may be reached. An empty host (a local path) is always allowed.
func (p Policy) Allows(host string) bool {
	if !p.Restricted() || host == "" {
		return true
	}
	host = strings.ToLower(host)
	for _, a := range p.AllowedHosts {
		if a == host {
			return true
		}
		if strings.HasPrefix(a, "*.") && strings.HasSuffix(host, a[1:]) {
			return true
		}
	}
	return false
}

// ParseRemoteHost returns the host a git remote URL points at, lowercased. It reads URL forms
// (https://, ssh://, git://), scp-style (git@host:owner/repo) and local paths, which return "".
func ParseRemoteHost(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("empty remote url")
	}
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			return "", fmt.Errorf("cannot read remote url: %w", err)
		}
		if u.Scheme == "file" {
			return "", nil
		}
		h := strings.ToLower(u.Hostname())
		if h == "" {
			return "", fmt.Errorf("remote url has no host")
		}
		return h, nil
	}
	// scp-style: a colon before any slash, and more than a drive letter before it.
	colon := strings.Index(raw, ":")
	slash := strings.Index(raw, "/")
	if colon > 1 && (slash < 0 || colon < slash) {
		host := raw[:colon]
		if at := strings.LastIndex(host, "@"); at >= 0 {
			host = host[at+1:]
		}
		host = strings.Trim(host, "[]")
		if host == "" {
			return "", fmt.Errorf("remote url has no host")
		}
		return strings.ToLower(host), nil
	}
	return "", nil
}
