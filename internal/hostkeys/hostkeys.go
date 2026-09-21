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
// Trust on first use, then refuse on change:
//
//   - Unknown host: record the key, print the fingerprint, continue. This is the fresh VM case and
//     it is the one time a key can be learned without a second channel to check it against.
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
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

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

// ErrUnknownHost is returned in strict mode for a host with no recorded key. It carries the offered
// fingerprint so an operator can pin it after checking it, rather than having to go and fetch it.
type ErrUnknownHost struct {
	Host    string
	Offered string
	Store   string
}

func (e *ErrUnknownHost) Error() string {
	return fmt.Sprintf(
		"no recorded host key for %s, and strict host key checking is on.\n"+
			"  offered  %s\n"+
			"\n"+
			"Strict mode refuses to learn a key, because there is nobody here to check it. A runner\n"+
			"with no memory between runs would otherwise trust whatever answers on this address,\n"+
			"every run.\n"+
			"\n"+
			"Check that fingerprint against the machine, then record it before this runs:\n"+
			"\n"+
			"  ssh-keyscan -t ecdsa,ed25519 %s >> %s\n",
		e.Host, e.Offered, hostOnly(e.Host), e.Store)
}

// hostOnly strips the port, since ssh-keyscan takes a host and an optional -p.
func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return hostport
}

// Store is a known_hosts file BaryoVM owns, plus the user's own file as a read-only second source.
type Store struct {
	path      string // BaryoVM's own known_hosts, written here
	extraPath string // the user's ~/.ssh/known_hosts, read but never written

	// strict refuses to learn. An unknown host is an error rather than a key recorded on faith.
	//
	// Trust on first use assumes there is a first use: one moment where a key is learned and every
	// connection after it is checked. A CI runner has no memory between runs, so every run is the
	// first one, and TOFU degrades to accepting whatever answers on the recorded address, every
	// time, with a key that can dump the production database. Strict mode is how CI says "I already
	// know this key, and if you do not, stop".
	strict bool
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
// human to check a fingerprint and no memory between runs.
func (s *Store) Strict(on bool) *Store {
	s.strict = on
	return s
}

// Callback returns an ssh.HostKeyCallback implementing trust on first use.
//
// learned is called with the fingerprint when a key is recorded for the first time, so the caller
// can tell the operator what it just trusted. It is not called when the key was already known.
func (s *Store) Callback(learned func(host, fingerprint string)) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		offered := ssh.FingerprintSHA256(key)

		// Our own file first. A key we recorded is the one we mean to check against.
		err := s.check(s.path, hostname, remote, key)
		switch {
		case err == nil:
			return nil
		case isChanged(err):
			stored, _ := s.storedFingerprint(s.path, hostname)
			return &ErrChanged{Host: hostname, Stored: stored, Offered: offered, Store: s.path}
		}

		// Not in ours. The user's own file is a better source than the network: if they have
		// SSHed to this box by hand, that key was checked by them, not offered to us now.
		if s.extraPath != "" {
			err := s.check(s.extraPath, hostname, remote, key)
			switch {
			case err == nil:
				// Record it in ours too, so the trust does not depend on a file we do not own.
				if werr := s.add(hostname, key); werr != nil {
					return werr
				}
				return nil
			case isChanged(err):
				stored, _ := s.storedFingerprint(s.extraPath, hostname)
				return &ErrChanged{Host: hostname, Stored: stored, Offered: offered, Store: s.extraPath}
			}
		}

		// Unknown everywhere: the fresh VM case.
		if s.strict {
			return &ErrUnknownHost{Host: hostname, Offered: offered, Store: s.path}
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
	b, err := os.ReadFile(path)
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

// add appends a host key to BaryoVM's own known_hosts, 0600 the same way fleet.json is written.
func (s *Store) add(hostname string, key ssh.PublicKey) error {
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
	if err := os.WriteFile(s.path, []byte(out), 0o600); err != nil {
		return 0, fmt.Errorf("write %s: %w", s.path, err)
	}
	return dropped, nil
}

// Path is where BaryoVM records the keys it has learned.
func (s *Store) Path() string { return s.path }
