// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package sshx

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// A host that accepts the TCP connection and never sends a banner used to hang Dial forever:
// ClientConfig.Timeout covers only the connect.
func TestDialReturnsWhenTheHostNeverSendsABanner(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close() // held open, never written to
		}
	}()

	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	blk, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	key := filepath.Join(dir, "id")
	if err := os.WriteFile(key, pem.EncodeToMemory(blk), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BARYOVM_HOME", dir)

	old := handshakeTimeout
	handshakeTimeout = 300 * time.Millisecond
	defer func() { handshakeTimeout = old }()

	port := ln.Addr().(*net.TCPAddr).Port
	done := make(chan error, 1)
	go func() {
		_, err := Dial(Target{Host: "127.0.0.1", Port: port, User: "u", KeyPath: key})
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected the handshake to fail")
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("Dial still blocked after 5s with a handshake timeout of %s (port %s)", handshakeTimeout, strconv.Itoa(port))
	}
}

type fakeConn struct {
	hang   bool
	closed chan struct{}
	sent   atomic.Int32
}

func (f *fakeConn) SendRequest(name string, _ bool, _ []byte) (bool, []byte, error) {
	f.sent.Add(1)
	if name != "keepalive@openssh.com" {
		panic("unexpected request " + name)
	}
	if f.hang {
		<-f.closed
	}
	return true, nil, nil
}
func (f *fakeConn) Close() error {
	select {
	case <-f.closed:
	default:
		close(f.closed)
	}
	return nil
}
func (f *fakeConn) Wait() error { <-f.closed; return nil }

func TestKeepAliveClosesAConnectionThatStopsAnswering(t *testing.T) {
	f := &fakeConn{hang: true, closed: make(chan struct{})}
	go keepAlive(f, 20*time.Millisecond, 50*time.Millisecond)
	select {
	case <-f.closed:
	case <-time.After(3 * time.Second):
		t.Fatal("the connection was never closed")
	}
	if f.sent.Load() == 0 {
		t.Error("no keepalive was sent")
	}
}

func TestKeepAliveLeavesAnAnsweringConnectionOpen(t *testing.T) {
	f := &fakeConn{closed: make(chan struct{})}
	go keepAlive(f, 10*time.Millisecond, 200*time.Millisecond)
	time.Sleep(150 * time.Millisecond)
	select {
	case <-f.closed:
		t.Fatal("a connection that answers must stay open")
	default:
	}
	if f.sent.Load() < 2 {
		t.Errorf("want several keepalives, got %d", f.sent.Load())
	}
	f.Close()
}
