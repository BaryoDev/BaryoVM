// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package sshx is BaryoVM's agentless SSH engine: dial a host with a key and
// run commands. Everything BaryoVM does to a VM (install Docker, run
// containers) goes through here, so there is no per-VM agent to install.
package sshx

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

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
	key, err := os.ReadFile(expand(t.KeyPath))
	if err != nil {
		return nil, fmt.Errorf("read key %s: %w", t.KeyPath, err)
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("parse key %s: %w", t.KeyPath, err)
	}
	cfg := &ssh.ClientConfig{
		User: t.User,
		Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		// TODO(security): replace with a TOFU known_hosts store. A fresh VM has
		// no known host key yet, so we accept-on-first-connect for now.
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         15 * time.Second,
	}
	addr := net.JoinHostPort(t.Host, fmt.Sprintf("%d", t.Port))
	c, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		return nil, fmt.Errorf("ssh dial %s: %w", addr, err)
	}
	return &Client{c: c}, nil
}

// Runner runs one command on a VM. *Client is the real one; an engine package takes this rather
// than the struct so its logic can be driven without a host, the same reason update.Runner exists a
// layer up. It is the whole of the connection an engine needs: dialling stays at the cli edge.
type Runner interface {
	Run(cmd string) (string, error)
}

// Run executes a command and returns its stdout, or an error carrying stderr.
func (c *Client) Run(cmd string) (string, error) {
	sess, err := c.c.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()
	var out, errb bytes.Buffer
	sess.Stdout = &out
	sess.Stderr = &errb
	if err := sess.Run(cmd); err != nil {
		return out.String(), fmt.Errorf("remote `%s`: %w: %s", cmd, err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
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
// It runs the whole string under one root shell. A bare prefix would cover only the first command
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
	return Sudo(true, "sh -c "+Quote(cmd))
}

// PublicKeyFromPrivate reads a private key and returns its OpenSSH public key
// line (authorized_keys form), so a provisioner can authorize the same key it
// will later connect with.
func PublicKeyFromPrivate(keyPath string) (string, error) {
	b, err := os.ReadFile(expand(keyPath))
	if err != nil {
		return "", fmt.Errorf("read key %s: %w", keyPath, err)
	}
	signer, err := ssh.ParsePrivateKey(b)
	if err != nil {
		return "", fmt.Errorf("parse key %s: %w", keyPath, err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))), nil
}

func expand(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}
