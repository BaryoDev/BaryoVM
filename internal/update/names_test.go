// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package update

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/BaryoDev/BaryoVM/internal/compose"
)

func staleApp() *fakeRunner {
	return &fakeRunner{
		before:  []compose.Image{img("app", "app:latest", "sha256:old", "sha256:old")},
		after:   []compose.Image{img("app", "app:latest", "sha256:old", "sha256:new")},
		healthy: []bool{true},
	}
}

func namesOptions() Options {
	return Options{HasBackup: true, HasHealthCheck: true, HealthAttempts: 1, HealthDelay: time.Millisecond}
}

// A container_name held by a container this project does not own fails compose up. Found out at
// the up, the backup has already run for an update that cannot happen (#1).
func TestUpdateChecksContainerNamesBeforeTheBackup(t *testing.T) {
	f := staleApp()

	if _, err := Run(f, namesOptions()); err != nil {
		t.Fatalf("update: %v", err)
	}
	want := []string{"pull", "images", "names", "backup", "up:app", "health"}
	if !reflect.DeepEqual(f.calls, want) {
		t.Errorf("calls:\n got %v\nwant %v", f.calls, want)
	}
}

func TestATakenContainerNameStopsTheUpdateBeforeTheBackup(t *testing.T) {
	f := staleApp()
	f.namesErr = errors.New(`container name "app" is held by a container compose does not manage`)

	res, err := Run(f, namesOptions())
	if err == nil {
		t.Fatalf("update went ahead over a taken name: %v", f.calls)
	}
	if !errors.Is(err, f.namesErr) {
		t.Errorf("the name conflict should be the error returned, got: %v", err)
	}
	if res.RolledBack {
		t.Error("nothing was recreated, so there is nothing to roll back")
	}
	want := []string{"pull", "images", "names"}
	if !reflect.DeepEqual(f.calls, want) {
		t.Errorf("calls:\n got %v\nwant %v", f.calls, want)
	}
}

// A stack with nothing to recreate never reaches compose up, so a name conflict cannot hurt it and
// must not start failing a nightly job that used to report "already up to date".
func TestAnUpToDateStackIsNotCheckedForNames(t *testing.T) {
	f := &fakeRunner{
		before:   []compose.Image{img("app", "app:latest", "sha256:same", "sha256:same")},
		after:    []compose.Image{img("app", "app:latest", "sha256:same", "sha256:same")},
		namesErr: errors.New("should not be asked"),
	}

	if _, err := Run(f, namesOptions()); err != nil {
		t.Fatalf("update: %v", err)
	}
	want := []string{"pull", "images"}
	if !reflect.DeepEqual(f.calls, want) {
		t.Errorf("calls:\n got %v\nwant %v", f.calls, want)
	}
}
