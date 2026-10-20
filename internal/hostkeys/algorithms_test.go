// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package hostkeys

import (
	"crypto/rand"
	"crypto/rsa"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func writeKnownHosts(t *testing.T, path string, lines ...string) {
	t.Helper()
	out := ""
	for _, l := range lines {
		out += l + "\n"
	}
	if err := os.WriteFile(path, []byte(out), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// startsWith reports whether got leads with want, then holds every other plain host key algorithm
// exactly once, so the server can always offer something Callback can report on, and never a
// certificate type, which Callback refuses.
func startsWith(got, want []string) bool {
	if len(got) < len(want) || !slices.Equal(got[:len(want)], want) {
		return false
	}
	seen := map[string]bool{}
	for _, a := range got {
		if seen[a] {
			return false
		}
		seen[a] = true
	}
	plain := PlainAlgorithms()
	for _, a := range plain {
		if !seen[a] {
			return false
		}
	}
	return len(got) == len(plain)
}

func TestAlgorithmsListTwoKeysOfTheSameTypeOnce(t *testing.T) {
	s := newStore(t)
	writeKnownHosts(t, s.Path(),
		knownhosts.Line([]string{"192.0.2.10"}, newKey(t)),
		knownhosts.Line([]string{"192.0.2.10"}, newKey(t)),
	)

	if got := s.Algorithms("192.0.2.10:22"); !startsWith(got, []string{ssh.KeyAlgoED25519}) {
		t.Errorf("two keys of one type must give one entry, got %v", got)
	}
}

func TestAlgorithmsDropTypesGoCannotCheck(t *testing.T) {
	s := newStore(t)
	writeKnownHosts(t, s.Path(), "192.0.2.10 sk-ssh-ed25519@openssh.com AAAAGnNrLXNzaC1lZDI1NTE5QG9wZW5zc2guY29tAAAAIA==")

	if got := s.Algorithms("192.0.2.10:22"); slices.Contains(got, "sk-ssh-ed25519@openssh.com") {
		t.Errorf("a type Go does not support as a host key must not be offered, got %v", got)
	}
}

func TestAlgorithmsOfAnUnknownHostAreThePlainDefaults(t *testing.T) {
	s := newStore(t)
	if got := s.Algorithms("192.0.2.10:22"); !slices.Equal(got, PlainAlgorithms()) {
		t.Errorf("an unknown host must get the plain defaults, got %v", got)
	}

	writeKnownHosts(t, s.Path(), knownhosts.Line([]string{"192.0.2.99"}, newKey(t)))
	if got := s.Algorithms("192.0.2.10:22"); !slices.Equal(got, PlainAlgorithms()) {
		t.Errorf("a host absent from a populated file must get the plain defaults, got %v", got)
	}
}

func TestAlgorithmsFollowTheStoredKeyType(t *testing.T) {
	s := newStore(t)
	writeKnownHosts(t, s.Path(), knownhosts.Line([]string{"192.0.2.10"}, newKey(t)))

	want := []string{ssh.KeyAlgoED25519}
	if got := s.Algorithms("192.0.2.10:22"); !startsWith(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestAlgorithmsOfAnRSAKeyIncludeTheSHA2Signatures(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa: %v", err)
	}
	key, err := ssh.NewPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatalf("public key: %v", err)
	}
	s := newStore(t)
	writeKnownHosts(t, s.Path(), knownhosts.Line([]string{"192.0.2.10"}, key))

	want := []string{"rsa-sha2-512", "rsa-sha2-256"}
	if got := s.Algorithms("192.0.2.10:22"); !startsWith(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestAlgorithmsMatchANonDefaultPort(t *testing.T) {
	s := newStore(t)
	writeKnownHosts(t, s.Path(), knownhosts.Line([]string{knownhosts.Normalize("192.0.2.10:2222")}, newKey(t)))

	if got := s.Algorithms("192.0.2.10:2222"); len(got) == 0 {
		t.Error("a key stored for host:port must be found")
	}
	if got := s.Algorithms("192.0.2.10:22"); !slices.Equal(got, PlainAlgorithms()) {
		t.Errorf("a key for another port must not leak across, got %v", got)
	}
}

func TestAlgorithmsReadHashedHostNames(t *testing.T) {
	s := newStore(t)
	hashed := knownhosts.HashHostname(knownhosts.Normalize("192.0.2.10:22"))
	writeKnownHosts(t, s.Path(), knownhosts.Line([]string{hashed}, newKey(t)))

	if got := s.Algorithms("192.0.2.10:22"); len(got) == 0 {
		t.Error("a hashed host name must still match")
	}
	if got := s.Algorithms("192.0.2.11:22"); !slices.Equal(got, PlainAlgorithms()) {
		t.Errorf("a hashed entry must not match another host, got %v", got)
	}
}

func TestAlgorithmsSkipGarbageRevokedAndCAEntries(t *testing.T) {
	s := newStore(t)
	k := newKey(t)
	good := knownhosts.Line([]string{"192.0.2.10"}, k)
	writeKnownHosts(t, s.Path(),
		"this is not a known_hosts line",
		"@revoked 192.0.2.10 "+ssh.KeyAlgoED25519+" "+sshBase64(k),
		"@cert-authority 192.0.2.10 "+ssh.KeyAlgoED25519+" "+sshBase64(k),
		"# 192.0.2.10 comment",
		good,
	)

	want := []string{ssh.KeyAlgoED25519}
	if got := s.Algorithms("192.0.2.10:22"); !startsWith(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}

	writeKnownHosts(t, s.Path(), "@revoked 192.0.2.10 "+ssh.KeyAlgoED25519+" "+sshBase64(k))
	if got := s.Algorithms("192.0.2.10:22"); !slices.Equal(got, PlainAlgorithms()) {
		t.Errorf("a revoked key is not a key to offer, got %v", got)
	}
}

func TestAlgorithmsPreferOurFileThenTheUsers(t *testing.T) {
	s := newStore(t)
	s.extraPath = filepath.Join(t.TempDir(), "known_hosts")
	writeKnownHosts(t, s.extraPath, knownhosts.Line([]string{"192.0.2.10"}, newKey(t)))

	if got := s.Algorithms("192.0.2.10:22"); len(got) == 0 {
		t.Error("the user's file is the fallback when ours has nothing")
	}

	rsaPriv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa: %v", err)
	}
	rsaKey, err := ssh.NewPublicKey(&rsaPriv.PublicKey)
	if err != nil {
		t.Fatalf("public key: %v", err)
	}
	writeKnownHosts(t, s.Path(), knownhosts.Line([]string{"192.0.2.10"}, rsaKey))
	if got := s.Algorithms("192.0.2.10:22"); !startsWith(got, []string{"rsa-sha2-512", "rsa-sha2-256"}) {
		t.Errorf("our file must win over the user's, got %v", got)
	}
}

func TestPlainAlgorithmsOfferNoCertificateType(t *testing.T) {
	for _, a := range PlainAlgorithms() {
		if strings.Contains(a, "-cert-") {
			t.Errorf("certificate type %s is offered, but Callback refuses certificates", a)
		}
	}
	if !slices.Contains(PlainAlgorithms(), ssh.KeyAlgoED25519) {
		t.Error("ed25519 must be offered")
	}
}
