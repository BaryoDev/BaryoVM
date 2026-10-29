// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package guard

import (
	"fmt"
	"net/url"
	"runtime"
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
	// AllowedRegistries are the package registries `publish` may reach, as hosts. Empty means no
	// restriction.
	AllowedRegistries []string
}

// RegistryPolicyKey is the git config key for the registries a repository may publish to:
//
//	git config --add baryovm.allowedRegistries registry.npmjs.org
const RegistryPolicyKey = "baryovm.allowedRegistries"

func splitList(values []string) []string {
	var out []string
	for _, v := range values {
		out = append(out, strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' })...)
	}
	return out
}

// ParseRegistries reads registry allowlist values: comma or space separated hosts. An entry may be
// a full URL (https://pkgs.example/npm/), which is reduced to its host: only the host is checked.
func ParseRegistries(values []string) []string {
	var out []string
	for _, e := range splitList(values) {
		if strings.Contains(e, "://") {
			if u, err := url.Parse(e); err == nil && u.Hostname() != "" {
				e = u.Hostname()
			}
		}
		out = append(out, strings.ToLower(e))
	}
	return out
}

// RegistriesRestricted reports whether a registry allowlist is set.
func (p Policy) RegistriesRestricted() bool { return len(p.AllowedRegistries) > 0 }

// AllowsRegistry reports whether publishing to host is allowed. Without an allowlist it always is.
func (p Policy) AllowsRegistry(host string) bool {
	return !p.RegistriesRestricted() || hostMatches(p.AllowedRegistries, strings.ToLower(host))
}

func hostMatches(list []string, host string) bool {
	for _, a := range list {
		if a == host || (strings.HasPrefix(a, "*.") && strings.HasSuffix(host, a[1:])) {
			return true
		}
	}
	return false
}

// ParsePolicy builds a Policy from raw values, each one a host or a list split on commas and
// whitespace. A host may start with "*." to allow every subdomain.
func ParsePolicy(values []string) Policy {
	var p Policy
	for _, h := range splitList(values) {
		p.AllowedHosts = append(p.AllowedHosts, strings.ToLower(h))
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
	return hostMatches(p.AllowedHosts, strings.ToLower(host))
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
			// url.Parse echoes the whole URL in its error, and a remote can carry a token as
			// https://user:TOKEN@host, so the error must not include it.
			return "", fmt.Errorf("cannot read the remote url: fix it with git remote set-url")
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
	// scp-style: a colon before any slash. On Windows a single letter before the colon is a drive
	// (C:\repos\r), not a host; elsewhere "x:path" is host x.
	colon := strings.Index(raw, ":")
	slash := strings.Index(raw, "/")
	if colon == 1 && runtime.GOOS == "windows" {
		return "", nil
	}
	if colon > 0 && (slash < 0 || colon < slash) {
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
