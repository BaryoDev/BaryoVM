// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package sshx

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/BaryoDev/BaryoVM/internal/hostkeys"
)

// setHostKeyState sets the process-wide inputs of the mode rule and puts them back afterwards.
func setHostKeyState(t *testing.T, override *bool, canAsk bool, confirm func(host, fp string) bool) {
	t.Helper()
	oldOverride, oldInteractive, oldConfirm := strictOverride, interactive, ConfirmHostKey
	t.Cleanup(func() { strictOverride, interactive, ConfirmHostKey = oldOverride, oldInteractive, oldConfirm })
	strictOverride, interactive, ConfirmHostKey = override, canAsk, confirm
}

func boolPtr(b bool) *bool { return &b }

func TestHostKeyModeResolution(t *testing.T) {
	yes := func(string, string) bool { return true }
	cases := []struct {
		name     string
		override *bool
		env      string
		canAsk   bool
		confirm  func(string, string) bool
		want     hostkeys.Mode
	}{
		{"default on a terminal asks", nil, "", true, yes, hostkeys.Ask},
		{"default with no terminal or JSON refuses", nil, "", false, yes, hostkeys.Refuse},
		{"default on a terminal with nothing to ask with refuses", nil, "", true, nil, hostkeys.Refuse},
		{"env strict refuses on a terminal", nil, "1", true, yes, hostkeys.Refuse},
		{"env true refuses", nil, "true", true, yes, hostkeys.Refuse},
		{"env 0 learns on a terminal", nil, "0", true, yes, hostkeys.Learn},
		{"env off learns with no terminal", nil, "off", false, yes, hostkeys.Learn},
		{"env no learns", nil, "no", false, nil, hostkeys.Learn},
		{"env false learns", nil, "false", false, nil, hostkeys.Learn},
		{"env gibberish is the default", nil, "maybe", true, yes, hostkeys.Ask},
		{"flag strict beats env 0", boolPtr(true), "0", true, yes, hostkeys.Refuse},
		{"flag false beats env 1", boolPtr(false), "1", false, yes, hostkeys.Learn},
		{"flag false on a terminal learns, it does not ask", boolPtr(false), "", true, yes, hostkeys.Learn},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("BARYOVM_STRICT_HOST_KEYS", c.env)
			setHostKeyState(t, c.override, c.canAsk, c.confirm)
			if got := HostKeyMode(); got != c.want {
				t.Errorf("HostKeyMode() = %v, want %v", got, c.want)
			}
			if got, want := StrictHostKeys(), c.want == hostkeys.Refuse; got != want {
				t.Errorf("StrictHostKeys() = %v, want %v", got, want)
			}
		})
	}
}

func TestStrictHostKeysRequestedIsOnlyAnExplicitRequest(t *testing.T) {
	t.Setenv("BARYOVM_STRICT_HOST_KEYS", "")
	setHostKeyState(t, nil, false, nil)
	if StrictHostKeysRequested() {
		t.Error("the no-terminal default refuses but was not requested; a project release must still want a pin")
	}
	t.Setenv("BARYOVM_STRICT_HOST_KEYS", "1")
	if !StrictHostKeysRequested() {
		t.Error("BARYOVM_STRICT_HOST_KEYS=1 is an explicit request")
	}
	t.Setenv("BARYOVM_STRICT_HOST_KEYS", "0")
	if StrictHostKeysRequested() {
		t.Error("BARYOVM_STRICT_HOST_KEYS=0 is not a strict request")
	}
}

func TestForceRefuseBeatsEveryOtherSettingAndRestores(t *testing.T) {
	t.Setenv("BARYOVM_STRICT_HOST_KEYS", "0")
	setHostKeyState(t, boolPtr(false), true, func(string, string) bool { return true })
	restore := ForceRefuse()
	if got := HostKeyMode(); got != hostkeys.Refuse {
		t.Errorf("under ForceRefuse HostKeyMode() = %v, want Refuse", got)
	}
	restore()
	if got := HostKeyMode(); got != hostkeys.Learn {
		t.Errorf("after restore HostKeyMode() = %v, want Learn", got)
	}
}

// dialUnknownHost dials a server whose key BaryoVM has never seen, with an empty home, and returns
// the home so a test can look at what was recorded.
func dialUnknownHost(t *testing.T, host string, port int) (string, error) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("BARYOVM_HOME", home)
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
	return home, err
}

func recorded(home string) string {
	b, _ := os.ReadFile(filepath.Join(home, "known_hosts"))
	return string(b)
}

