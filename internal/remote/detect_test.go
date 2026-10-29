// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package remote

import (
	"errors"
	"strings"
	"testing"
)

type fnRunner func(cmd string) (string, error)

func (f fnRunner) Run(cmd string) (string, error) { return f(cmd) }

func TestNewNonceIsRandomHex(t *testing.T) {
	a, b := NewNonce(), NewNonce()
	if a == b || len(a) < 16 {
		t.Errorf("nonces %q %q", a, b)
	}
	for _, c := range a {
		if !strings.ContainsRune("0123456789abcdef", c) {
			t.Errorf("nonce %q is not plain hex", a)
		}
	}
}

func TestDetectPassesWhenTheNonceComesBack(t *testing.T) {
	r := fnRunner(func(cmd string) (string, error) {
		return strings.TrimPrefix(cmd, "echo ") + "\n", nil
	})
	if err := DetectSwallowed(r, "abc123"); err != nil {
		t.Error(err)
	}
}

func TestDetectCatchesAForcedScriptThatIgnoresTheCommand(t *testing.T) {
	r := fnRunner(func(string) (string, error) { return "deploy done\n", nil })
	err := DetectSwallowed(r, "abc123")
	if err == nil || !strings.Contains(err.Error(), "command=") {
		t.Errorf("a swallowed command must fail loudly: %v", err)
	}
}

func TestDetectCatchesAForcedScriptThatPrintsNothing(t *testing.T) {
	r := fnRunner(func(string) (string, error) { return "", nil })
	if err := DetectSwallowed(r, "abc123"); err == nil {
		t.Error("empty output is not the nonce")
	}
}

func TestDetectRecognisesTheWrapper(t *testing.T) {
	r := fnRunner(func(string) (string, error) {
		return `{"protocol":"` + Protocol + `","ok":false}`, nil
	})
	err := DetectSwallowed(r, "abc123")
	if err == nil || !strings.Contains(err.Error(), "vm remote set") {
		t.Errorf("should point at `vm remote set`: %v", err)
	}
}

func TestDetectReportsAProbeError(t *testing.T) {
	r := fnRunner(func(string) (string, error) { return "", errors.New("exit 1") })
	err := DetectSwallowed(r, "abc123")
	if err == nil || !strings.Contains(err.Error(), "probe command failed") {
		t.Errorf("got %v", err)
	}
}

func TestDetectAcceptsTheNonceOnItsOwnLineAfterABanner(t *testing.T) {
	r := fnRunner(func(cmd string) (string, error) {
		return "Welcome to the box\r\nlast login: today\r\n  " + strings.TrimPrefix(cmd, "echo ") + "  \r\n", nil
	})
	if err := DetectSwallowed(r, "abc123"); err != nil {
		t.Errorf("a banner before the nonce line must pass: %v", err)
	}
}

func TestDetectRefusesTheNonceInsideOtherText(t *testing.T) {
	r := fnRunner(func(cmd string) (string, error) {
		return "you ran: " + strings.TrimPrefix(cmd, "echo ") + " ok\n", nil
	})
	if err := DetectSwallowed(r, "abc123"); err == nil {
		t.Error("the nonce inside other text on one line is not an answer")
	}
}

func TestDetectRefusesOutputWithoutTheNonce(t *testing.T) {
	r := fnRunner(func(string) (string, error) { return "banner\nmore\n", nil })
	if err := DetectSwallowed(r, "abc123"); err == nil {
		t.Error("output with no nonce must fail")
	}
}
