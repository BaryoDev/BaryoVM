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
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

// Strict mode exists because trust on first use assumes there IS a first use. A CI runner has no
// memory between runs, so every run is the first one and TOFU degrades to accepting whatever
// answers on the recorded address, every time.
func TestStrictRefusesAnUnknownHost(t *testing.T) {
	s := newStore(t).Strict(true)
	err := s.Callback(nil)("192.0.2.10:22", addr(t), newKey(t))
	if err == nil {
		t.Fatal("strict mode must refuse a host it has never seen")
	}
	var ue *ErrUnknownHost
	if !errors.As(err, &ue) {
		t.Fatalf("want ErrUnknownHost, got %T: %v", err, err)
	}
	if ue.Offered == "" {
		t.Error("the offered fingerprint must be carried, so it can be pinned after checking")
	}
	if !strings.Contains(err.Error(), "ssh-keyscan") {
		t.Error("the error must say how to record the key")
	}
}

// Refusing must not write anything: a strict run that recorded the key would have learned it, which
// is the thing strict mode forbids.
func TestStrictLearnsNothing(t *testing.T) {
	s := newStore(t).Strict(true)
	_ = s.Callback(nil)("192.0.2.10:22", addr(t), newKey(t))
	if _, err := os.Stat(s.Path()); err == nil {
		t.Fatal("strict mode must not write a known_hosts entry it refused to trust")
	}
}

// A pinned key still works in strict mode, which is the whole point: CI supplies the file.
func TestStrictAcceptsARecordedKey(t *testing.T) {
	s := newStore(t)
	key := newKey(t)
	if err := s.Callback(nil)("192.0.2.10:22", addr(t), key); err != nil {
		t.Fatalf("learning in normal mode: %v", err)
	}
	strict := New(t.TempDir()).Strict(true)
	strict.path = s.Path() // the same recorded file a CI job would supply
	strict.extraPath = ""
	if err := strict.Callback(nil)("192.0.2.10:22", addr(t), key); err != nil {
		t.Fatalf("a recorded key must still pass in strict mode: %v", err)
	}
}

// Strict changes what happens to an UNKNOWN host. A changed key was already refused and must stay
// refused, with the changed-key error rather than the unknown-host one.
func TestStrictStillReportsAChangedKeyAsChanged(t *testing.T) {
	s := newStore(t)
	first, second := newKey(t), newKey(t)
	if err := s.Callback(nil)("192.0.2.10:22", addr(t), first); err != nil {
		t.Fatalf("first: %v", err)
	}
	s.Strict(true)
	err := s.Callback(nil)("192.0.2.10:22", addr(t), second)
	var ce *ErrChanged
	if !errors.As(err, &ce) {
		t.Fatalf("want ErrChanged, got %T: %v", err, err)
	}
}

