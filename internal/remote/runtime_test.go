// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package remote

import "testing"

func TestRuntimeInRemoteJSONReachesTheBuilders(t *testing.T) {
	cfg, err := ParseConfig([]byte(`{"stacks":{"app":{"dir":"/opt/app","runtime":"podman","dbContainer":"pg","dbName":"x"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	sc := cfg.Stacks["app"]
	if sc.Runtime != "podman" {
		t.Fatalf("runtime not parsed: %+v", sc)
	}
	if composeStack(sc).Runtime != "podman" {
		t.Error("composeStack drops the runtime")
	}
	if backupConfig(sc, "app").Runtime != "podman" {
		t.Error("backupConfig drops the runtime")
	}
}
