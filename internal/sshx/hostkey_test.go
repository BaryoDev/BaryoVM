// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package sshx

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/BaryoDev/BaryoVM/internal/hostkeys"
)

func ed25519Signer(t *testing.T) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ed25519: %v", err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	return s
}

func ecdsaSigner(t *testing.T) ssh.Signer {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate ecdsa: %v", err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	return s
}

// serveHostKeys runs an SSH server on loopback that offers every given host key and accepts any
// login, and returns its host and port. It only completes handshakes; no session is served.
func serveHostKeys(t *testing.T, keys ...ssh.Signer) (string, int) {
	t.Helper()
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) {
			return &ssh.Permissions{}, nil
		},
	}
	for _, k := range keys {
		cfg.AddHostKey(k)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				sc, chans, reqs, err := ssh.NewServerConn(conn, cfg)
				if err != nil {
					return
				}
				defer sc.Close()
				go ssh.DiscardRequests(reqs)
				for c := range chans {
					_ = c.Reject(ssh.Prohibited, "no sessions")
				}
			}()
		}
	}()
	return "127.0.0.1", ln.Addr().(*net.TCPAddr).Port
}

// dialWithKnownHost points BaryoVM's home at a fresh dir whose known_hosts holds only known for
// the server, hides the user's own file, and dials.
func dialWithKnownHost(t *testing.T, host string, port int, known ssh.PublicKey) error {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("BARYOVM_HOME", home)

	name := knownhosts.Normalize(net.JoinHostPort(host, strconv.Itoa(port)))
	line := knownhosts.Line([]string{name}, known) + "\n"
	if err := os.WriteFile(filepath.Join(home, "known_hosts"), []byte(line), 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate login key: %v", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("marshal login key: %v", err)
	}
	keyPath := filepath.Join(t.TempDir(), "id")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("write login key: %v", err)
	}

	c, err := Dial(Target{Host: host, Port: port, User: "u", KeyPath: keyPath})
	if err == nil {
		c.c.Close()
	}
	return err
}

func TestDialUsesTheStoredKeyTypeWhenServerOffersMore(t *testing.T) {
	ed := ed25519Signer(t)
	host, port := serveHostKeys(t, ecdsaSigner(t), ed)

	if err := dialWithKnownHost(t, host, port, ed.PublicKey()); err != nil {
		t.Fatalf("a host known by its ED25519 key must connect when the server also offers ECDSA: %v", err)
	}
}

func rsaSigner(t *testing.T) ssh.Signer {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa: %v", err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	return s
}

func TestDialReportsChangedWhenTheServerHasOnlyAnotherKeyType(t *testing.T) {
	host, port := serveHostKeys(t, ecdsaSigner(t))

	err := dialWithKnownHost(t, host, port, ed25519Signer(t).PublicKey())
	var changed *hostkeys.ErrChanged
	if !errors.As(err, &changed) {
		t.Fatalf("a stored ED25519 key against an ECDSA-only server must be ErrChanged, got %v", err)
	}
}

func TestDialReportsChangedForAStoredRSAKeyAgainstOtherTypes(t *testing.T) {
	host, port := serveHostKeys(t, ed25519Signer(t), ecdsaSigner(t))

	err := dialWithKnownHost(t, host, port, rsaSigner(t).PublicKey())
	var changed *hostkeys.ErrChanged
	if !errors.As(err, &changed) {
		t.Fatalf("a stored RSA key against an ED25519 and ECDSA server must be ErrChanged, got %v", err)
	}
}

func TestDialStillRefusesAChangedKeyOfTheStoredType(t *testing.T) {
	host, port := serveHostKeys(t, ecdsaSigner(t), ed25519Signer(t))

	err := dialWithKnownHost(t, host, port, ed25519Signer(t).PublicKey())
	var changed *hostkeys.ErrChanged
	if !errors.As(err, &changed) {
		t.Fatalf("a different ED25519 key must still be ErrChanged, got %v", err)
	}
}
