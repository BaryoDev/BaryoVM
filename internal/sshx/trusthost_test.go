// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package sshx

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/BaryoDev/BaryoVM/internal/hostkeys"
)

// freshHome isolates HOME and BARYOVM_HOME with no recorded keys anywhere, clears the trust switch
// (flag and environment), and returns BaryoVM's home and a login key path.
func freshHome(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("BARYOVM_HOME", home)
	t.Setenv("BARYOVM_TRUST_NEW_HOST", "")
	t.Setenv("BARYOVM_STRICT_HOST_KEYS", "")
	ResetTrustNewHost()
	t.Cleanup(ResetTrustNewHost)

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
	return home, keyPath
}

func dialTo(t *testing.T, host string, port int, keyPath string) error {
	t.Helper()
	c, err := Dial(Target{Host: host, Port: port, User: "u", KeyPath: keyPath})
	if err == nil {
		c.Close()
	}
	return err
}

func TestDialRefusesAnUnknownHostByDefault(t *testing.T) {
	home, keyPath := freshHome(t)
	signer := ed25519Signer(t)
	host, port := serveHostKeys(t, signer)

	// BARYOVM_STRICT_HOST_KEYS=0 used to be how a human turned strict off. It means nothing now.
	t.Setenv("BARYOVM_STRICT_HOST_KEYS", "0")
	err := dialTo(t, host, port, keyPath)
	var ue *hostkeys.ErrUnknownHost
	if !errors.As(err, &ue) {
		t.Fatalf("an unknown host must be refused by default, got %v", err)
	}
	msg := err.Error()
	addr := host + ":" + strconv.Itoa(port)
	for _, want := range []string{addr, signer.PublicKey().Type(), ssh.FingerprintSHA256(signer.PublicKey()), "--trust-new-host"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal must mention %q:\n%s", want, msg)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "known_hosts")); err == nil {
		t.Error("a refused host must not be written to known_hosts")
	}
	if IsUnreachable(err) {
		t.Error("a host key refusal is not an unreachable host")
	}
}

func TestDialLearnsAnUnknownHostWithTheFlag(t *testing.T) {
	home, keyPath := freshHome(t)
	host, port := serveHostKeys(t, ed25519Signer(t))

	SetTrustNewHost(true)
	if err := dialTo(t, host, port, keyPath); err != nil {
		t.Fatalf("--trust-new-host must learn an unknown host: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(home, "known_hosts")); err != nil || len(b) == 0 {
		t.Fatalf("the learned key must be recorded: %v", err)
	}

	// The trust was for that run. Once recorded the key is simply known, flag or not.
	SetTrustNewHost(false)
	if err := dialTo(t, host, port, keyPath); err != nil {
		t.Errorf("a recorded key must pass without the flag: %v", err)
	}
}

func TestDialLearnsAnUnknownHostWithTheEnvironment(t *testing.T) {
	_, keyPath := freshHome(t)
	host, port := serveHostKeys(t, ed25519Signer(t))

	t.Setenv("BARYOVM_TRUST_NEW_HOST", "1")
	if err := dialTo(t, host, port, keyPath); err != nil {
		t.Fatalf("BARYOVM_TRUST_NEW_HOST=1 must learn an unknown host: %v", err)
	}
}

// The flag given on the command line beats the environment, in both directions.
func TestTrustFlagBeatsTheEnvironment(t *testing.T) {
	freshHome(t)
	t.Setenv("BARYOVM_TRUST_NEW_HOST", "1")
	SetTrustNewHost(false)
	if TrustNewHost() {
		t.Error("--trust-new-host=false must beat BARYOVM_TRUST_NEW_HOST=1")
	}
}

func TestDialRefusesAChangedKeyEvenWithTheFlag(t *testing.T) {
	freshHome(t)
	recorded := ed25519Signer(t)
	host, port := serveHostKeys(t, ed25519Signer(t))

	SetTrustNewHost(true)
	err := dialWithKnownHost(t, host, port, recorded.PublicKey())
	var ce *hostkeys.ErrChanged
	if !errors.As(err, &ce) {
		t.Fatalf("a changed key must be refused even with --trust-new-host, got %v", err)
	}
}
