// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package sshx is BaryoVM's agentless SSH engine: dial a host with a key and
// run commands. Everything BaryoVM does to a VM (install Docker, run
// containers) goes through here, so there is no per-VM agent to install.
package sshx

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/BaryoDev/BaryoVM/internal/hostkeys"
)

// home is where BaryoVM keeps its own state, the same rule fleet uses: BARYOVM_HOME if set, else
// ~/.baryovm. Duplicated rather than imported because fleet imports nothing from here and the
// dependency direction in CLAUDE.md runs one way.
func home() string {
	if d := os.Getenv("BARYOVM_HOME"); d != "" {
		return d
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".baryovm")
}

// strictOverride is what the --strict-host-keys flag sets, so the flag wins over the environment.
var strictOverride *bool

// interactive is set by the cli layer when a person can be asked: stdin and stderr are terminals and
// output is not JSON. sshx cannot look, because the terminal and the output mode live in ui and the
// dependency direction in CLAUDE.md runs one way. Unset means nobody can be asked.
var interactive bool

// SetStrictHostKeys is how the cli layer applies its flag.
func SetStrictHostKeys(on bool) { strictOverride = &on }

// SetInteractive tells sshx whether a person can be asked about an unknown host key.
func SetInteractive(on bool) { interactive = on }

// ForceRefuse makes every unknown host a refusal until the returned function is called, whatever the
// flag, the environment or the terminal say. A release uses it so that no host is ever trusted on
// first use during one, by prompt or otherwise.
func ForceRefuse() (restore func()) {
	prev := strictOverride
	on := true
	strictOverride = &on
	return func() { strictOverride = prev }
}

// strictSetting reports an explicit choice: 1 strict (refuse), 0 learn on first use, -1 nothing set.
// The flag wins over the environment. BARYOVM_STRICT_HOST_KEYS is what a CI job sets once for the
// whole workflow, read here rather than passed down because it has to reach a dial from every
// command without threading a parameter through six engine packages.
func strictSetting() int {
	if strictOverride != nil {
		if *strictOverride {
			return 1
		}
		return 0
	}
	switch strings.ToLower(os.Getenv("BARYOVM_STRICT_HOST_KEYS")) {
	case "1", "true", "yes", "on":
		return 1
	case "0", "false", "no", "off":
		return 0
	}
	return -1
}

// resolveMode is the rule for an unknown host. An explicit strict setting refuses, an explicit
// non-strict one learns on first use, and with nothing set a person is asked when there is one (and
// something to ask them with) and the host is refused when there is not.
func resolveMode(setting int, canAsk bool) hostkeys.Mode {
	switch setting {
	case 1:
		return hostkeys.Refuse
	case 0:
		return hostkeys.Learn
	}
	if canAsk {
		return hostkeys.Ask
	}
	return hostkeys.Refuse
}

// HostKeyMode resolves the flag, then the environment, then whether a person can be asked.
func HostKeyMode() hostkeys.Mode {
	return resolveMode(strictSetting(), interactive && ConfirmHostKey != nil)
}

// StrictHostKeys reports whether an unknown host will be refused without asking. With nothing set
// that is true when there is no terminal, so it is not the same as "strict was requested"; see
// StrictHostKeysRequested.
func StrictHostKeys() bool { return HostKeyMode() == hostkeys.Refuse }

// StrictHostKeysRequested reports whether the flag or BARYOVM_STRICT_HOST_KEYS asked for strict
// checking. A project release needs this, not the default: it must have a pin or an explicit strict
// request before it connects.
func StrictHostKeysRequested() bool { return strictSetting() == 1 }

// OnLearnHostKey is called the first time a host key is recorded, so the operator sees the
// fingerprint they are now trusting. The cli layer sets it; sshx does not print (see CLAUDE.md on
// the dependency direction), so a nil hook means the learning is silent.
var OnLearnHostKey func(host, fingerprint string)

// ConfirmHostKey asks whether to trust an unknown host's key, and returns true only for a yes. The
// cli layer sets it and does the prompting; sshx never prints or reads input. It may block for as
// long as a person takes, and Dial keeps that time out of the handshake deadline. A nil hook means an
// unknown host cannot be asked about and is refused.
var ConfirmHostKey func(host, fingerprint string) bool

func learned(host, fingerprint string) {
	if OnLearnHostKey != nil {
		OnLearnHostKey(host, fingerprint)
	}
}

// Target is a host to connect to.
type Target struct {
	Host    string
	Port    int
	User    string
	KeyPath string
}

// Client is a live SSH connection.
type Client struct{ c *ssh.Client }

