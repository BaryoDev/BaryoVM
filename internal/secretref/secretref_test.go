// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package secretref

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

const secret = "s3cr3t-value-9f2"

func fake() Resolver {
	return Resolver{
		Getenv: func(k string) string {
			if k == "TOKEN" {
				return secret
			}
			return ""
		},
		ReadFile: func(p string) ([]byte, error) {
			switch p {
			case "/run/tok":
				return []byte(secret + "\n"), nil
			case "/run/bad":
				return nil, &os.PathError{Op: "open", Path: "/run/bad", Err: os.ErrPermission}
			}
			return nil, os.ErrNotExist
		},
		Run: func(ctx context.Context, argv []string) ([]byte, error) {
			switch argv[0] {
			case "gh":
				return []byte(secret + "\n"), nil
			case "slow":
				<-ctx.Done()
				return nil, ctx.Err()
			case "leaky":
				return nil, errors.New("failed: " + secret)
			}
			return nil, errors.New("nope")
		},
		AllowCmd: true,
	}
}

func TestResolveKinds(t *testing.T) {
	r := fake()
	for _, ref := range []string{"env:TOKEN", "file:/run/tok", "cmd:gh auth token"} {
		got, err := r.Resolve(ref)
		if err != nil || got != secret {
			t.Errorf("%s: got %q, %v", ref, got, err)
		}
	}
}

func TestParseRejects(t *testing.T) {
	for _, ref := range []string{"TOKEN", "env:", "env:1BAD", "env:A B", "file:", "cmd:", "cmd:'x", "keychain:x/y", "vault:x"} {
		if _, err := Parse(ref); err == nil {
			t.Errorf("%q should not parse", ref)
		}
	}
}

func TestSplitArgv(t *testing.T) {
	got, err := SplitArgv(`op read "op://my vault/item/field" --no-newline`)
	want := []string{"op", "read", "op://my vault/item/field", "--no-newline"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestUnresolvedNamesTheReferenceNotAValue(t *testing.T) {
	r := fake()
	r.Timeout = 20 * time.Millisecond
	for _, ref := range []string{"env:MISSING", "file:/nowhere", "file:/run/bad", "cmd:broken", "cmd:leaky", "cmd:slow"} {
		_, err := r.Resolve(ref)
		if err == nil {
			t.Errorf("%s should not resolve", ref)
			continue
		}
		if !strings.Contains(err.Error(), ref) {
			t.Errorf("%s: error must name the reference: %v", ref, err)
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("%s: error leaked a value: %v", ref, err)
		}
	}
}

func TestCmdIsRefusedUnlessAllowed(t *testing.T) {
	r := fake()
	r.AllowCmd = false
	if _, err := r.Resolve("cmd:gh auth token"); err == nil {
		t.Fatal("cmd: must be refused when not allowed")
	}
}

func TestEmptyValueDoesNotResolve(t *testing.T) {
	r := fake()
	r.ReadFile = func(string) ([]byte, error) { return []byte("\n"), nil }
	if _, err := r.Resolve("file:/x"); err == nil {
		t.Fatal("an empty file is not a secret")
	}
}

func TestRealCmdRunsWithoutAShellAndHidesStderr(t *testing.T) {
	r := Default().WithCmd()
	got, err := r.Resolve("cmd:printf abc")
	if err != nil || got != "abc" {
		t.Fatalf("got %q, %v", got, err)
	}
	// A shell would expand this; exec passes it through literally.
	got, err = r.Resolve("cmd:printf $HOME")
	if err != nil || got != "$HOME" {
		t.Fatalf("no shell expected, got %q, %v", got, err)
	}
	_, err = r.Resolve("cmd:sh -c 'echo " + secret + " >&2; exit 3'")
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("stderr must not reach the error: %v", err)
	}
}