func TestDialAsksAboutAnUnknownHostAndRecordsTheKeyOnYes(t *testing.T) {
	t.Setenv("BARYOVM_STRICT_HOST_KEYS", "")
	signer := ed25519Signer(t)
	host, port := serveHostKeys(t, signer)
	var asked atomic.Int32
	var gotFP string
	setHostKeyState(t, nil, true, func(_, fp string) bool {
		asked.Add(1)
		gotFP = fp
		return true
	})
	home, err := dialUnknownHost(t, host, port)
	if err != nil {
		t.Fatalf("a yes must let the dial through: %v", err)
	}
	if asked.Load() != 1 {
		t.Errorf("asked %d times, want once", asked.Load())
	}
	if gotFP != ssh.FingerprintSHA256(signer.PublicKey()) {
		t.Errorf("the prompt was shown %q, not the offered fingerprint", gotFP)
	}
	if recorded(home) == "" {
		t.Error("a yes must record the key")
	}
}

func TestDialRefusesAndRecordsNothingWhenTheAnswerIsNo(t *testing.T) {
	t.Setenv("BARYOVM_STRICT_HOST_KEYS", "")
	host, port := serveHostKeys(t, ed25519Signer(t))
	setHostKeyState(t, nil, true, func(string, string) bool { return false })
	home, err := dialUnknownHost(t, host, port)
	var unknown *hostkeys.ErrUnknownHost
	if !errors.As(err, &unknown) || unknown.Reason != hostkeys.ReasonDeclined {
		t.Fatalf("want a declined ErrUnknownHost, got %v", err)
	}
	if recorded(home) != "" {
		t.Error("a no must not record the key")
	}
}

func TestDialRefusesAnUnknownHostWithNoTerminal(t *testing.T) {
	t.Setenv("BARYOVM_STRICT_HOST_KEYS", "")
	host, port := serveHostKeys(t, ed25519Signer(t))
	var asked atomic.Int32
	setHostKeyState(t, nil, false, func(string, string) bool { asked.Add(1); return true })
	home, err := dialUnknownHost(t, host, port)
	var unknown *hostkeys.ErrUnknownHost
	if !errors.As(err, &unknown) || unknown.Reason != hostkeys.ReasonNoTerminal {
		t.Fatalf("want ErrUnknownHost refused for having no terminal, got %v", err)
	}
	if asked.Load() != 0 {
		t.Error("with no terminal nobody may be asked")
	}
	if recorded(home) != "" {
		t.Error("a refused host must not be recorded")
	}
}

func TestDialLearnsSilentlyWhenStrictIsSwitchedOff(t *testing.T) {
	t.Setenv("BARYOVM_STRICT_HOST_KEYS", "0")
	host, port := serveHostKeys(t, ed25519Signer(t))
	var asked atomic.Int32
	setHostKeyState(t, nil, true, func(string, string) bool { asked.Add(1); return false })
	home, err := dialUnknownHost(t, host, port)
	if err != nil {
		t.Fatalf("BARYOVM_STRICT_HOST_KEYS=0 must learn on first use: %v", err)
	}
	if asked.Load() != 0 {
		t.Error("learning must not prompt")
	}
	if recorded(home) == "" {
		t.Error("learning must record the key")
	}
}

func TestDialStrictRefusesEvenWhenAPersonCouldBeAsked(t *testing.T) {
	t.Setenv("BARYOVM_STRICT_HOST_KEYS", "")
	host, port := serveHostKeys(t, ed25519Signer(t))
	var asked atomic.Int32
	setHostKeyState(t, boolPtr(true), true, func(string, string) bool { asked.Add(1); return true })
	_, err := dialUnknownHost(t, host, port)
	var unknown *hostkeys.ErrUnknownHost
	if !errors.As(err, &unknown) || asked.Load() != 0 {
		t.Fatalf("strict must refuse without asking, got %v (asked %d)", err, asked.Load())
	}
}

// A person reading a fingerprint can take longer than the handshake deadline. The time spent in the
// prompt must not count against it.
func TestPromptTimeIsNotCountedAgainstTheHandshakeDeadline(t *testing.T) {
	t.Setenv("BARYOVM_STRICT_HOST_KEYS", "")
	host, port := serveHostKeys(t, ed25519Signer(t))
	setHostKeyState(t, nil, true, func(string, string) bool {
		time.Sleep(900 * time.Millisecond)
		return true
	})
	old := handshakeTimeout
	handshakeTimeout = 300 * time.Millisecond
	defer func() { handshakeTimeout = old }()

	if _, err := dialUnknownHost(t, host, port); err != nil {
		t.Fatalf("a prompt longer than the handshake deadline must not fail the dial: %v", err)
	}
}

