// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package hostkeys verifies the identity of a VM before BaryoVM talks to it.
//
// Until this existed the engine dialled with ssh.InsecureIgnoreHostKey, whose name is honest: it
// does not accept on first connect, it accepts on every connect. Anything that could answer on a
// VM's address, by ARP or DNS or an IP reassigned after a rebuild, received whatever BaryoVM was
// about to do next, which includes pg_dump output streamed over the session and the contents of a
// stack's config file.
//
// Ask on first use, then refuse on change:
//
//   - Unknown host: what happens is the Store's Mode. Ask hands the fingerprint to a confirm hook
//     (the cli layer prompts a person) and records the key only on a yes. Refuse is an error, for CI
//     and anything else with nobody to ask. Learn records it and carries on, which is how releases
//     before this one behaved and is still available for someone who wants it.
//   - Known host, same key: continue silently.
//   - Known host, different key: refuse, print both fingerprints, and name the one command that
//     accepts the new key for that host. There is deliberately no flag that turns the check off
//     everywhere, because the moment it exists it ends up in a CI script and the check is gone.
//
// The user's own ~/.ssh/known_hosts is consulted read-only as a second source: someone who has
// already SSHed to the box by hand has the right key there, and learning it from them is better
// than learning it from the network.
package hostkeys

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha1" // #nosec G505 -- OpenSSH hashed known_hosts entries are HMAC-SHA1, the format requires it
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// ErrChanged is returned when a host offers a key that differs from the recorded one. It carries
// both fingerprints so the caller can print them without re-deriving either.
type ErrChanged struct {
	Host    string
	Stored  string // fingerprint recorded earlier
	Offered string // fingerprint the host just presented
	Store   string // path of the file holding the recorded key
}

func (e *ErrChanged) Error() string {
	return fmt.Sprintf(
		"host key for %s changed\n"+
			"  stored   %s\n"+
			"  offered  %s\n"+
			"\n"+
			"A rebuilt VM is the ordinary reason for this, and so is someone answering on that\n"+
			"address who should not be. BaryoVM cannot tell the two apart, so it stops here.\n"+
			"\n"+
			"If you rebuilt the machine, drop the old key and let the next connection learn the\n"+
			"new one:\n"+
			"\n"+
			"  baryovm vm forget-key %s\n",
		e.Host, e.Stored, e.Offered, e.Host)
}

// Reason says why an unknown host was not trusted, so the message gives advice that works for that
// case. The zero value is ReasonNoTerminal.
type Reason string

const (
	// ReasonNoTerminal: nobody could be asked because there is no terminal (a pipe, CI).
	ReasonNoTerminal Reason = ""
	// ReasonJSON: nobody could be asked because output is JSON, which a program reads.
	ReasonJSON Reason = "json"
	// ReasonStrict: strict checking was requested by the flag or BARYOVM_STRICT_HOST_KEYS.
	ReasonStrict Reason = "strict"
	// ReasonRelease: a project release never trusts a new host, by prompt or otherwise.
	ReasonRelease Reason = "release"
	// ReasonDeclined: a person was asked and did not say yes.
	ReasonDeclined Reason = "declined"
	// ReasonFingerprint: the caller supplied the fingerprint it expected and the host offered another.
	ReasonFingerprint Reason = "fingerprint"
)

// ErrUnknownHost is returned for a host with no recorded key that was not trusted. It carries the
// offered fingerprint so an operator can pin it after checking it, rather than having to go and
// fetch it, and the Reason so the message and a program reading it know what to do next.
type ErrUnknownHost struct {
	Host    string
	Offered string
	Store   string
	Reason  Reason
	// Expected is the fingerprint the caller said to trust, set for ReasonFingerprint.
	Expected string
}

