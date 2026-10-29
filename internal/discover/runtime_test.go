// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package discover

import (
	"strings"
	"testing"
)

func TestDiscoverCommandsFollowTheRuntime(t *testing.T) {
	if got, want := LsCmd(true, "podman"), "sudo -n podman compose ls --format json"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
	if got := ConfigCmd([]string{"/opt/a/c.yaml"}, false, "podman"); got != "podman compose -f '/opt/a/c.yaml' config --format json" {
		t.Errorf("got %q", got)
	}
	if got := AddCommand(Project{Name: "a", Dir: "/opt/a"}, "vm", false, "podman"); !strings.HasSuffix(got, " --runtime podman") {
		t.Errorf("adopt line loses the runtime: %s", got)
	}
}