// The deadline still bounds a host that stalls after the prompt is answered.
func TestHandshakeDeadlineIsRearmedAfterThePrompt(t *testing.T) {
	g := &handshakeGuard{}
	srv, cli := netPipe(t)
	defer srv.Close()
	defer cli.Close()
	_ = cli.SetDeadline(time.Now().Add(time.Hour))
	g.set(cli, 100*time.Millisecond)
	g.pause(func() { time.Sleep(200 * time.Millisecond) })
	res := make(chan error, 1)
	go func() {
		_, err := cli.Read(make([]byte, 1))
		res <- err
	}()
	select {
	case err := <-res:
		if err == nil {
			t.Fatal("read should time out, nothing was written")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("deadline not re-armed to the handshake window after the prompt")
	}
}

func netPipe(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	acc := make(chan net.Conn, 1)
	go func() { c, _ := ln.Accept(); acc <- c }()
	cli, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return <-acc, cli
}

func TestRefusalReasonFollowsHowTheRunWasStarted(t *testing.T) {
	cases := []struct {
		name     string
		env      string
		json     bool
		force    bool
		override *bool
		want     hostkeys.Reason
	}{
		{"no terminal", "", false, false, nil, hostkeys.ReasonNoTerminal},
		{"json", "", true, false, nil, hostkeys.ReasonJSON},
		{"strict by env", "1", false, false, nil, hostkeys.ReasonStrict},
		{"strict by flag", "", true, false, boolPtr(true), hostkeys.ReasonStrict},
		{"release beats the rest", "1", true, true, boolPtr(true), hostkeys.ReasonRelease},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("BARYOVM_STRICT_HOST_KEYS", c.env)
			setHostKeyState(t, c.override, false, nil)
			oldJSON := outputJSON
			t.Cleanup(func() { outputJSON = oldJSON })
			SetOutputJSON(c.json)
			if c.force {
				defer ForceRefuse()()
			}
			if got := refuseReason(); got != c.want {
				t.Errorf("refuseReason() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestDialTrustsAMatchingFingerprintWithNoPrompt(t *testing.T) {
	t.Setenv("BARYOVM_STRICT_HOST_KEYS", "")
	signer := ed25519Signer(t)
	host, port := serveHostKeys(t, signer)
	setHostKeyState(t, nil, false, nil)
	defer TrustFingerprint(ssh.FingerprintSHA256(signer.PublicKey()))()
	home, err := dialUnknownHost(t, host, port)
	if err != nil {
		t.Fatalf("a matching fingerprint must let the dial through: %v", err)
	}
	if recorded(home) == "" {
		t.Error("the key must be recorded")
	}
}

func TestDialRefusesAMismatchedFingerprintAndWritesNothing(t *testing.T) {
	t.Setenv("BARYOVM_STRICT_HOST_KEYS", "0")
	host, port := serveHostKeys(t, ed25519Signer(t))
	setHostKeyState(t, nil, false, nil)
	defer TrustFingerprint(ssh.FingerprintSHA256(ed25519Signer(t).PublicKey()))()
	home, err := dialUnknownHost(t, host, port)
	var unknown *hostkeys.ErrUnknownHost
	if !errors.As(err, &unknown) || unknown.Reason != hostkeys.ReasonFingerprint {
		t.Fatalf("want ReasonFingerprint, got %v", err)
	}
	if recorded(home) != "" {
		t.Error("a mismatch must write nothing")
	}
}

func TestTrustFingerprintIsIgnoredDuringARelease(t *testing.T) {
	t.Setenv("BARYOVM_STRICT_HOST_KEYS", "")
	signer := ed25519Signer(t)
	host, port := serveHostKeys(t, signer)
	setHostKeyState(t, nil, false, nil)
	defer TrustFingerprint(ssh.FingerprintSHA256(signer.PublicKey()))()
	defer ForceRefuse()()
	home, err := dialUnknownHost(t, host, port)
	var unknown *hostkeys.ErrUnknownHost
	if !errors.As(err, &unknown) || unknown.Reason != hostkeys.ReasonRelease {
		t.Fatalf("a release must refuse a new host whatever fingerprint is given, got %v", err)
	}
	if recorded(home) != "" {
		t.Error("nothing may be recorded")
	}
}
