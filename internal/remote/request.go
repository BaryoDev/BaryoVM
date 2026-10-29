// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package remote is the VM side of running BaryoVM behind an SSH forced command.
//
// A VM owner who pins a CI key in authorized_keys with command="..." has decided that key may do
// one thing. BaryoVM normally sends arbitrary shell, which that key discards, so the forced script
// runs instead and the deploy looks like it worked (BaryoDev/BaryoVM#98). This package is the
// other design: the forced command is `baryovm remote serve`, the client sends a structured
// request instead of shell, and the VM decides, from a root-owned file the client cannot touch,
// what is allowed.
//
// The security boundary is three things, each tested on its own: Parse (the request is a fixed
// grammar, never handed to a shell), the allowlist file (which stacks and ops exist at all), and
// the permission check on that file. Nothing the client sends reaches a shell. The stack name is
// only ever a lookup key into the file, and every value that does reach a shell comes from the
// file, through the same sshx.Quote the rest of BaryoVM uses.
package remote

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Protocol names the request and reply grammar. A reply that does not carry it did not come from
// this wrapper.
const Protocol = "baryovm-remote/v1"

const (
	word    = "baryovm-remote"
	version = "v1"
	// maxRequest bounds what Parse looks at. The longest legal request is far shorter.
	maxRequest = 128
)

// Op is one operation the wrapper can run.
type Op string

// The v1 allowlist. exec is not here by definition (it is arbitrary shell) and neither is release,
// which uploads files and so cannot be expressed as a request with nothing else in it.
const (
	OpUpdate Op = "update"
	OpBackup Op = "backup"
	OpStatus Op = "status"
	OpLogs   Op = "logs"
)

// Ops is every operation v1 knows, in the order they are documented.
var Ops = []Op{OpUpdate, OpBackup, OpStatus, OpLogs}

// Valid reports whether o is in the v1 allowlist.
func (o Op) Valid() bool {
	for _, k := range Ops {
		if o == k {
			return true
		}
	}
	return false
}

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// ValidStackName reports whether s is a legal stack name. It is a necessary condition only: the
// name must also be a key in the allowlist file.
func ValidStackName(s string) bool { return nameRE.MatchString(s) }

// Request is one parsed request.
type Request struct {
	Op    Op
	Stack string
}

// String is the exact line a client sends for r.
func (r Request) String() string {
	return word + " " + version + " " + string(r.Op) + " " + r.Stack
}

// ErrNoRequest is returned when the connection carried no command at all, which is what an
// interactive login looks like. A forced-command key must never get a shell, so this is a refusal.
var ErrNoRequest = errors.New("no request: this key only runs baryovm-remote requests, not a shell")

// Parse turns SSH_ORIGINAL_COMMAND into a Request, or refuses it.
//
// The grammar is four words separated by single spaces: "baryovm-remote v1 <op> <stack>". There is
// no tokenizer, no quoting, no escaping and no shell involved: the line is split on one space and
// each field is compared or matched against a closed set. Anything else is refused, including a
// fifth word, a doubled space, a tab, a newline, a non-ASCII byte, an unknown op and a bad name.
func Parse(raw string) (Request, error) {
	if raw == "" {
		return Request{}, ErrNoRequest
	}
	if len(raw) > maxRequest {
		return Request{}, errors.New("refused: request is too long")
	}
	for i := 0; i < len(raw); i++ {
		if c := raw[i]; c < 0x20 || c > 0x7e {
			return Request{}, errors.New("refused: request contains a control or non-ASCII byte")
		}
	}
	f := strings.Split(raw, " ")
	if len(f) != 4 || f[0] != word {
		return Request{}, errors.New("refused: not a baryovm-remote request (expected: baryovm-remote v1 <op> <stack>)")
	}
	if f[1] != version {
		return Request{}, fmt.Errorf("refused: unsupported protocol version %q (this VM speaks %s)", f[1], version)
	}
	op := Op(f[2])
	if !op.Valid() {
		switch op {
		case "exec", "release":
			return Request{}, fmt.Errorf("refused: %q is not available over a forced command (v1 allows %s)", f[2], opList())
		}
		return Request{}, fmt.Errorf("refused: unknown op %q (v1 allows %s)", f[2], opList())
	}
	if !ValidStackName(f[3]) {
		return Request{}, errors.New("refused: bad stack name (lowercase letters, digits and dashes, starting with a letter or digit, at most 63 characters)")
	}
	return Request{Op: op, Stack: f[3]}, nil
}

func opList() string {
	s := make([]string, len(Ops))
	for i, o := range Ops {
		s[i] = string(o)
	}
	return strings.Join(s, ", ")
}
