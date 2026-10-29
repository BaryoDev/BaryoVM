// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package ctr

import "testing"

func TestValidAndBin(t *testing.T) {
	for _, ok := range []string{"", "docker", "podman"} {
		if err := Valid(ok); err != nil {
			t.Errorf("Valid(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"nerdctl", "Docker", "docker; rm -rf /", " podman", "docker compose"} {
		if Valid(bad) == nil {
			t.Errorf("Valid(%q) accepted", bad)
		}
		if Bin(bad) != Unsupported {
			t.Errorf("Bin(%q) = %q, want the unsupported word", bad, Bin(bad))
		}
	}
	if Bin("podman") != "podman" || Bin("") != "docker" || Compose("podman") != "podman compose" {
		t.Error("Bin or Compose wrong")
	}
}
