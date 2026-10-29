// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package provider

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// Cloudflare publishes its edge ranges here. They change, so they are fetched
// when a run needs them and never baked into the binary.
const (
	CloudflareV4URL = "https://www.cloudflare.com/ips-v4"
	CloudflareV6URL = "https://www.cloudflare.com/ips-v6"
)

// CloudflarePreset is the value that stands for Cloudflare's ranges in a
// source list.
const CloudflarePreset = "cloudflare"

// Fetcher returns the body at a URL. It is the seam tests replace with a
// canned response.
type Fetcher func(ctx context.Context, url string) ([]byte, error)

// HTTPFetch is the real Fetcher: a bounded GET that treats any non-200 as a
// failure.
func HTTPFetch(ctx context.Context, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

// ParseRanges reads one CIDR per line. A line that is not a CIDR fails the
// whole parse and an empty body is an error: a half-read or empty list must
// never become "no limit" or "limit to nothing" by accident.
func ParseRanges(body []byte) ([]string, error) {
	var out []string
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		_, n, err := net.ParseCIDR(line)
		if err != nil {
			return nil, fmt.Errorf("%q is not a CIDR", line)
		}
		out = append(out, n.String())
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("the list is empty")
	}
	return out, nil
}

// CloudflareRanges fetches the IPv4 and IPv6 lists. Any failure aborts: the
// caller must not fall back to an empty list or to open.
func CloudflareRanges(ctx context.Context, fetch Fetcher) ([]string, error) {
	var all []string
	for _, u := range []string{CloudflareV4URL, CloudflareV6URL} {
		body, err := fetch(ctx, u)
		if err != nil {
			return nil, fmt.Errorf("cloudflare ranges: fetching %s: %w (nothing was changed; pass explicit CIDRs instead)", u, err)
		}
		r, err := ParseRanges(body)
		if err != nil {
			return nil, fmt.Errorf("cloudflare ranges: %s: %w (nothing was changed)", u, err)
		}
		all = append(all, r...)
	}
	return all, nil
}

// ResolveSources turns a --ssh-from style list into CIDRs: the cloudflare
// preset expands to the fetched ranges, anything else must parse as a CIDR.
// An empty input stays empty, which means "no limit".
func ResolveSources(ctx context.Context, fetch Fetcher, in []string) ([]string, error) {
	var out []string
	for _, v := range in {
		v = strings.TrimSpace(v)
		if strings.EqualFold(v, CloudflarePreset) {
			r, err := CloudflareRanges(ctx, fetch)
			if err != nil {
				return nil, err
			}
			out = append(out, r...)
			continue
		}
		_, n, err := net.ParseCIDR(v)
		if err != nil {
			return nil, fmt.Errorf("%q is not a CIDR or the %q preset (use 203.0.113.7/32 for one address)", v, CloudflarePreset)
		}
		out = append(out, n.String())
	}
	return out, nil
}