// Dial connects to the target using the private key at KeyPath.
func Dial(t Target) (*Client, error) {
	if t.Port == 0 {
		t.Port = 22
	}
	signer, closeAgent, err := loadSigner(t.KeyPath)
	if err != nil {
		return nil, err
	}
	defer closeAgent() // signing happens during the handshake, so the agent is done once Dial returns
	addr := net.JoinHostPort(t.Host, fmt.Sprintf("%d", t.Port))
	guard := &handshakeGuard{}
	store := hostkeys.New(home()).Mode(HostKeyMode())
	if confirm := ConfirmHostKey; confirm != nil {
		// The handshake deadline is for a host that stalls, not for a person reading a fingerprint.
		store.Confirm(func(host, fp string) bool {
			var ok bool
			guard.pause(func() { ok = confirm(host, fp) })
			return ok
		})
	}
	cfg := &ssh.ClientConfig{
		User:            t.User,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: store.Callback(learned),
		// Ask for the key types already recorded first, so a server holding several offers one we
		// can check rather than one that looks like a changed key.
		HostKeyAlgorithms: store.Algorithms(addr),
		Timeout:           15 * time.Second,
	}
	c, err := dialGuarded(addr, cfg, handshakeTimeout, guard)
	if err != nil {
		return nil, fmt.Errorf("ssh dial %s: %w", addr, err)
	}
	go keepAlive(c, keepAliveInterval, keepAliveReplyTimeout)
	return &Client{c: c}, nil
}

// handshakeTimeout bounds the SSH handshake. ClientConfig.Timeout covers only the TCP connect, so a
// host that accepts the connection and never sends a banner would otherwise hang Dial forever.
var handshakeTimeout = 30 * time.Second

// keepAliveInterval and keepAliveReplyTimeout decide when a VM that stopped answering is given up
// on: a keepalive goes out every interval and must be answered within the reply timeout.
var (
	keepAliveInterval     = 30 * time.Second
	keepAliveReplyTimeout = 20 * time.Second
)

// handshakeGuard lets the handshake deadline be set aside while something waits on a person.
type handshakeGuard struct {
	mu        sync.Mutex
	conn      net.Conn
	handshake time.Duration
}

func (g *handshakeGuard) set(conn net.Conn, handshake time.Duration) {
	g.mu.Lock()
	g.conn, g.handshake = conn, handshake
	g.mu.Unlock()
}

// pause clears the connection deadline, runs fn, then gives the rest of the handshake a fresh full
// window. The time fn takes is not counted against the deadline.
func (g *handshakeGuard) pause(fn func()) {
	g.mu.Lock()
	conn, handshake := g.conn, g.handshake
	g.mu.Unlock()
	if conn != nil {
		_ = conn.SetDeadline(time.Time{})
	}
	defer func() {
		if conn != nil {
			_ = conn.SetDeadline(time.Now().Add(handshake))
		}
	}()
	fn()
}

// dialWithHandshakeDeadline is ssh.Dial with a deadline on the connection until the handshake is
// done, then cleared so a long command is not cut off.
func dialWithHandshakeDeadline(addr string, cfg *ssh.ClientConfig, handshake time.Duration) (*ssh.Client, error) {
	return dialGuarded(addr, cfg, handshake, &handshakeGuard{})
}

// dialGuarded is dialWithHandshakeDeadline whose deadline guard can be paused by a host key prompt.
func dialGuarded(addr string, cfg *ssh.ClientConfig, handshake time.Duration, guard *handshakeGuard) (*ssh.Client, error) {
	conn, err := net.DialTimeout("tcp", addr, cfg.Timeout)
	if err != nil {
		return nil, err
	}
	if err := conn.SetDeadline(time.Now().Add(handshake)); err != nil {
		_ = conn.Close()
		return nil, err
	}
	guard.set(conn, handshake)
	sc, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		_ = sc.Close()
		return nil, err
	}
	return ssh.NewClient(sc, chans, reqs), nil
}

// keepAliver is the part of *ssh.Client keepAlive needs.
type keepAliver interface {
	SendRequest(name string, wantReply bool, payload []byte) (bool, []byte, error)
	Close() error
	Wait() error
}

// keepAlive sends keepalive@openssh.com every interval and closes the connection when one is not
// answered within reply (or fails). Closing makes every pending Run return an error instead of
// waiting on a VM that dropped off the network. It ends when the connection does.
func keepAlive(c keepAliver, interval, reply time.Duration) {
	done := make(chan struct{})
	go func() {
		_ = c.Wait()
		close(done)
	}()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
		}
		res := make(chan error, 1)
		go func() {
			_, _, err := c.SendRequest("keepalive@openssh.com", true, nil)
			res <- err
		}()
		select {
		case err := <-res:
			if err != nil {
				_ = c.Close()
				return
			}
		case <-time.After(reply):
			_ = c.Close()
			return
		case <-done:
			return
		}
	}
}

