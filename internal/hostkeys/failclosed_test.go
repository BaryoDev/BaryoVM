// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package hostkeys

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

const host = "192.0.2.10:22"

func record(t *testing.T, path string, prefix string, k ssh.PublicKey) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	line := prefix + knownhosts.Line([]string{knownhosts.Normalize(host)}, k) + "\n"
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(line); err != nil {
		t.Fatal(err)
	}
}

func appendRaw(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

func hostCert(t *testing.T) *ssh.Certificate {
	t.Helper()
	_, caPriv, _ := ed25519.GenerateKey(rand.Reader)
	ca, err := ssh.NewSignerFromKey(caPriv)
	if err != nil {
		t.Fatal(err)
	}
	c := &ssh.Certificate{Key: newKey(t), CertType: ssh.HostCert, ValidBefore: ssh.CertTimeInfinity}
	if err := c.SignCert(rand.Reader, ca); err != nil {
		t.Fatal(err)
	}
	return c
}

func mustRefuseAndLearnNothing(t *testing.T, s *Store, key ssh.PublicKey) error {
	t.Helper()
	before, _ := os.ReadFile(s.path)
	learned := false
	err := s.Callback(func(string, string) { learned = true })(host, addr(t), key)
	after, _ := os.ReadFile(s.path)
	if err == nil {
		t.Fatal("the key was accepted")
	}
	if learned || string(before) != string(after) {
		t.Fatalf("a refused key must not be learned:\n%s", after)
	}
	return err
}

func TestCertificateFromAKnownHostIsRefused(t *testing.T) {
	s := newStore(t)
	record(t, s.path, "", newKey(t))
	mustRefuseAndLearnNothing(t, s, hostCert(t))
}

func TestCertificateFromAnUnknownHostIsNotLearned(t *testing.T) {
	mustRefuseAndLearnNothing(t, newStore(t), hostCert(t))
}

func TestUnreadableOwnFileRefusesInsteadOfLearning(t *testing.T) {
	s := newStore(t)
	appendRaw(t, s.path, "garbage line here\n")
	record(t, s.path, "", newKey(t))
	err := mustRefuseAndLearnNothing(t, s, newKey(t))
	if !strings.Contains(err.Error(), s.path) {
		t.Errorf("the error must name the file to fix: %v", err)
	}
}

func TestUnreadableUserFileRefusesInsteadOfLearning(t *testing.T) {
	s := newStore(t)
	s.extraPath = filepath.Join(t.TempDir(), "known_hosts")
	appendRaw(t, s.extraPath, "garbage line here\n")
	record(t, s.extraPath, "", newKey(t))
	err := mustRefuseAndLearnNothing(t, s, newKey(t))
	if !strings.Contains(err.Error(), s.extraPath) {
		t.Errorf("the error must name the file to fix: %v", err)
	}
}

func TestRevokedKeyIsRefused(t *testing.T) {
	for _, own := range []bool{true, false} {
		s := newStore(t)
		path := s.path
		if !own {
			s.extraPath = filepath.Join(t.TempDir(), "known_hosts")
			path = s.extraPath
		}
		key := newKey(t)
		record(t, path, "@revoked ", key)
		err := mustRefuseAndLearnNothing(t, s, key)
		if !strings.Contains(err.Error(), "revoked") {
			t.Errorf("own=%v: the error must say the key is revoked: %v", own, err)
		}
	}
}
