// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package fleet

import "testing"

func TestTheRemoteModeSurvivesARoundTrip(t *testing.T) {
	t.Setenv("BARYOVM_HOME", t.TempDir())
	s := &Store{}
	s.Upsert(VM{Name: "ci", Host: "h", User: "u", KeyPath: "k", Provider: "ssh", Remote: RemoteForced})
	s.Upsert(VM{Name: "plain", Host: "h", User: "u", KeyPath: "k", Provider: "ssh"})
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if vm := got.Find("ci"); vm == nil || !vm.Forced() {
		t.Errorf("remote mode lost: %+v", vm)
	}
	if vm := got.Find("plain"); vm == nil || vm.Forced() {
		t.Errorf("an ordinary VM must not read as forced: %+v", vm)
	}
}