func (e *ErrUnknownHost) Error() string {
	record := fmt.Sprintf(
		"Check that fingerprint against the machine, then record it before this runs:\n"+
			"\n"+
			"  %s >> %s\n", Keyscan(e.Host), e.Store)
	learnAgain := "\n" +
		"To learn an unknown host's key on first use instead, as releases before this one did, set\n" +
		"BARYOVM_STRICT_HOST_KEYS=0 (or pass --strict-host-keys=false).\n"
	switch e.Reason {
	case ReasonDeclined:
		return fmt.Sprintf(
			"host key for %s not trusted.\n"+
				"  offered  %s\n"+
				"\n"+
				"Nothing was recorded. To trust it later, check that fingerprint against the machine and\n"+
				"record it:\n"+
				"\n"+
				"  %s >> %s\n",
			e.Host, e.Offered, Keyscan(e.Host), e.Store)
	case ReasonFingerprint:
		return fmt.Sprintf(
			"host key for %s does not match the fingerprint you gave.\n"+
				"  expected %s\n"+
				"  offered  %s\n"+
				"\n"+
				"Nothing was recorded. Whatever answered is not the machine whose key you checked.\n",
			e.Host, e.Expected, e.Offered)
	case ReasonStrict:
		return fmt.Sprintf(
			"no recorded host key for %s, and strict host key checking was requested (--strict-host-keys or\n"+
				"BARYOVM_STRICT_HOST_KEYS).\n"+
				"  offered  %s\n"+
				"\n"+
				"Strict checking refuses an unknown host without asking.\n"+
				"\n"+
				"%s"+
				"\n"+
				"To be asked instead, unset BARYOVM_STRICT_HOST_KEYS and drop --strict-host-keys.\n",
			e.Host, e.Offered, record)
	case ReasonRelease:
		return fmt.Sprintf(
			"no recorded host key for %s, and a project release never trusts a new host.\n"+
				"  offered  %s\n"+
				"\n"+
				"It does not ask and it does not learn. Check that fingerprint against the machine and pin\n"+
				"it as hostKey for that VM in the project file, or record it in known_hosts and pass\n"+
				"--strict-host-keys:\n"+
				"\n"+
				"  %s >> %s\n",
			e.Host, e.Offered, Keyscan(e.Host), e.Store)
	case ReasonJSON:
		return fmt.Sprintf(
			"no recorded host key for %s, and output is JSON, so nobody can be asked.\n"+
				"  offered  %s\n"+
				"\n"+
				"%s"+
				"%s",
			e.Host, e.Offered, record, learnAgain)
	}
	return fmt.Sprintf(
		"no recorded host key for %s, and there is no terminal to ask on.\n"+
			"  offered  %s\n"+
			"\n"+
			"An unknown host is refused here, because nobody can check its key. A runner with no memory\n"+
			"between runs would otherwise trust whatever answers on this address, every run.\n"+
			"\n"+
			"%s"+
			"%s",
		e.Host, e.Offered, record, learnAgain)
}

// Mode is what a Store does with a host that has no recorded key anywhere.
type Mode int

const (
	// Learn records the key and carries on (trust on first use). It is the zero value because that is
	// what a Store did before modes existed; the cli layer picks the real default.
	Learn Mode = iota
	// Refuse returns ErrUnknownHost without asking.
	Refuse
	// Ask calls the Store's confirm hook. No hook, or an answer other than yes, refuses.
	Ask
)

// Keyscan is the ssh-keyscan command that fetches a host's ecdsa, ed25519 and rsa keys. ssh-keyscan
// takes the port as -p and cannot resolve host:port.
func Keyscan(hostport string) string {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		return "ssh-keyscan -t ecdsa,ed25519,rsa " + hostport
	}
	return "ssh-keyscan -p " + port + " -t ecdsa,ed25519,rsa " + host
}

// Store is a known_hosts file BaryoVM owns, plus the user's own file as a read-only second source.
type Store struct {
	path      string // BaryoVM's own known_hosts, written here
	extraPath string // the user's ~/.ssh/known_hosts, read but never written

	// mode decides an unknown host. Trust on first use assumes there is a first use: one moment where
	// a key is learned and every connection after it is checked. A CI runner has no memory between
	// runs, so every run is the first one, and TOFU degrades to accepting whatever answers on the
	// recorded address, every time, with a key that can dump the production database. Refuse is how
	// CI says "I already know this key, and if you do not, stop".
	mode Mode

	// confirm is asked in Ask mode. It must not be called with the store locked by the caller, and it
	// may block for as long as a person takes to read a fingerprint.
	confirm func(host, fingerprint string) bool

	// refuseReason is what a Refuse (or an Ask with no hook) reports as the reason.
	refuseReason Reason

	// expect is a fingerprint the caller already checked. An unknown host offering exactly that key is
	// trusted without asking, in any mode. Empty means none.
	expect string
}

