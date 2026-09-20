// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package hostkeys

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func newKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	k, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("public key: %v", err)
	}
	return k
}

func addr(t *testing.T) net.Addr {
	t.Helper()
	a, err := net.ResolveTCPAddr("tcp", "192.0.2.10:22")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	return a
}

// store rooted at a temp dir, with the user's own known_hosts deliberately disabled so a test
// cannot accidentally read the real one.
func newStore(t *testing.T) *Store {
	t.Helper()
	s := New(t.TempDir())
	s.extraPath = ""
	return s
}

func TestUnknownHostIsLearnedAndReported(t *testing.T) {
	s := newStore(t)
	key := newKey(t)

	var gotHost, gotFP string
	cb := s.Callback(func(h, fp string) { gotHost, gotFP = h, fp })

	if err := cb("192.0.2.10:22", addr(t), key); err != nil {
		t.Fatalf("first connect should be allowed: %v", err)
	}
	if gotHost == "" || gotFP == "" {
		t.Error("learning a key must be reported, or the operator never sees what was trusted")
	}
	if gotFP != ssh.FingerprintSHA256(key) {
		t.Errorf("reported the wrong fingerprint: %s", gotFP)
	}
	if _, err := os.Stat(s.Path()); err != nil {
		t.Fatalf("known_hosts was not written: %v", err)
	}
}

func TestKnownHostWithSameKeyPassesSilently(t *testing.T) {
	s := newStore(t)
	key := newKey(t)

	learns := 0
	cb := s.Callback(func(string, string) { learns++ })

	if err := cb("192.0.2.10:22", addr(t), key); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := cb("192.0.2.10:22", addr(t), key); err != nil {
		t.Fatalf("second connect with the same key must pass: %v", err)
	}
	if learns != 1 {
		t.Errorf("a key already known must not be reported as learned again, got %d", learns)
	}
}

// The reason this package exists. A different key on a known host must stop the connection.
func TestChangedKeyIsRefused(t *testing.T) {
	s := newStore(t)
	first, second := newKey(t), newKey(t)
	cb := s.Callback(nil)

	if err := cb("192.0.2.10:22", addr(t), first); err != nil {
		t.Fatalf("first: %v", err)
	}

	err := cb("192.0.2.10:22", addr(t), second)
	if err == nil {
		t.Fatal("a changed host key must be refused")
	}
	var ce *ErrChanged
	if !errors.As(err, &ce) {
		t.Fatalf("want ErrChanged, got %T: %v", err, err)
	}
	if ce.Stored == "" || ce.Offered == "" {
		t.Error("both fingerprints must be carried, the operator has to compare them")
	}
	if ce.Stored == ce.Offered {
		t.Error("stored and offered fingerprints are identical, so one was read wrong")
	}
	if !strings.Contains(err.Error(), "forget-key") {
		t.Error("the message must name the command that accepts a new key")
	}
}

func TestForgetLetsTheNextKeyBeLearned(t *testing.T) {
	s := newStore(t)
	first, second := newKey(t), newKey(t)
	cb := s.Callback(nil)

	if err := cb("192.0.2.10:22", addr(t), first); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := cb("192.0.2.10:22", addr(t), second); err == nil {
		t.Fatal("expected refusal before forgetting")
	}

	n, err := s.Forget("192.0.2.10:22")
	if err != nil {
		t.Fatalf("forget: %v", err)
	}
	if n != 1 {
		t.Errorf("want 1 key dropped, got %d", n)
	}
	if err := cb("192.0.2.10:22", addr(t), second); err != nil {
		t.Fatalf("after forgetting, the new key must be learned: %v", err)
	}
}

// Forgetting a host that was never recorded must report zero rather than looking like it worked.
func TestForgetUnknownHostReportsZero(t *testing.T) {
	s := newStore(t)
	n, err := s.Forget("192.0.2.99:22")
	if err != nil {
		t.Fatalf("forget: %v", err)
	}
	if n != 0 {
		t.Errorf("want 0, got %d", n)
	}
}

func TestForgetLeavesOtherHostsAlone(t *testing.T) {
	s := newStore(t)
	cb := s.Callback(nil)
	keep := newKey(t)

	if err := cb("192.0.2.10:22", addr(t), newKey(t)); err != nil {
		t.Fatalf("host a: %v", err)
	}
	other, _ := net.ResolveTCPAddr("tcp", "192.0.2.20:22")
	if err := cb("192.0.2.20:22", other, keep); err != nil {
		t.Fatalf("host b: %v", err)
	}

	if _, err := s.Forget("192.0.2.10:22"); err != nil {
		t.Fatalf("forget: %v", err)
	}
	if err := cb("192.0.2.20:22", other, keep); err != nil {
		t.Fatalf("the untouched host must still be known: %v", err)
	}
}

func TestKnownHostsIsWrittenPrivate(t *testing.T) {
	s := newStore(t)
	if err := s.Callback(nil)("192.0.2.10:22", addr(t), newKey(t)); err != nil {
		t.Fatalf("connect: %v", err)
	}
	fi, err := os.Stat(s.Path())
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("known_hosts must be 0600 like fleet.json, got %o", perm)
	}
}

// A key the user already verified by hand is a better source than the network. It is adopted into
// BaryoVM's own file so the trust does not depend on a file BaryoVM does not own.
func TestUserKnownHostsIsAcceptedAndAdopted(t *testing.T) {
	dir := t.TempDir()
	userDir := t.TempDir()
	userFile := filepath.Join(userDir, "known_hosts")

	key := newKey(t)
	line := "192.0.2.10:22 " + key.Type() + " " +
		strings.TrimSpace(sshBase64(key)) + "\n"
	if err := os.WriteFile(userFile, []byte(line), 0o600); err != nil {
		t.Fatalf("write user known_hosts: %v", err)
	}

	s := New(dir)
	s.extraPath = userFile

	learns := 0
	if err := s.Callback(func(string, string) { learns++ })("192.0.2.10:22", addr(t), key); err != nil {
		t.Fatalf("a key the user already trusts must be accepted: %v", err)
	}
	if learns != 0 {
		t.Error("a key adopted from the user's file was not learned from the network, do not report it as such")
	}
	if _, err := os.Stat(s.Path()); err != nil {
		t.Error("the adopted key must be recorded in BaryoVM's own file")
	}
}

func sshBase64(k ssh.PublicKey) string {
	// ssh.MarshalAuthorizedKey gives "type base64 [comment]\n"; take the base64 field.
	parts := strings.Fields(string(ssh.MarshalAuthorizedKey(k)))
	if len(parts) < 2 {
		return ""
	}
	return parts[1]
}
