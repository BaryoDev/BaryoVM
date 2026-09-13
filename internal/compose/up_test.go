// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package compose

import (
	"errors"
	"reflect"
	"testing"
)

// scriptedRunner replies to each call in order, and records every command it was sent.
type scriptedRunner struct {
	replies []error
	seen    []string
}

func (r *scriptedRunner) Run(cmd string) (string, error) {
	r.seen = append(r.seen, cmd)
	if len(r.replies) == 0 {
		return "", nil
	}
	err := r.replies[0]
	r.replies = r.replies[1:]
	return "", err
}

// What compose printed on 2 Sep 2026 when a release collided with a container an earlier recreate
// had left renamed.
const reportedConflict = "Process exited with status 1: Container umbraco-pwa-demo Recreate\n" +
	"Error response from daemon: Conflict. The container name \"/b92d86916493_umbraco-pwa-demo\" " +
	"is already in use by container \"a7cd72183b018e5d6fdce4a991243998d968ef12af87e1bedd10f96f99b99cd1\". " +
	"You have to remove (or rename) that container to be able to reuse that name."

func TestUpRemovesAContainerLeftRenamedByAnEarlierRecreateAndTriesAgain(t *testing.T) {
	r := &scriptedRunner{replies: []error{errors.New(reportedConflict), nil}}
	s := Stack{Dir: "/home/opc/umbraco-pwa"}

	if _, err := Up(r, s, UpOptions{}); err != nil {
		t.Fatalf("up should succeed once the leftover is gone: %v", err)
	}

	want := []string{
		"cd '/home/opc/umbraco-pwa' && docker compose up -d",
		"docker rm -f 'a7cd72183b018e5d6fdce4a991243998d968ef12af87e1bedd10f96f99b99cd1'",
		"cd '/home/opc/umbraco-pwa' && docker compose up -d",
	}
	if !reflect.DeepEqual(r.seen, want) {
		t.Errorf("commands:\n got %q\nwant %q", r.seen, want)
	}
}

func TestTheLeftoverIsRemovedWithTheStacksSudo(t *testing.T) {
	r := &scriptedRunner{replies: []error{errors.New(reportedConflict), nil}}
	s := Stack{Dir: "/opt/app", Sudo: true}

	if _, err := Up(r, s, UpOptions{}); err != nil {
		t.Fatalf("up: %v", err)
	}
	if len(r.seen) != 3 {
		t.Fatalf("want up, rm, up; got %q", r.seen)
	}
	if want := "sudo -n docker rm -f 'a7cd72183b018e5d6fdce4a991243998d968ef12af87e1bedd10f96f99b99cd1'"; r.seen[1] != want {
		t.Errorf("rm: got %q, want %q", r.seen[1], want)
	}
}

// Only a name in compose's own `<12 hex>_<name>` rename form is a leftover. A clash with any other
// container is somebody's real container, and removing it is not this tool's call.
func TestUpDoesNotRemoveAContainerThatIsNotARecreateLeftover(t *testing.T) {
	conflict := errors.New("Error response from daemon: Conflict. The container name \"/umbraco-pwa-demo\" " +
		"is already in use by container \"a7cd72183b018e5d6fdce4a991243998d968ef12af87e1bedd10f96f99b99cd1\".")
	r := &scriptedRunner{replies: []error{conflict}}

	_, err := Up(r, Stack{Dir: "/opt/app"}, UpOptions{})
	if err == nil {
		t.Fatal("a conflict with a real container must still fail")
	}
	if len(r.seen) != 1 {
		t.Errorf("want only the one up, got %q", r.seen)
	}
}

// One retry, not a loop: if the second up collides again, that error is the answer.
func TestUpRetriesOnlyOnce(t *testing.T) {
	r := &scriptedRunner{replies: []error{errors.New(reportedConflict), nil, errors.New(reportedConflict)}}

	_, err := Up(r, Stack{Dir: "/opt/app"}, UpOptions{})
	if err == nil {
		t.Fatal("a second conflict must be returned")
	}
	if len(r.seen) != 3 {
		t.Errorf("want up, rm, up; got %q", r.seen)
	}
}

func TestAFailedRemovalReturnsTheOriginalConflict(t *testing.T) {
	r := &scriptedRunner{replies: []error{errors.New(reportedConflict), errors.New("permission denied")}}

	_, err := Up(r, Stack{Dir: "/opt/app"}, UpOptions{})
	if err == nil {
		t.Fatal("up must fail when the leftover could not be removed")
	}
	if len(r.seen) != 2 {
		t.Errorf("want up, rm; got %q", r.seen)
	}
}