// New returns the store rooted at dir, which is BaryoVM's home.
func New(dir string) *Store {
	s := &Store{path: filepath.Join(dir, "known_hosts")}
	if home, err := os.UserHomeDir(); err == nil {
		s.extraPath = filepath.Join(home, ".ssh", "known_hosts")
	}
	return s
}

// Strict makes an unknown host an error rather than something to learn. Set it wherever there is no
// human to check a fingerprint and no memory between runs. Strict(false) is Mode(Learn).
func (s *Store) Strict(on bool) *Store {
	if on {
		s.mode = Refuse
	} else {
		s.mode = Learn
	}
	return s
}

// Mode sets what happens to an unknown host.
func (s *Store) Mode(m Mode) *Store {
	s.mode = m
	return s
}

// RefuseReason sets the Reason carried by an unknown-host refusal that nobody was asked about.
func (s *Store) RefuseReason(r Reason) *Store {
	s.refuseReason = r
	return s
}

// ExpectFingerprint trusts an unknown host whose offered key has exactly this SHA256 fingerprint,
// without asking and in any mode, and refuses one that offers another. It never applies to a host
// that already has a recorded key: a changed key is refused whatever fingerprint is given.
func (s *Store) ExpectFingerprint(fp string) *Store {
	s.expect = fp
	return s
}

// Confirm sets the hook Ask mode calls with the host and the offered fingerprint. Only a true answer
// trusts the key. The hostkeys package never prints or reads input, so whoever sets this does that.
func (s *Store) Confirm(fn func(host, fingerprint string) bool) *Store {
	s.confirm = fn
	return s
}

// Callback returns an ssh.HostKeyCallback implementing the Store's mode for unknown hosts.
//
// learned is called with the fingerprint when a key is recorded for the first time in Learn mode, so
// the caller can tell the operator what it just trusted. It is not called when the key was already
// known, nor in Ask mode, where the person has just seen the fingerprint.
func (s *Store) Callback(learned func(host, fingerprint string)) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		offered := ssh.FingerprintSHA256(key)

		// Only plain keys are recorded and compared. knownhosts answers a certificate with an error
		// that is neither "absent" nor "changed", which used to fall through to learning it, so any
		// self-signed certificate replaced the recorded key.
		if _, ok := key.(*ssh.Certificate); ok {
			return fmt.Errorf("%s offered a host certificate (%s); BaryoVM checks plain host keys only and will not trust it", hostname, key.Type())
		}

		// A key pinned for this run must also agree with what the machine already trusts or has
		// revoked, checked with the real hostname and address (an entry may be keyed by IP). An
		// unknown host in the stores is fine. Then the offered key must equal the pin. Nothing is
		// learned or written.
		if pk := pinFor(hostname); pk != nil {
			// knownhosts looks up the hostname only when one is given, so the address is a second pass.
			names := []string{hostname}
			if remote != nil && remote.String() != hostname {
				names = append(names, remote.String())
			}
			for _, path := range []string{s.path, s.extraPath} {
				for _, name := range names {
					if err := s.check(path, name, remote, key); err != nil {
						if err := s.refusal(err, path, hostname, offered); err != nil {
							return err
						}
					}
				}
			}
			if bytes.Equal(pk.Marshal(), key.Marshal()) {
				return nil
			}
			return &ErrChanged{Host: hostname, Stored: ssh.FingerprintSHA256(pk), Offered: offered, Store: "the project file"}
		}

		// Our own file first. A key we recorded is the one we mean to check against.
		err := s.check(s.path, hostname, remote, key)
		if err == nil {
			return nil
		}
		if err := s.refusal(err, s.path, hostname, offered); err != nil {
			return err
		}

		// Not in ours. The user's own file is a better source than the network: if they have
		// SSHed to this box by hand, that key was checked by them, not offered to us now.
		if s.extraPath != "" {
			err := s.check(s.extraPath, hostname, remote, key)
			if err == nil {
				// Record it in ours too, so the trust does not depend on a file we do not own.
				return s.add(hostname, key)
			}
			if err := s.refusal(err, s.extraPath, hostname, offered); err != nil {
				return err
			}
		}

		// Unknown everywhere: the fresh VM case.
		if s.expect != "" {
			if s.expect != offered {
				return &ErrUnknownHost{Host: hostname, Offered: offered, Store: s.path, Reason: ReasonFingerprint, Expected: s.expect}
			}
			return s.add(hostname, key)
		}
		switch s.mode {
		case Refuse:
			return &ErrUnknownHost{Host: hostname, Offered: offered, Store: s.path, Reason: s.refuseReason}
		case Ask:
			// A missing hook means nobody can be asked, which is a refusal, never a yes.
			if s.confirm == nil {
				return &ErrUnknownHost{Host: hostname, Offered: offered, Store: s.path, Reason: s.refuseReason}
			}
			// One prompt at a time across every dial in the process. A second dial to the same new host
			// waits here, and then finds the first answer already recorded instead of asking again.
			askMu.Lock()
			defer askMu.Unlock()
			if err := s.check(s.path, hostname, remote, key); err == nil {
				return nil
			} else if err := s.refusal(err, s.path, hostname, offered); err != nil {
				return err
			}
			if !s.confirm(hostname, offered) {
				return &ErrUnknownHost{Host: hostname, Offered: offered, Store: s.path, Reason: ReasonDeclined}
			}
			return s.add(hostname, key)
		}
		// Learn it.
		if err := s.add(hostname, key); err != nil {
			return err
		}
		if learned != nil {
			learned(hostname, offered)
		}
		return nil
	}
}