// IsUnreachable reports whether a Dial error means no connection was made: nothing answered, the
// address did not resolve, or it timed out. A host key refusal is not that. The host did answer, and
// with a key that is not the one on record, so it must stay an error for the caller to act on.
func IsUnreachable(err error) bool {
	var changed *hostkeys.ErrChanged
	var unknown *hostkeys.ErrUnknownHost
	if errors.As(err, &changed) || errors.As(err, &unknown) {
		return false
	}
	var ne net.Error
	return errors.As(err, &ne)
}

// Runner runs one command on a VM. *Client is the real one; an engine package takes this rather
// than the struct so its logic can be driven without a host, the same reason update.Runner exists a
// layer up. It is the whole of the connection an engine needs: dialling stays at the cli edge.
type Runner interface {
	Run(cmd string) (string, error)
}

// StdinRunner is a Runner that can also stream a reader into the command's stdin. The tar
// transport uses it to feed an archive to `tar -x` as it is written, never holding it whole.
type StdinRunner interface {
	Runner
	RunStdin(cmd string, stdin io.Reader) (string, error)
}

var _ StdinRunner = (*Client)(nil)

// RunStdin runs a command with stdin read from in, and returns its stdout. The reader is copied
// as the command consumes it. A non-zero exit is an error carrying stderr, as in Run.
func (c *Client) RunStdin(cmd string, in io.Reader) (string, error) {
	sess, err := c.c.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()
	var out, errb bytes.Buffer
	sess.Stdin = in
	sess.Stdout = &out
	sess.Stderr = &errb
	if err := sess.Run(cmd); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return out.String(), fmt.Errorf("remote `%s`: %w: %s", cmd, err, msg)
	}
	return out.String(), nil
}

// Run executes a command and returns its stdout, or an error carrying stderr.
func (c *Client) Run(cmd string) (string, error) {
	cap, err := c.RunCapture(cmd)
	if err != nil {
		return cap.Stdout, err
	}
	if cap.ExitCode != 0 {
		msg := strings.TrimSpace(cap.Stderr)
		if msg == "" {
			msg = fmt.Sprintf("exit status %d", cap.ExitCode)
		}
		if cap.ExitErr != nil {
			return cap.Stdout, fmt.Errorf("remote `%s`: %w: %s", cmd, cap.ExitErr, msg)
		}
		return cap.Stdout, fmt.Errorf("remote `%s`: %s", cmd, msg)
	}
	return cap.Stdout, nil
}

// Capture is the stdout, stderr and exit code of one remote command.
// ExitErr is the *ssh.ExitError when ExitCode != 0, so callers of Run can
// still unwrap it after the Capture split.
type Capture struct {
	Stdout   string
	Stderr   string
	ExitCode int
	ExitErr  error
}

// RunCapture runs a command and always returns its streams. A non-zero remote
// exit is reported in Capture.ExitCode with a nil error; error is reserved for
// dial/session failures where no remote process ran.
func (c *Client) RunCapture(cmd string) (Capture, error) {
	return c.runCapture(cmd, nil)
}

// RunInput runs a command with stdin as its standard input and returns its stdout, or an error
// carrying stderr. It exists so a secret can reach a remote process without ever being part of the
// command line, where `ps` on the VM would show it.
//
// The error echoes cmd and the remote stderr, as Run does. cmd is safe to echo because the secret
// is in stdin; stderr is the caller's to redact, since only the caller knows what it sent.
func (c *Client) RunInput(cmd string, stdin string) (string, error) {
	cap, err := c.runCapture(cmd, strings.NewReader(stdin))
	if err != nil {
		return cap.Stdout, err
	}
	if cap.ExitCode != 0 {
		msg := strings.TrimSpace(cap.Stderr)
		if msg == "" {
			msg = fmt.Sprintf("exit status %d", cap.ExitCode)
		}
		return cap.Stdout, fmt.Errorf("remote `%s`: %w: %s", cmd, cap.ExitErr, msg)
	}
	return cap.Stdout, nil
}

func (c *Client) runCapture(cmd string, stdin io.Reader) (Capture, error) {
	sess, err := c.c.NewSession()
	if err != nil {
		return Capture{}, err
	}
	defer sess.Close()
	var out, errb bytes.Buffer
	sess.Stdout = &out
	sess.Stderr = &errb
	if stdin != nil {
		sess.Stdin = stdin
	}
	err = sess.Run(cmd)
	cap := Capture{Stdout: out.String(), Stderr: errb.String()}
	if err == nil {
		return cap, nil
	}
	if ee, ok := err.(*ssh.ExitError); ok {
		cap.ExitCode = ee.ExitStatus()
		cap.ExitErr = ee
		return cap, nil
	}
	return cap, fmt.Errorf("remote `%s`: %w: %s", cmd, err, strings.TrimSpace(errb.String()))
}

