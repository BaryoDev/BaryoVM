// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/BaryoDev/BaryoVM/internal/fleet"
)

// writeTestKey writes a throwaway private key and returns its path.
func writeTestKey(t *testing.T) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "id")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func newSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// closedPort is an address on this machine that nothing listens on.
func closedPort(t *testing.T) (host string, port string) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h, p, _ := net.SplitHostPort(l.Addr().String())
	l.Close()
	return h, p
}

func TestVMAddUnreachableHostIsSavedAsLinuxWithAWarning(t *testing.T) {
	t.Setenv("BARYOVM_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	host, port := closedPort(t)

	_, stderr, err := runCmdE(t, newVMAddCmd(), nil, false, "box", "--host", host, "--port", port, "--key", writeTestKey(t))
	if err != nil {
		t.Fatalf("an unreachable host must not fail vm add: %v", err)
	}
	if !strings.Contains(stderr, "could not reach "+host+" to detect its OS, recorded it as linux. Run vm add again when it is up, or pass --os windows") {
		t.Errorf("warning missing from output:\n%s", stderr)
	}
	store, err := fleet.Load()
	if err != nil {
		t.Fatal(err)
	}
	vm := store.Find("box")
	if vm == nil || vm.OS != "" || vm.HostOS() != "linux" {
		t.Fatalf("stored %+v, want an entry with the OS left empty", vm)
	}
}

func TestVMAddUnreachableHostPutsTheNoteInTheJSONResult(t *testing.T) {
	t.Setenv("BARYOVM_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	host, port := closedPort(t)

	stdout, _, err := runCmdE(t, newVMAddCmd(), nil, true, "box", "--host", host, "--port", port, "--key", writeTestKey(t))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, `"ok":true`) && !strings.Contains(stdout, `"ok": true`) {
		t.Errorf("result is not ok:\n%s", stdout)
	}
	if !strings.Contains(stdout, "could not reach "+host+" to detect its OS, recorded it as linux") {
		t.Errorf("note missing from the JSON result:\n%s", stdout)
	}
}

// A host key that disagrees with the recorded one is a possible interception, not an absent host,
// so it must fail and save nothing.
func TestVMAddHostKeyMismatchStillFailsAndSavesNothing(t *testing.T) {
	bvmHome := t.TempDir()
	t.Setenv("BARYOVM_HOME", bvmHome)
	t.Setenv("HOME", t.TempDir())

	served := newSigner(t)
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(served)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				if sc, _, _, err := ssh.NewServerConn(conn, cfg); err == nil {
					sc.Close()
				}
			}()
		}
	}()
	host, port, _ := net.SplitHostPort(l.Addr().String())

	other := newSigner(t)
	line := knownhosts.Line([]string{net.JoinHostPort(host, port)}, other.PublicKey())
	if err := os.WriteFile(filepath.Join(bvmHome, "known_hosts"), []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, _, err = runCmdE(t, newVMAddCmd(), nil, false, "box", "--host", host, "--port", port, "--key", writeTestKey(t))
	if err == nil || !strings.Contains(err.Error(), "host key") {
		t.Fatalf("a changed host key must fail vm add with a host key error, got %v", err)
	}
	store, lerr := fleet.Load()
	if lerr != nil {
		t.Fatal(lerr)
	}
	if store.Find("box") != nil {
		t.Error("nothing may be saved after a host key refusal")
	}
}