// refusal turns a failed check into the error to return, or nil when the host is simply absent from
// path. Everything that is not "absent" refuses: a file that cannot be read may hold the very key that
// would have refused this one, so learning past it fails open.
func (s *Store) refusal(err error, path, hostname, offered string) error {
	var re *knownhosts.RevokedError
	switch {
	case errors.Is(err, errNotFound):
		return nil
	case errors.As(err, &re):
		return fmt.Errorf("host key %s for %s is marked @revoked in %s", offered, hostname, path)
	case isChanged(err):
		stored, _ := s.storedFingerprint(path, hostname)
		return &ErrChanged{Host: hostname, Stored: stored, Offered: offered, Store: path}
	}
	return fmt.Errorf("check the host key for %s against %s: %w; fix or remove the bad line before connecting", hostname, path, err)
}

// PlainAlgorithms is every host key algorithm the client supports except certificates, for
// ssh.ClientConfig.HostKeyAlgorithms. Callback refuses certificates, so offering them would make a
// host that has both a certificate and a plain key unreachable.
func PlainAlgorithms() []string {
	var out []string
	for _, a := range ssh.SupportedAlgorithms().HostKeys {
		if !strings.Contains(a, "-cert-") {
			out = append(out, a)
		}
	}
	return out
}

// Algorithms returns the host key algorithms of the keys recorded for a host, for
// ssh.ClientConfig.HostKeyAlgorithms. Without it the server offers whichever key type the client
// prefers, which may not be the one recorded, and a recorded ED25519 key then looks like a changed
// host key when the server offers ECDSA.
//
// The recorded types come first and the rest of the supported ones follow, so the list only
// reorders preference. A server holding the recorded type offers it. A server without it offers
// another type, and Callback reports that as a changed key instead of the handshake failing with
// "no common algorithm". Callback still checks the key itself.
//
// BaryoVM's own file wins when it has the host, the same order Callback checks in; the user's file
// is used only when ours has nothing. A host with no recorded key gets PlainAlgorithms, so a fresh
// VM can still be learned. Certificate types are never offered: Callback refuses certificates.
func (s *Store) Algorithms(hostname string) []string {
	if pk := pinFor(hostname); pk != nil {
		return keyAlgorithms(pk)
	}
	for _, path := range []string{s.path, s.extraPath} {
		stored := algorithmsIn(path, hostname)
		if len(stored) == 0 {
			continue
		}
		supported := PlainAlgorithms()
		var algs []string
		seen := map[string]bool{}
		for _, a := range stored {
			if slices.Contains(supported, a) && !seen[a] {
				seen[a] = true
				algs = append(algs, a)
			}
		}
		if len(algs) == 0 {
			return supported // only types Go cannot check as a host key; Callback will report it
		}
		for _, a := range supported {
			if !seen[a] {
				seen[a] = true
				algs = append(algs, a)
			}
		}
		return algs
	}
	return PlainAlgorithms()
}

