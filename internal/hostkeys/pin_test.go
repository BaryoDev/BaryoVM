// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package hostkeys

import (
	"errors"
	"os"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func authorized(k ssh.PublicKey) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k)))
}

func resetPins(t *testing.T) {
	t.Helper()
	ResetSessionPins()
	t.Cleanup(ResetSessionPins)
}

func TestPinThenCallbackAcceptsOnlyThePinnedKeyInStrictMode(t *testing.T) {
	resetPins(t)
	s := newStore(t).Strict(true)
	good, bad := newKey(t), newKey(t)
	if err := s.PinForRun(host, authorized(good)+" ci@runner"); err != nil {
		t.Fatalf("pin: %v", err)
	}
	if err := s.Callback(nil)(host, addr(t), good); err != nil {
		t.Errorf("the pinned key should be accepted: %v", err)
	}
	var changed *ErrChanged
	if err := s.Callback(nil)(host, addr(t), bad); !errors.As(err, &changed) {
		t.Errorf("a different key must be refused as changed, got %v", err)
	}
}

// A pin is for the run. Writing it to known_hosts would let a committed file change what this
// machine trusts for good.
func TestPinForRunWritesNothingToDisk(t *testing.T) {
	resetPins(t)
	s := newStore(t)
	k := newKey(t)
	if err := s.PinForRun(host, authorized(k)); err != nil {
		t.Fatal(err)
	}
	if err := s.Callback(nil)(host, addr(t), k); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.Path()); !os.IsNotExist(err) {
		t.Errorf("known_hosts must not exist after a pin and a connection, stat: %v", err)
	}
	ResetSessionPins()
	if err := s.Strict(true).Callback(nil)(host, addr(t), k); err == nil {
		t.Error("after the run the pin must be gone")
	}
}

func TestPinRefusesAKeyThatContradictsEitherPersistentStore(t *testing.T) {
	for _, which := range []string{"own", "user"} {
		t.Run(which, func(t *testing.T) {
			resetPins(t)
			s := newStore(t)
			path := s.path
			if which == "user" {
				s.extraPath = t.TempDir() + "/known_hosts"
				path = s.extraPath
			}
			record(t, path, "", newKey(t))
			var changed *ErrChanged
			if err := s.PinForRun(host, authorized(newKey(t))); !errors.As(err, &changed) {
				t.Fatalf("want a changed-key refusal, got %v", err)
			}
			if pinFor(host) != nil {
				t.Error("a refused pin must not be kept")
			}
		})
	}
}

func TestPinAgreeingWithARecordedKeyIsAccepted(t *testing.T) {
	resetPins(t)
	s := newStore(t)
	k := newKey(t)
	record(t, s.path, "", k)
	if err := s.PinForRun(host, authorized(k)); err != nil {
		t.Fatalf("same key: %v", err)
	}
}

func TestPinRefusesGarbage(t *testing.T) {
	resetPins(t)
	if err := newStore(t).PinForRun(host, "not a key"); err == nil {
		t.Fatal("a line that is not a public key must be refused")
	}
}

func TestKnownHostsForReturnsThePinForRsync(t *testing.T) {
	resetPins(t)
	s := newStore(t)
	k := newKey(t)
	if err := s.PinForRun(host, authorized(k)); err != nil {
		t.Fatal(err)
	}
	got, err := s.KnownHostsFor(host)
	if err != nil || !strings.Contains(got, strings.Fields(authorized(k))[1]) {
		t.Errorf("rsync must be handed the pinned key, got %q (%v)", got, err)
	}
}

// named is the hostname the project file uses; known_hosts may hold the IP instead.
const named = "vm.example.test:22"

func TestPinnedCallbackConsultsStoresByRealAddress(t *testing.T) {
	resetPins(t)
	s := newStore(t).Strict(true)
	pinned := newKey(t)
	if err := s.PinForRun(named, authorized(pinned)); err != nil {
		t.Fatal(err)
	}
	// The user's known_hosts holds a different key under the host's IP only.
	remote := addr(t)
	other := newKey(t)
	line := knownhosts.Line([]string{remote.String()}, other) + "\n"
	if err := os.WriteFile(s.path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	var changed *ErrChanged
	if err := s.Callback(nil)(named, remote, pinned); !errors.As(err, &changed) {
		t.Fatalf("a different key recorded under the IP must refuse the pinned key, got %v", err)
	}
}

func TestPinnedCallbackRefusesARevokedKey(t *testing.T) {
	resetPins(t)
	s := newStore(t).Strict(true)
	pinned := newKey(t)
	if err := s.PinForRun(named, authorized(pinned)); err != nil {
		t.Fatal(err)
	}
	remote := addr(t)
	line := "@revoked " + knownhosts.Line([]string{remote.String()}, pinned) + "\n"
	if err := os.WriteFile(s.path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	err := s.Callback(nil)(named, remote, pinned)
	if err == nil || !strings.Contains(err.Error(), "revoked") {
		t.Fatalf("a revoked pinned key must be refused, got %v", err)
	}
}
