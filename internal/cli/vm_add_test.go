// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
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

// A host that answers SSH but cannot run the probes is reachable. The warning must carry the
// detection error and must not claim the host was unreachable.
func TestVMAddDetectionFailureKeepsTheErrorAndDoesNotSayUnreachable(t *testing.T) {
	t.Setenv("BARYOVM_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	// The test server's key is new and there is no terminal, which is refused by default. This test
	// is about detection, so it learns the key the way releases before host key prompts did.
	t.Setenv("BARYOVM_STRICT_HOST_KEYS", "0")

	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(newSigner(t))
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
				sc, chans, reqs, err := ssh.NewServerConn(conn, cfg)
				if err != nil {
					return
				}
				defer sc.Close()
				go ssh.DiscardRequests(reqs)
				for nc := range chans {
					ch, creqs, err := nc.Accept()
					if err != nil {
						return
					}
					go func() {
						for r := range creqs {
							_ = r.Reply(false, nil)
						}
						ch.Close()
					}()
				}
			}()
		}
	}()
	host, port, _ := net.SplitHostPort(l.Addr().String())

	_, stderr, err := runCmdE(t, newVMAddCmd(), nil, false, "box", "--host", host, "--port", port, "--key", writeTestKey(t))
	if err != nil {
		t.Fatalf("a failed detection must not fail vm add: %v", err)
	}
	if !strings.Contains(stderr, "could not tell whether the host is Linux or Windows") {
		t.Errorf("detection error missing from the warning:\n%s", stderr)
	}
	if strings.Contains(stderr, "could not reach") || strings.Contains(stderr, "unreachable") {
		t.Errorf("the host was reached, the warning must not say otherwise:\n%s", stderr)
	}
	store, err := fleet.Load()
	if err != nil {
		t.Fatal(err)
	}
	if store.Find("box") == nil {
		t.Error("the VM must still be added")
	}
}

// serveOneKey runs an SSH server on loopback with one host key and returns its host, port and key.
func serveOneKey(t *testing.T) (string, string, ssh.Signer) {
	t.Helper()
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
	return host, port, served
}

func TestVMAddHostKeyFingerprintRecordsAMatchingKey(t *testing.T) {
	bvmHome := t.TempDir()
	t.Setenv("BARYOVM_HOME", bvmHome)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("BARYOVM_STRICT_HOST_KEYS", "")
	host, port, served := serveOneKey(t)

	_, _, err := runCmdE(t, newVMAddCmd(), nil, true, "box", "--host", host, "--port", port, "--key", writeTestKey(t),
		"--host-key-fingerprint", ssh.FingerprintSHA256(served.PublicKey()))
	if err != nil {
		t.Fatalf("a matching fingerprint must register the VM: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(bvmHome, "known_hosts")); !strings.Contains(string(b), host) {
		t.Errorf("the key must be recorded, known_hosts is %q", b)
	}
	if store, _ := fleet.Load(); store.Find("box") == nil {
		t.Error("the VM must be saved")
	}
}

func TestVMAddHostKeyFingerprintMismatchRefusesAndWritesNothing(t *testing.T) {
	bvmHome := t.TempDir()
	t.Setenv("BARYOVM_HOME", bvmHome)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("BARYOVM_STRICT_HOST_KEYS", "0")
	host, port, _ := serveOneKey(t)

	stdout, _, err := runCmdE(t, newVMAddCmd(), nil, true, "box", "--host", host, "--port", port, "--key", writeTestKey(t),
		"--host-key-fingerprint", ssh.FingerprintSHA256(newSigner(t).PublicKey()))
	if err == nil || !strings.Contains(err.Error(), "does not match the fingerprint you gave") {
		t.Fatalf("want a mismatch refusal, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(bvmHome, "known_hosts")); statErr == nil {
		t.Error("a mismatch must write nothing")
	}
	if store, _ := fleet.Load(); store.Find("box") != nil {
		t.Error("the VM must not be saved")
	}
	if !strings.Contains(stdout, `"reason"`) || !strings.Contains(stdout, `"fingerprint"`) {
		t.Errorf("the JSON error must carry the structured reason:\n%s", stdout)
	}
}

// The fingerprint vouches for an unknown host only. A host already recorded with another key is a
// changed key and stays refused, even when the given fingerprint is the one it now offers.
func TestVMAddHostKeyFingerprintDoesNotOverrideAChangedKey(t *testing.T) {
	bvmHome := t.TempDir()
	t.Setenv("BARYOVM_HOME", bvmHome)
	t.Setenv("HOME", t.TempDir())
	host, port, served := serveOneKey(t)
	line := knownhosts.Line([]string{net.JoinHostPort(host, port)}, newSigner(t).PublicKey())
	if err := os.WriteFile(filepath.Join(bvmHome, "known_hosts"), []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := runCmdE(t, newVMAddCmd(), nil, true, "box", "--host", host, "--port", port, "--key", writeTestKey(t),
		"--host-key-fingerprint", ssh.FingerprintSHA256(served.PublicKey()))
	if err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("a changed key must still be refused, got %v", err)
	}
}

// An unknown host under -o json carries the host and the offered fingerprint as fields, so an app can
// show it, ask the person, and call again with --host-key-fingerprint.
func TestVMAddUnknownHostJSONErrorCarriesTheOfferedFingerprint(t *testing.T) {
	t.Setenv("BARYOVM_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("BARYOVM_STRICT_HOST_KEYS", "")
	host, port, served := serveOneKey(t)

	stdout, _, err := runCmdE(t, newVMAddCmd(), nil, true, "box", "--host", host, "--port", port, "--key", writeTestKey(t))
	if err == nil {
		t.Fatal("an unknown host under JSON must be refused")
	}
	var out struct {
		OK   bool `json:"ok"`
		Data struct {
			Host    string `json:"host"`
			Offered string `json:"offered"`
			Reason  string `json:"reason"`
		} `json:"data"`
	}
	if jerr := json.Unmarshal([]byte(stdout), &out); jerr != nil {
		t.Fatalf("not one JSON document (%v): %q", jerr, stdout)
	}
	if out.OK || out.Data.Offered != ssh.FingerprintSHA256(served.PublicKey()) || !strings.Contains(out.Data.Host, host) {
		t.Errorf("structured fields missing or wrong: %+v", out)
	}
}
