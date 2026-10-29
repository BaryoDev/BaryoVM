// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"strings"
	"testing"
)

func TestPodmanDryRuns(t *testing.T) {
	releaseFixture(t)
	p := dryData(t, newDeployCmd(), "--vm", "vm1", "--image", "nginx", "--name", "site", "--runtime", "podman", "--pull", "--dry-run")
	all := joinCommands(p)
	for _, want := range []string{"podman pull", "podman rm -f", "podman run -d"} {
		if !strings.Contains(all, want) {
			t.Errorf("deploy dry run lacks %q:\n%s", want, all)
		}
	}
	if strings.Contains(all, "docker") {
		t.Errorf("a podman dry run mentions docker:\n%s", all)
	}
	b := dryData(t, newVMBootstrapCmd(), "vm1", "--runtime", "podman", "--dry-run")
	if len(b.Commands) != 1 || !strings.Contains(b.Commands[0], "install -y podman") || strings.Contains(b.Commands[0], "get.docker.com") {
		t.Errorf("bootstrap dry run for podman: %v", b.Commands)
	}
}
