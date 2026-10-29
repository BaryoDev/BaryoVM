// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"github.com/BaryoDev/BaryoVM/internal/provider"
)

// fakeProvider counts Provision calls so a test can prove nothing was created.
type fakeProvider struct{ provisioned []provider.Spec }

func (f *fakeProvider) Name() string { return "fake" }
func (f *fakeProvider) Provision(_ context.Context, s provider.Spec) (provider.Instance, error) {
	f.provisioned = append(f.provisioned, s)
	return provider.Instance{ID: "i-1", Name: s.Name, PublicIP: "198.51.100.9", User: "ubuntu", Provider: "lightsail"}, nil
}
func (f *fakeProvider) Destroy(context.Context, string) error { return nil }

// provisionHarness isolates fleet state, writes a throwaway key, and swaps the
// provider factory and resolver for fakes. No cloud API or DNS is reached.
func provisionHarness(t *testing.T, dns map[string][]string) (*fakeProvider, string) {
	t.Helper()
	t.Setenv("BARYOVM_HOME", t.TempDir())
	t.Setenv("BARYOVM_ACTIVITY", "")
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(t.TempDir(), "id")
	if err := os.WriteFile(key, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	fp := &fakeProvider{}
	oldP, oldL := newProviderFn, lookupHost
	newProviderFn = func(context.Context, string, string) (provider.Provider, error) { return fp, nil }
	lookupHost = func(_ context.Context, h string) ([]string, error) {
		if a, ok := dns[h]; ok {
			return a, nil
		}
		return nil, errors.New("no such host")
	}
	t.Cleanup(func() { newProviderFn, lookupHost = oldP, oldL })
	return fp, key
}

const edge = "203.0.113.0/24,2001:db8::/32"

func TestProvisionWithoutYesCreatesNothing(t *testing.T) {
	fp, key := provisionHarness(t, nil)
	for _, args := range [][]string{
		{"vm", "provision", "web1", "--key", key},
		{"up", "web1", "--key", key, "--image", "nginx", "--container", "site"},
		{"vm", "provision", "web1", "--key", key, "--yes", "--dry-run"},
	} {
		if err := runLogged(t, args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	if len(fp.provisioned) != 0 {
		t.Fatalf("provider create was called %d time(s) without --yes", len(fp.provisioned))
	}
	store, _ := fleet.Load()
	if store.Find("web1") != nil {
		t.Fatal("a VM was registered without --yes")
	}
}

func TestProvisionYesWithFailingHostnameCheckCreatesNothing(t *testing.T) {
	fp, key := provisionHarness(t, map[string][]string{"direct.example": {"198.51.100.1"}})
	cases := map[string][]string{
		"resolves outside the ranges": {"--https-from", edge, "--http-from", edge, "--host", "direct.example"},
		"does not resolve":            {"--https-from", edge, "--http-from", edge, "--host", "ghost.example"},
		"limit with no host named":    {"--https-from", edge, "--http-from", edge},
	}
	for name, extra := range cases {
		args := append([]string{"vm", "provision", "web1", "--key", key, "--yes"}, extra...)
		err := runLogged(t, args...)
		if err == nil || !strings.Contains(err.Error(), "hostname check failed") {
			t.Fatalf("%s: want hostname check refusal, got %v", name, err)
		}
	}
	if len(fp.provisioned) != 0 {
		t.Fatalf("provider create was called %d time(s) after a failed check", len(fp.provisioned))
	}
}

func TestProvisionYesWithPassingCheckCreates(t *testing.T) {
	fp, key := provisionHarness(t, map[string][]string{"proxied.example": {"203.0.113.5"}})
	if err := runLogged(t, "vm", "provision", "web1", "--key", key, "--yes",
		"--https-from", edge, "--http-from", edge, "--host", "proxied.example"); err != nil {
		t.Fatal(err)
	}
	if len(fp.provisioned) != 1 || fp.provisioned[0].Name != "web1" {
		t.Fatalf("want one create for web1, got %+v", fp.provisioned)
	}
	store, _ := fleet.Load()
	if store.Find("web1") == nil {
		t.Fatal("the new VM was not registered")
	}
}

func TestProvisionYesWithOpenWebPortsNeedsNoHost(t *testing.T) {
	fp, key := provisionHarness(t, nil)
	if err := runLogged(t, "vm", "provision", "web1", "--key", key, "--yes"); err != nil {
		t.Fatal(err)
	}
	if len(fp.provisioned) != 1 {
		t.Fatalf("want one create, got %d", len(fp.provisioned))
	}
}

func TestUpYesProvisionsThroughTheSameGuard(t *testing.T) {
	fp, key := provisionHarness(t, nil)
	err := runLogged(t, "up", "web1", "--key", key, "--yes", "--image", "nginx", "--container", "site",
		"--https-from", edge, "--http-from", edge)
	if err == nil || !strings.Contains(err.Error(), "hostname check failed") {
		t.Fatalf("want hostname check refusal, got %v", err)
	}
	if len(fp.provisioned) != 0 {
		t.Fatal("up created a VM after a failed hostname check")
	}
}

func TestCostNote(t *testing.T) {
	if got := costNote("lightsail", ""); !strings.Contains(got, "USD 3.50") {
		t.Fatalf("default lightsail size should state its price, got %q", got)
	}
	if got := costNote("lightsail", "large_3_0"); !strings.Contains(got, "not known") {
		t.Fatalf("an unknown size must not invent a price, got %q", got)
	}
}
