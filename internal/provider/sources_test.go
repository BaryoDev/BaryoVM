// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package provider

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestZeroSpecKeepsAnyIPv4(t *testing.T) {
	for _, port := range []int{22, 80, 443} {
		if got := (Spec{}).CidrsFor(port); !reflect.DeepEqual(got, []string{"0.0.0.0/0"}) {
			t.Errorf("port %d = %v, want 0.0.0.0/0", port, got)
		}
		if (Spec{}).Limited(port) {
			t.Errorf("port %d reads as limited on a zero Spec", port)
		}
	}
	s := Spec{SSHFrom: []string{"203.0.113.7/32"}}
	if got := s.CidrsFor(22); !reflect.DeepEqual(got, []string{"203.0.113.7/32"}) || !s.Limited(22) {
		t.Errorf("22 = %v", got)
	}
	if got := s.CidrsFor(80); !reflect.DeepEqual(got, []string{"0.0.0.0/0"}) {
		t.Errorf("an unset port picked up another port's range: %v", got)
	}
}

func TestParseRanges(t *testing.T) {
	got, err := ParseRanges([]byte("173.245.48.0/20\r\n103.21.244.0/22\n\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"173.245.48.0/20", "103.21.244.0/22"}) {
		t.Errorf("got %v", got)
	}
	if _, err := ParseRanges([]byte("\n\n")); err == nil {
		t.Error("an empty list was accepted")
	}
	if _, err := ParseRanges([]byte("173.245.48.0/20\n<html>")); err == nil {
		t.Error("an HTML error page was accepted")
	}
}

func canned(t *testing.T) Fetcher {
	return func(_ context.Context, url string) ([]byte, error) {
		switch url {
		case CloudflareV4URL:
			return []byte("173.245.48.0/20\n103.21.244.0/22\n"), nil
		case CloudflareV6URL:
			return []byte("2400:cb00::/32\n"), nil
		}
		t.Errorf("unexpected url %s", url)
		return nil, errors.New("unexpected")
	}
}

func TestCloudflarePresetExpandsBothFamilies(t *testing.T) {
	got, err := ResolveSources(context.Background(), canned(t), []string{"cloudflare", "203.0.113.7/32"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"173.245.48.0/20", "103.21.244.0/22", "2400:cb00::/32", "203.0.113.7/32"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestCloudflareFetchFailureAborts(t *testing.T) {
	fail := func(_ context.Context, url string) ([]byte, error) {
		if url == CloudflareV6URL {
			return nil, errors.New("connection refused")
		}
		return []byte("173.245.48.0/20\n"), nil
	}
	got, err := ResolveSources(context.Background(), fail, []string{"cloudflare"})
	if err == nil || got != nil {
		t.Fatalf("a failed fetch gave %v, %v; it must abort with no list", got, err)
	}
	empty := func(context.Context, string) ([]byte, error) { return []byte("\n"), nil }
	if got, err := ResolveSources(context.Background(), empty, []string{"cloudflare"}); err == nil || got != nil {
		t.Fatalf("an empty list gave %v, %v", got, err)
	}
}

func TestResolveSourcesRejectsABareAddress(t *testing.T) {
	if _, err := ResolveSources(context.Background(), canned(t), []string{"203.0.113.7"}); err == nil {
		t.Error("a bare address was accepted")
	}
	if got, err := ResolveSources(context.Background(), canned(t), nil); err != nil || got != nil {
		t.Errorf("empty input = %v, %v", got, err)
	}
}