// algorithmsIn reads one known_hosts file. Lines it cannot parse, and @revoked and @cert-authority
// entries (which are not host keys to offer), are skipped. A missing file has no algorithms.
func algorithmsIn(path, hostname string) []string {
	if path == "" {
		return nil
	}
	b, err := os.ReadFile(path) // #nosec G304 -- known_hosts path chosen by the user or the store
	if err != nil {
		return nil
	}
	want := knownhosts.Normalize(hostname)
	var algs []string
	seen := map[string]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		marker, hosts, key, _, _, err := ssh.ParseKnownHosts([]byte(line))
		if err != nil || marker != "" || !hostMatches(hosts, want) {
			continue
		}
		for _, a := range keyAlgorithms(key) {
			if !seen[a] {
				seen[a] = true
				algs = append(algs, a)
			}
		}
	}
	return algs
}

// hostMatches reports whether any host field of a known_hosts line is the wanted, normalized name.
// A hashed field (|1|salt|hash) cannot be compared as text, so it is re-hashed with its own salt.
func hostMatches(hosts []string, want string) bool {
	for _, h := range hosts {
		if strings.HasPrefix(h, "|1|") {
			if hashedMatches(h, want) {
				return true
			}
			continue
		}
		if h == want || knownhosts.Normalize(h) == want {
			return true
		}
	}
	return false
}

func hashedMatches(field, want string) bool {
	parts := strings.Split(field, "|")
	if len(parts) != 4 {
		return false
	}
	salt, err := base64.StdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	sum, err := base64.StdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	mac := hmac.New(sha1.New, salt)
	mac.Write([]byte(want))
	return hmac.Equal(mac.Sum(nil), sum)
}

// keyAlgorithms maps a stored key to the signature algorithms a server may use for it. An RSA key
// can be proven with any of three, newest first; every other type has one.
func keyAlgorithms(key ssh.PublicKey) []string {
	if key.Type() == ssh.KeyAlgoRSA {
		return []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}
	}
	return []string{key.Type()}
}

// check runs one known_hosts file's callback. A missing file is "not found", not an error: a first
// run has no file yet, and refusing there would make the fresh VM case impossible.
func (s *Store) check(path, hostname string, remote net.Addr, key ssh.PublicKey) error {
	if path == "" {
		return errNotFound
	}
	if _, err := os.Stat(path); err != nil {
		return errNotFound
	}
	cb, err := knownhosts.New(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err := cb(hostname, remote, key); err != nil {
		var ke *knownhosts.KeyError
		if errors.As(err, &ke) && len(ke.Want) == 0 {
			return errNotFound // known_hosts parsed fine, this host is simply absent
		}
		return err
	}
	return nil
}

var errNotFound = errors.New("host not found in known_hosts")

// isChanged reports whether err says the host is known and offered a different key. A KeyError with
// entries in Want is the mismatch case; one with none is "absent", which is not a mismatch.
func isChanged(err error) bool {
	if errors.Is(err, errNotFound) {
		return false
	}
	var ke *knownhosts.KeyError
	return errors.As(err, &ke) && len(ke.Want) > 0
}

// storedFingerprint reads back the fingerprint recorded for a host, for the error message.
func (s *Store) storedFingerprint(path, hostname string) (string, error) {
	b, err := os.ReadFile(path) // #nosec G304 -- known_hosts path chosen by the user or the store
	if err != nil {
		return "", err
	}
	want := knownhosts.Normalize(hostname)
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		_, hosts, key, _, _, err := ssh.ParseKnownHosts([]byte(line))
		if err != nil {
			continue
		}
		for _, h := range hosts {
			if h == want || knownhosts.Normalize(h) == want {
				return ssh.FingerprintSHA256(key), nil
			}
		}
	}
	return "", errNotFound
}

// askMu serializes prompts. addMu makes the check-then-append in add one step, so two dials that both
// learn the same host (the Learn mode, or a prompt answered for another host) write one line.
var (
	askMu sync.Mutex
	addMu sync.Mutex
)

// add appends a host key to BaryoVM's own known_hosts, 0600 the same way fleet.json is written. A key
// already recorded for the host is not written twice.
func (s *Store) add(hostname string, key ssh.PublicKey) error {
	addMu.Lock()
	defer addMu.Unlock()
	if err := s.check(s.path, hostname, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 22}, key); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(s.path), err)
	}
	line := knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key) + "\n"
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open %s: %w", s.path, err)
	}
	defer f.Close()
	if _, err := f.WriteString(line); err != nil {
		return fmt.Errorf("write %s: %w", s.path, err)
	}
	return nil
}

