// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package compose

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// namesRunner answers by substring and records every command, in order.
type namesRunner struct {
	answers map[string]string
	fail    map[string]error
	seen    []string
}

func (r *namesRunner) Run(cmd string) (string, error) {
	r.seen = append(r.seen, cmd)
	for k, err := range r.fail {
		if strings.Contains(cmd, k) {
			return "", err
		}
	}
	for k, out := range r.answers {
		if strings.Contains(cmd, k) {
			return out, nil
		}
	}
	return "", fmt.Errorf("namesRunner: no answer for %q", cmd)
}

const demoConfig = `{"name": "umbraco-pwa", "services": {"demo": {"image": "umbraco-pwa-demo:arm64", "container_name": "umbraco-pwa-demo"}}}`

func namesAnswers(containers string) map[string]string {
	return map[string]string{"config --format json": demoConfig, "com.docker.compose.project": containers}
}

// #1: the name was held by a container started with docker run, which carries no compose labels.
func TestAContainerStartedByHandIsReportedWithTheCommandThatFreesTheName(t *testing.T) {
	r := &namesRunner{answers: namesAnswers("umbraco-pwa-demo\t\nunrelated\t\n")}

	err := CheckContainerNames(r, Stack{Dir: "/home/opc/umbraco-pwa"})

	var conflict *NameConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("want a NameConflictError, got %v", err)
	}
	if len(conflict.Conflicts) != 1 || conflict.Conflicts[0] != (NameConflict{Name: "umbraco-pwa-demo"}) {
		t.Errorf("conflicts: %+v", conflict.Conflicts)
	}
	want := `container name "umbraco-pwa-demo" is held by a container compose does not manage, probably started ` +
		"with docker run: check it is safe to remove, then run `docker rm -f 'umbraco-pwa-demo'` on the VM and try again"
	if err.Error() != want {
		t.Errorf("message:\n got %s\nwant %s", err.Error(), want)
	}
	wantCmds := []string{
		"cd '/home/opc/umbraco-pwa' && docker compose config --format json",
		`docker ps -a --format '{{.Names}}\t{{.Label "com.docker.compose.project"}}'`,
	}
	if !reflect.DeepEqual(r.seen, wantCmds) {
		t.Errorf("commands:\n got %q\nwant %q", r.seen, wantCmds)
	}
}

// A second release finds its own container holding the name, and a recreate can leave a renamed one
// beside it (#57). Both belong to the project, so neither is a conflict.
func TestContainersThisProjectOwnsAreNotConflicts(t *testing.T) {
	r := &namesRunner{answers: namesAnswers("umbraco-pwa-demo\tumbraco-pwa\nb92d86916493_umbraco-pwa-demo\tumbraco-pwa\n")}

	if err := CheckContainerNames(r, Stack{Dir: "/home/opc/umbraco-pwa"}); err != nil {
		t.Fatalf("own containers reported as a conflict: %v", err)
	}
	if len(r.seen) != 2 {
		t.Errorf("want the config read and the container list, got %q", r.seen)
	}
}

func TestAContainerFromAnotherComposeProjectIsAConflict(t *testing.T) {
	r := &namesRunner{answers: namesAnswers("umbraco-pwa-demo\tsomething-else\n")}

	err := CheckContainerNames(r, Stack{Dir: "/home/opc/umbraco-pwa"})

	var conflict *NameConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("want a NameConflictError, got %v", err)
	}
	if len(conflict.Conflicts) != 1 || conflict.Conflicts[0].Project != "something-else" {
		t.Errorf("conflicts: %+v", conflict.Conflicts)
	}
	if !strings.Contains(err.Error(), `a container from compose project "something-else"`) {
		t.Errorf("the message should say which project holds it: %v", err)
	}
}

func TestEveryTakenNameIsReported(t *testing.T) {
	cfg := `{"name": "shop", "services": {"web": {"container_name": "shop-web"}, "api": {"container_name": "shop-api"}, "db": {}}}`
	r := &namesRunner{answers: map[string]string{
		"config --format json":       cfg,
		"com.docker.compose.project": "shop-web\t\nshop-api\t\n",
	}}

	err := CheckContainerNames(r, Stack{Dir: "/opt/shop"})

	var conflict *NameConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("want a NameConflictError, got %v", err)
	}
	got := []string{}
	for _, c := range conflict.Conflicts {
		got = append(got, c.Name)
	}
	if want := []string{"shop-api", "shop-web"}; !reflect.DeepEqual(got, want) {
		t.Errorf("conflicts: got %v, want %v", got, want)
	}
	for _, name := range got {
		if !strings.Contains(err.Error(), "docker rm -f '"+name+"'") {
			t.Errorf("message does not give the command for %s: %v", name, err)
		}
	}
}

// Without a container_name compose picks the name itself, so there is nothing to collide with and no
// reason to list containers.
func TestAComposeFileWithNoContainerNameIsNotChecked(t *testing.T) {
	r := &namesRunner{answers: map[string]string{
		"config --format json": `{"name": "app", "services": {"app": {"image": "app:latest"}}}`,
	}}

	if err := CheckContainerNames(r, Stack{Dir: "/opt/app"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(r.seen) != 1 {
		t.Errorf("want only the config read, got %q", r.seen)
	}
}

func TestTheNameCheckRunsWithTheStacksSudo(t *testing.T) {
	r := &namesRunner{answers: namesAnswers("umbraco-pwa-demo\t\n")}

	err := CheckContainerNames(r, Stack{Dir: "/opt/app", Sudo: true})
	if err == nil {
		t.Fatal("want a conflict")
	}
	wantCmds := []string{
		`sudo -n "${SHELL:-/bin/sh}" -c 'cd '\''/opt/app'\'' && docker compose config --format json'`,
		`sudo -n docker ps -a --format '{{.Names}}\t{{.Label "com.docker.compose.project"}}'`,
	}
	if !reflect.DeepEqual(r.seen, wantCmds) {
		t.Errorf("commands:\n got %q\nwant %q", r.seen, wantCmds)
	}
	if !strings.Contains(err.Error(), "`sudo -n docker rm -f 'umbraco-pwa-demo'`") {
		t.Errorf("a sudo stack's fix needs sudo too: %v", err)
	}
}

// Not being able to read the names is not the same as a conflict, and callers treat the two
// differently.
func TestAFailedReadIsNotReportedAsAConflict(t *testing.T) {
	r := &namesRunner{fail: map[string]error{"config --format json": errors.New("no configuration file provided")}}

	err := CheckContainerNames(r, Stack{Dir: "/opt/app"})
	if err == nil {
		t.Fatal("a failed read should be returned")
	}
	var conflict *NameConflictError
	if errors.As(err, &conflict) {
		t.Errorf("a failed read was reported as a conflict: %v", err)
	}
}