func TestKeyscanPassesThePortAsAFlag(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"192.0.2.10:22", "ssh-keyscan -p 22 -t ecdsa,ed25519,rsa 192.0.2.10"},
		{"192.0.2.10:2222", "ssh-keyscan -p 2222 -t ecdsa,ed25519,rsa 192.0.2.10"},
		{"[2001:db8::1]:22", "ssh-keyscan -p 22 -t ecdsa,ed25519,rsa 2001:db8::1"},
		{"vm.example", "ssh-keyscan -t ecdsa,ed25519,rsa vm.example"},
	} {
		if got := Keyscan(c.in); got != c.want {
			t.Errorf("Keyscan(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	err := &ErrUnknownHost{Host: "192.0.2.10:2222", Offered: "SHA256:x", Store: "/k"}
	if !strings.Contains(err.Error(), "ssh-keyscan -p 2222 -t ecdsa,ed25519,rsa 192.0.2.10 >> /k") {
		t.Errorf("strict-mode error must keep the port: %s", err)
	}
}

func TestKnownHostsForReturnsOnlyTheRecordedKeyOfThatHost(t *testing.T) {
	s := newStore(t)
	key := newKey(t)
	if err := s.add("192.0.2.10:22", key); err != nil {
		t.Fatal(err)
	}
	if err := s.add("192.0.2.11:22", newKey(t)); err != nil {
		t.Fatal(err)
	}
	got, err := s.KnownHostsFor("192.0.2.10:22")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(got, "\n") != 1 || !strings.HasPrefix(got, "192.0.2.10 ") {
		t.Errorf("want exactly the one line for 192.0.2.10, got %q", got)
	}
	if none, _ := s.KnownHostsFor("192.0.2.99:22"); none != "" {
		t.Errorf("an unknown host must give no keys, got %q", none)
	}
}

func TestAskTrustsAndRecordsOnYes(t *testing.T) {
	var gotHost, gotFP string
	s := newStore(t).Mode(Ask).Confirm(func(host, fp string) bool {
		gotHost, gotFP = host, fp
		return true
	})
	key := newKey(t)
	if err := s.Callback(nil)("192.0.2.10:22", addr(t), key); err != nil {
		t.Fatalf("a yes must trust the key: %v", err)
	}
	if gotHost != "192.0.2.10:22" || gotFP != ssh.FingerprintSHA256(key) {
		t.Errorf("confirm saw %q %q, want the host and the offered fingerprint", gotHost, gotFP)
	}
	if b, _ := os.ReadFile(s.Path()); !strings.Contains(string(b), "192.0.2.10") {
		t.Error("a yes must record the key")
	}
	// Recorded, so the next connection is a known host and is not asked again.
	again := newStore(t).Mode(Ask).Confirm(func(string, string) bool { t.Error("asked about a known host"); return false })
	again.path = s.Path()
	if err := again.Callback(nil)("192.0.2.10:22", addr(t), key); err != nil {
		t.Fatalf("known key after a yes: %v", err)
	}
}

func TestAskRefusesAndWritesNothingOnNo(t *testing.T) {
	s := newStore(t).Mode(Ask).Confirm(func(string, string) bool { return false })
	err := s.Callback(nil)("192.0.2.10:22", addr(t), newKey(t))
	var ue *ErrUnknownHost
	if !errors.As(err, &ue) || ue.Reason != ReasonDeclined {
		t.Fatalf("want a declined ErrUnknownHost, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "not trusted") {
		t.Errorf("a declined key must say it was not trusted: %v", err)
	}
	if _, err := os.Stat(s.Path()); err == nil {
		t.Fatal("a no must not write a known_hosts entry")
	}
}

func TestAskWithNoConfirmHookRefuses(t *testing.T) {
	s := newStore(t).Mode(Ask)
	err := s.Callback(nil)("192.0.2.10:22", addr(t), newKey(t))
	var ue *ErrUnknownHost
	if !errors.As(err, &ue) || ue.Reason == ReasonDeclined {
		t.Fatalf("want ErrUnknownHost with nobody asked, got %T: %v", err, err)
	}
	if _, err := os.Stat(s.Path()); err == nil {
		t.Fatal("nothing may be written when nobody was asked")
	}
}

func TestLearnModeStillLearnsWithoutAsking(t *testing.T) {
	s := newStore(t).Mode(Learn).Confirm(func(string, string) bool { t.Error("learn mode must not ask"); return false })
	var learned string
	if err := s.Callback(func(_, fp string) { learned = fp })("192.0.2.10:22", addr(t), newKey(t)); err != nil {
		t.Fatalf("learn: %v", err)
	}
	if learned == "" {
		t.Error("the learned hook must be called in Learn mode")
	}
	if _, err := os.Stat(s.Path()); err != nil {
		t.Error("Learn must record the key")
	}
}

func TestAskDoesNotCallTheLearnedHook(t *testing.T) {
	s := newStore(t).Mode(Ask).Confirm(func(string, string) bool { return true })
	if err := s.Callback(func(_, _ string) { t.Error("the person just saw the fingerprint; learned must not repeat it") })("192.0.2.10:22", addr(t), newKey(t)); err != nil {
		t.Fatal(err)
	}
}

// A changed key is refused in every mode, and Ask must not even prompt for it.
func TestAChangedKeyIsRefusedInEveryMode(t *testing.T) {
	for _, m := range []Mode{Learn, Refuse, Ask} {
		s := newStore(t)
		if err := s.Callback(nil)("192.0.2.10:22", addr(t), newKey(t)); err != nil {
			t.Fatalf("first: %v", err)
		}
		s.Mode(m).Confirm(func(string, string) bool { t.Error("a changed key must not be asked about"); return true })
		var ce *ErrChanged
		if err := s.Callback(nil)("192.0.2.10:22", addr(t), newKey(t)); !errors.As(err, &ce) {
			t.Errorf("mode %v: want ErrChanged, got %T: %v", m, err, err)
		}
	}
}

func TestUnknownHostMessageNamesTheWayBackToLearning(t *testing.T) {
	msg := (&ErrUnknownHost{Host: "192.0.2.10:22", Offered: "SHA256:x", Store: "/k"}).Error()
	for _, want := range []string{"BARYOVM_STRICT_HOST_KEYS=0", "ssh-keyscan", "/k"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message must mention %q: %s", want, msg)
		}
	}
	if strings.Contains(msg, "strict host key checking is on") {
		t.Error("the message must be right for the no-terminal default too")
	}
}

func TestUnknownHostMessageMatchesTheReason(t *testing.T) {
	base := ErrUnknownHost{Host: "192.0.2.10:22", Offered: "SHA256:off", Store: "/k"}
	cases := []struct {
		reason  Reason
		want    []string
		notWant []string
	}{
		{ReasonNoTerminal, []string{"no terminal to ask on", "BARYOVM_STRICT_HOST_KEYS=0", "ssh-keyscan"}, []string{"strict host key checking was requested"}},
		{ReasonJSON, []string{"output is JSON", "BARYOVM_STRICT_HOST_KEYS=0", "ssh-keyscan"}, []string{"no terminal"}},
		{ReasonStrict, []string{"strict host key checking was requested", "ssh-keyscan", "unset BARYOVM_STRICT_HOST_KEYS"}, []string{"to learn an unknown host's key"}},
		{ReasonRelease, []string{"project release never trusts a new host", "hostKey"}, []string{"BARYOVM_STRICT_HOST_KEYS=0"}},
		{ReasonDeclined, []string{"not trusted", "Nothing was recorded"}, []string{"BARYOVM_STRICT_HOST_KEYS=0"}},
		{ReasonFingerprint, []string{"does not match the fingerprint you gave", "expected SHA256:want", "offered  SHA256:off"}, []string{"BARYOVM_STRICT_HOST_KEYS=0"}},
	}
	for _, c := range cases {
		e := base
		e.Reason = c.reason
		e.Expected = "SHA256:want"
		msg := e.Error()
		for _, w := range c.want {
			if !strings.Contains(msg, w) {
				t.Errorf("reason %q: message must mention %q:\n%s", c.reason, w, msg)
			}
		}
		for _, w := range c.notWant {
			if strings.Contains(msg, w) {
				t.Errorf("reason %q: message must not mention %q:\n%s", c.reason, w, msg)
			}
		}
	}
}

func TestExpectedFingerprintTrustsAMatchInAnyModeAndWritesIt(t *testing.T) {
	for _, m := range []Mode{Learn, Refuse, Ask} {
		s := newStore(t).Mode(m)
		key := newKey(t)
		s.ExpectFingerprint(ssh.FingerprintSHA256(key))
		if err := s.Callback(nil)("192.0.2.10:22", addr(t), key); err != nil {
			t.Fatalf("mode %v: a matching fingerprint must trust the key: %v", m, err)
		}
		if b, _ := os.ReadFile(s.Path()); !strings.Contains(string(b), "192.0.2.10") {
			t.Errorf("mode %v: the key must be recorded", m)
		}
	}
}

func TestExpectedFingerprintMismatchRefusesWithBothAndWritesNothing(t *testing.T) {
	s := newStore(t).Mode(Learn)
	key := newKey(t)
	s.ExpectFingerprint(ssh.FingerprintSHA256(newKey(t)))
	err := s.Callback(nil)("192.0.2.10:22", addr(t), key)
	var ue *ErrUnknownHost
	if !errors.As(err, &ue) || ue.Reason != ReasonFingerprint {
		t.Fatalf("want ErrUnknownHost with ReasonFingerprint, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), ue.Expected) || !strings.Contains(err.Error(), ssh.FingerprintSHA256(key)) {
		t.Errorf("both fingerprints must be in the message: %v", err)
	}
	if _, err := os.Stat(s.Path()); err == nil {
		t.Fatal("a mismatch must write nothing")
	}
}

// The fingerprint only vouches for an unknown host. A host with another key on record is changed.
func TestExpectedFingerprintDoesNotOverrideAChangedKey(t *testing.T) {
	s := newStore(t)
	if err := s.Callback(nil)("192.0.2.10:22", addr(t), newKey(t)); err != nil {
		t.Fatal(err)
	}
	second := newKey(t)
	s.ExpectFingerprint(ssh.FingerprintSHA256(second))
	var ce *ErrChanged
	if err := s.Callback(nil)("192.0.2.10:22", addr(t), second); !errors.As(err, &ce) {
		t.Fatalf("want ErrChanged, got %T: %v", err, err)
	}
}

// Fleet commands dial several VMs at once. Two dials to one new host must prompt once and record one
// line, the second answered from the first.
func TestConcurrentAskForOneHostPromptsOnceAndRecordsOnce(t *testing.T) {
	dir := t.TempDir()
	key := newKey(t)
	var asked atomic.Int32
	confirm := func(string, string) bool {
		asked.Add(1)
		time.Sleep(100 * time.Millisecond)
		return true
	}
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := New(dir).Mode(Ask).Confirm(confirm)
			s.extraPath = ""
			if err := s.Callback(nil)("192.0.2.10:22", addr(t), key); err != nil {
				t.Errorf("callback: %v", err)
			}
		}()
	}
	wg.Wait()
	if asked.Load() != 1 {
		t.Errorf("asked %d times, want once", asked.Load())
	}
	b, _ := os.ReadFile(filepath.Join(dir, "known_hosts"))
	if n := strings.Count(strings.TrimSpace(string(b)), "\n") + 1; n != 1 {
		t.Errorf("known_hosts has %d lines, want 1:\n%s", n, b)
	}
}

// The answer to a prompt can arrive after something else recorded a different key for the host. The
// add must then refuse like any changed key and leave the file alone.
func TestAddAfterTheAnswerRefusesWhenAnotherKeyWasRecordedMeanwhile(t *testing.T) {
	dir := t.TempDir()
	a, b := newKey(t), newKey(t)
	s := New(dir).Mode(Ask)
	s.extraPath = ""
	s.Confirm(func(string, string) bool {
		other := New(dir)
		other.extraPath = ""
		if err := other.Callback(nil)("192.0.2.10:22", addr(t), b); err != nil {
			t.Errorf("record the other key: %v", err)
		}
		return true
	})
	var ce *ErrChanged
	if err := s.Callback(nil)("192.0.2.10:22", addr(t), a); !errors.As(err, &ce) {
		t.Fatalf("want ErrChanged, got %T: %v", err, err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "known_hosts"))
	if strings.Count(string(data), "\n") != 1 || strings.Contains(string(data), sshBase64(a)) || !strings.Contains(string(data), sshBase64(b)) {
		t.Errorf("only key B may be on file:\n%s", data)
	}
}

// A key that is already recorded is a success that writes nothing.
func TestAddOfAnAlreadyRecordedKeyWritesNothing(t *testing.T) {
	s := newStore(t)
	key := newKey(t)
	if err := s.add("192.0.2.10:22", key); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(s.Path())
	if err := s.add("192.0.2.10:22", key); err != nil {
		t.Fatalf("a recorded key is a success: %v", err)
	}
	after, _ := os.ReadFile(s.Path())
	if string(before) != string(after) {
		t.Errorf("nothing may be written:\nbefore %s\nafter %s", before, after)
	}
}

// The fingerprint a caller verified must still be the key on offer for a host that is already
// known, or it would be ignored and the caller would think the check happened.
func TestExpectedFingerprintIsCheckedForAnAlreadyRecordedHost(t *testing.T) {
	s := newStore(t)
	b := newKey(t)
	if err := s.Callback(nil)("192.0.2.10:22", addr(t), b); err != nil {
		t.Fatal(err)
	}
	wrong := ssh.FingerprintSHA256(newKey(t))
	s.ExpectFingerprint(wrong)
	err := s.Callback(nil)("192.0.2.10:22", addr(t), b)
	var ue *ErrUnknownHost
	if !errors.As(err, &ue) || ue.Reason != ReasonFingerprint {
		t.Fatalf("want ReasonFingerprint, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), wrong) || !strings.Contains(err.Error(), ssh.FingerprintSHA256(b)) {
		t.Errorf("both fingerprints must be in the message: %v", err)
	}
	s.ExpectFingerprint(ssh.FingerprintSHA256(b))
	if err := s.Callback(nil)("192.0.2.10:22", addr(t), b); err != nil {
		t.Fatalf("a matching fingerprint for the recorded key is fine: %v", err)
	}
}

// The guard wraps the whole wait-and-ask section, so the lock wait is inside it.
func TestGuardWrapsTheWaitForAnotherPrompt(t *testing.T) {
	dir := t.TempDir()
	key := newKey(t)
	release := make(chan struct{})
	started := make(chan struct{})
	first := New(dir).Mode(Ask).Confirm(func(string, string) bool {
		close(started)
		<-release
		return true
	})
	first.extraPath = ""
	done := make(chan error, 1)
	go func() { done <- first.Callback(nil)("192.0.2.10:22", addr(t), key) }()
	<-started

	var inGuard atomic.Bool
	second := New(dir).Mode(Ask).Confirm(func(string, string) bool { t.Error("asked a second time"); return false })
	second.extraPath = ""
	second.Guard(func(fn func()) { inGuard.Store(true); fn() })
	res := make(chan error, 1)
	go func() { res <- second.Callback(nil)("192.0.2.10:22", addr(t), key) }()
	time.Sleep(50 * time.Millisecond)
	if !inGuard.Load() {
		t.Error("the guard must be entered before the wait for the other prompt")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-res; err != nil {
		t.Fatal(err)
	}
}
