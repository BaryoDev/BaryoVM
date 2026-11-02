// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package sshx

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// lockedKey writes a passphrase key and its .pub, the shape the library cannot open on its own.
func lockedKey(t *testing.T) (string, ed25519.PrivateKey, ssh.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "id")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".pub", ssh.MarshalAuthorizedKey(sshPub), 0o644); err != nil {
		t.Fatal(err)
	}
	return path, priv, sshPub
}

// runAgent serves an in-memory agent on a socket and points SSH_AUTH_SOCK at it.
func runAgent(t *testing.T, keys ...ed25519.PrivateKey) {
	t.Helper()
	ring := agent.NewKeyring()
	for _, k := range keys {
		if err := ring.Add(agent.AddedKey{PrivateKey: k}); err != nil {
			t.Fatal(err)
		}
	}
	dir, err := os.MkdirTemp("", "ag") // not t.TempDir: its long path can pass the unix socket limit
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() { _ = agent.ServeAgent(ring, c); _ = c.Close() }()
		}
	}()
	t.Setenv("SSH_AUTH_SOCK", sock)
}

func TestLoadSignerUsesAgentForKeyItCannotRead(t *testing.T) {
	path, priv, pub := lockedKey(t)
	_, other, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	runAgent(t, other, priv)

	signer, closeAgent, err := loadSigner(path)
	if err != nil {
		t.Fatal(err)
	}
	defer closeAgent()
	if !bytes.Equal(signer.PublicKey().Marshal(), pub.Marshal()) {
		t.Fatal("agent returned a different key than the .pub names")
	}
	sig, err := signer.Sign(rand.Reader, []byte("data"))
	if err != nil {
		t.Fatal(err)
	}
	if err := pub.Verify([]byte("data"), sig); err != nil {
		t.Fatal(err)
	}
}

func TestLoadSignerRefusesWhenAgentLacksTheKey(t *testing.T) {
	path, _, _ := lockedKey(t)
	_, other, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	runAgent(t, other)

	_, _, err = loadSigner(path)
	if err == nil || !strings.Contains(err.Error(), "run ssh-add") {
		t.Fatalf("err = %v, want the run ssh-add hint", err)
	}
}

func TestLoadSignerNoAgentKeepsParseError(t *testing.T) {
	path, _, _ := lockedKey(t)
	t.Setenv("SSH_AUTH_SOCK", "")

	_, _, err := loadSigner(path)
	if err == nil || !strings.Contains(err.Error(), "parse key") {
		t.Fatalf("err = %v, want a parse key error", err)
	}
}
