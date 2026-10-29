package sshx

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func edSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, k, _ := ed25519.GenerateKey(rand.Reader)
	s, err := ssh.NewSignerFromKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// certSigner presents key's public half as a host certificate signed by a CA nobody trusts.
func certSigner(t *testing.T, key ssh.Signer) ssh.Signer {
	t.Helper()
	c := &ssh.Certificate{Key: key.PublicKey(), CertType: ssh.HostCert, ValidBefore: ssh.CertTimeInfinity}
	if err := c.SignCert(rand.Reader, edSigner(t)); err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewCertSigner(c, key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func serveSSH(t *testing.T, hostKeys ...ssh.Signer) (string, int) {
	t.Helper()
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) {
		return &ssh.Permissions{}, nil
	}}
	for _, k := range hostKeys {
		cfg.AddHostKey(k)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				_, chans, reqs, err := ssh.NewServerConn(c, cfg)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(reqs)
				for ch := range chans {
					_ = ch.Reject(ssh.Prohibited, "")
				}
			}()
		}
	}()
	host, port, _ := net.SplitHostPort(l.Addr().String())
	p, _ := strconv.Atoi(port)
	return host, p
}

// knownHost isolates HOME and BARYOVM_HOME, records stored for the address, and returns a target
// with a fresh client key plus the path of BaryoVM's own known_hosts.
func knownHost(t *testing.T, host string, port int, stored ssh.PublicKey) (Target, string) {
	t.Helper()
	home := t.TempDir()
	bh := filepath.Join(home, ".baryovm")
	if err := os.MkdirAll(bh, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("BARYOVM_HOME", bh)
	own := filepath.Join(bh, "known_hosts")
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	if err := os.WriteFile(own, []byte(knownhosts.Line([]string{knownhosts.Normalize(addr)}, stored)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, k, _ := ed25519.GenerateKey(rand.Reader)
	der, _ := x509.MarshalPKCS8PrivateKey(k)
	keyPath := filepath.Join(home, "id")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return Target{Host: host, Port: port, User: "u", KeyPath: keyPath}, own
}

func TestSelfSignedHostCertificateDoesNotReplaceTheStoredKey(t *testing.T) {
	real, impostor := edSigner(t), edSigner(t)
	host, port := serveSSH(t, certSigner(t, impostor))
	target, own := knownHost(t, host, port, real.PublicKey())
	before, _ := os.ReadFile(own)

	if c, err := Dial(target); err == nil {
		c.Close()
		t.Fatal("dial accepted a host that only had a self-signed certificate for another key")
	}
	if after, _ := os.ReadFile(own); string(after) != string(before) {
		t.Fatalf("the impostor's key was recorded:\n%s", after)
	}
}

func TestHostWithACertificateIsStillReachedByItsPlainKey(t *testing.T) {
	key := edSigner(t)
	host, port := serveSSH(t, certSigner(t, key), key)
	target, _ := knownHost(t, host, port, key.PublicKey())

	c, err := Dial(target)
	if err != nil {
		t.Fatalf("dial a known host that also has a certificate: %v", err)
	}
	c.Close()
}