// sessionPins are host keys a project file names for this run only. They are never written to a
// known_hosts file: a committed file must not change what this machine trusts for good. Guarded
// because dials can run concurrently.
var (
	pinMu       sync.RWMutex
	sessionPins = map[string]ssh.PublicKey{}
)

func pinFor(hostport string) ssh.PublicKey {
	pinMu.RLock()
	defer pinMu.RUnlock()
	return sessionPins[knownhosts.Normalize(hostport)]
}

// ResetSessionPins forgets every key pinned for the run.
func ResetSessionPins() {
	pinMu.Lock()
	sessionPins = map[string]ssh.PublicKey{}
	pinMu.Unlock()
}

// PinForRun trusts a host key given in authorized_keys form ("ssh-ed25519 AAAA...", a comment is
// ignored) for hostport until ResetSessionPins, so a runner with no memory checks the key a project
// file names instead of learning whatever answers. Nothing is written to disk.
//
// A pin that contradicts a key either persistent store records for the host (BaryoVM's own file or
// the user's ~/.ssh/known_hosts) is refused as a changed key: a committed file does not get to
// override a key somebody already checked.
func (s *Store) PinForRun(hostport, authorized string) error {
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(authorized))
	if err != nil {
		return fmt.Errorf("host key for %s is not an authorized_keys line (type, base64 key): %w", hostport, err)
	}
	if _, ok := key.(*ssh.Certificate); ok {
		return fmt.Errorf("host key for %s is a certificate; BaryoVM checks plain host keys only", hostport)
	}
	remote := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 22}
	for _, path := range []string{s.path, s.extraPath} {
		err := s.check(path, hostport, remote, key)
		if err == nil {
			continue
		}
		if err := s.refusal(err, path, hostport, ssh.FingerprintSHA256(key)); err != nil {
			return err
		}
	}
	pinMu.Lock()
	sessionPins[knownhosts.Normalize(hostport)] = key
	pinMu.Unlock()
	return nil
}

// Forget removes every recorded key for a host from BaryoVM's own file, so the next connection
// learns the new one. It reports how many entries it dropped, so "forgot 0 keys" is visible rather
// than looking like success.
func (s *Store) Forget(hostname string) (int, error) {
	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("read %s: %w", s.path, err)
	}
	want := knownhosts.Normalize(hostname)
	var kept []string
	dropped := 0
	for _, line := range strings.Split(string(b), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if !strings.HasPrefix(trimmed, "#") {
			_, hosts, _, _, _, err := ssh.ParseKnownHosts([]byte(trimmed))
			if err == nil {
				match := false
				for _, h := range hosts {
					if h == want || knownhosts.Normalize(h) == want {
						match = true
						break
					}
				}
				if match {
					dropped++
					continue
				}
			}
		}
		kept = append(kept, line)
	}
	if dropped == 0 {
		return 0, nil
	}
	out := strings.Join(kept, "\n")
	if out != "" && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	if err := os.WriteFile(s.path, []byte(out), 0o600); err != nil { // #nosec G703 -- s.path is the store file, not request input
		return 0, fmt.Errorf("write %s: %w", s.path, err)
	}
	return dropped, nil
}

// Path is where BaryoVM records the keys it has learned.
func (s *Store) Path() string { return s.path }

// KnownHostsFor returns the known_hosts lines recorded for one host:port, ready to write to a file
// another tool (rsync's ssh) will read. BaryoVM's own file wins; the user's file is used only when
// ours has nothing, the same order Callback checks in. Only plain keys are returned, never
// @revoked or @cert-authority entries. An empty result means the host is not known yet.
func (s *Store) KnownHostsFor(hostport string) (string, error) {
	if pk := pinFor(hostport); pk != nil {
		return knownhosts.Line([]string{knownhosts.Normalize(hostport)}, pk) + "\n", nil
	}
	for _, path := range []string{s.path, s.extraPath} {
		if path == "" {
			continue
		}
		b, err := os.ReadFile(path) // #nosec G304 -- known_hosts path chosen by the user or the store
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", fmt.Errorf("read %s: %w", path, err)
		}
		want := knownhosts.Normalize(hostport)
		var out strings.Builder
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			marker, hosts, _, _, _, err := ssh.ParseKnownHosts([]byte(line))
			if err != nil || marker != "" || !hostMatches(hosts, want) {
				continue
			}
			out.WriteString(line + "\n")
		}
		if out.Len() > 0 {
			return out.String(), nil
		}
	}
	return "", nil
}