// Close ends the connection.
func (c *Client) Close() error { return c.c.Close() }

// Quote single-quotes an argument so it is safe to embed in a remote shell command.
func Quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// Sudo prefixes a remote command this code built itself with non-interactive sudo, and returns it
// untouched when the caller did not ask for root.
//
// One function writes the prefix. Compose, backup and release each used to write "sudo -n " for
// themselves, and a stack's Sudo setting reached some of those call sites and not others: an image
// build ran as the SSH user while the compose up next to it ran as root, and the release hooks had
// the same gap. One helper is not tidiness here, it is what stops a fifth remote command being
// built without one.
//
// The -n is not optional. Under -o json there is no terminal to answer a password prompt, so a bare
// sudo hangs until the session times out; -n fails immediately and says why.
func Sudo(sudo bool, cmd string) string {
	if !sudo {
		return cmd
	}
	return "sudo -n " + cmd
}

// SudoShell is Sudo for a command that came from a manifest rather than from this code.
//
// It runs the whole string under one root shell, the same shell an unelevated hook would have got. A bare prefix would cover only the first command
// of "nginx -t && systemctl reload nginx" and leave the reload to fail on its own, which is the
// half-applied failure these hooks keep producing.
//
// A command that already says sudo is wrapped too, and that is deliberate. Sudo inside sudo is
// harmless: the inner one is already root, so it authorises and execs. Skipping such a string is
// not harmless, because "sudo -n nginx -t && systemctl reload nginx" starts with sudo and is still
// half unprivileged, and a hook written with the prefix by hand is the most likely one to be
// compound, since writing it that way was the workaround for this bug.
func SudoShell(sudo bool, cmd string) string {
	if !sudo {
		return cmd
	}
	// $SHELL, not sh. An unelevated hook runs in the SSH login shell, which on these hosts is
	// bash; `sudo -n "${SHELL:-/bin/sh}" -c` is dash on Debian and Ubuntu, so a hook using [[ ]], source, arrays
	// or pipefail exits 127 where it worked before. Elevating a command should change who runs
	// it, not what language it is written in. The fallback covers a session with no SHELL set.
	return Sudo(true, `"${SHELL:-/bin/sh}" -c `+Quote(cmd))
}

// PublicKeyFromPrivate reads a private key and returns its OpenSSH public key
// line (authorized_keys form), so a provisioner can authorize the same key it
// will later connect with.
func PublicKeyFromPrivate(keyPath string) (string, error) {
	signer, closeAgent, err := loadSigner(keyPath)
	if err != nil {
		return "", err
	}
	defer closeAgent()
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))), nil
}

// loadSigner returns a signer for the key at keyPath. A key this library cannot read itself (one with
// a passphrase, or a hardware key whose file is only a handle) is used through ssh-agent instead,
// matched by the public key in keyPath.pub. The returned func releases the agent connection.
func loadSigner(keyPath string) (ssh.Signer, func(), error) {
	b, err := os.ReadFile(expand(keyPath))
	if err != nil {
		return nil, nil, fmt.Errorf("read key %s: %w", keyPath, err)
	}
	signer, parseErr := ssh.ParsePrivateKey(b)
	if parseErr == nil {
		return signer, func() {}, nil
	}
	signer, closeAgent, agentErr := agentSigner(expand(keyPath) + ".pub")
	if agentErr != nil {
		return nil, nil, fmt.Errorf("parse key %s: %w (ssh-agent: %v)", keyPath, parseErr, agentErr)
	}
	return signer, closeAgent, nil
}

// agentSigner finds the key named by a .pub file in the running ssh-agent.
func agentSigner(pubPath string) (ssh.Signer, func(), error) {
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return nil, nil, errors.New("SSH_AUTH_SOCK is not set")
	}
	pubLine, err := os.ReadFile(pubPath) // #nosec G304 -- the .pub beside the key the operator configured
	if err != nil {
		return nil, nil, err
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey(pubLine)
	if err != nil {
		return nil, nil, fmt.Errorf("parse %s: %w", pubPath, err)
	}
	conn, err := net.Dial("unix", sock) // #nosec G704 -- the operator's own ssh-agent socket, a local unix path
	if err != nil {
		return nil, nil, err
	}
	closeConn := func() { _ = conn.Close() }
	signers, err := agent.NewClient(conn).Signers()
	if err != nil {
		closeConn()
		return nil, nil, err
	}
	want := pub.Marshal()
	for _, s := range signers {
		if bytes.Equal(s.PublicKey().Marshal(), want) {
			return s, closeConn, nil
		}
	}
	closeConn()
	return nil, nil, fmt.Errorf("%s is not loaded, run ssh-add", pubPath)
}

func expand(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}
