// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package deploy

import (
	"strings"
	"testing"
)

func TestAPodmanSpecBuildsPodmanCommands(t *testing.T) {
	cmds := Commands(Spec{Name: "site", Image: "nginx:alpine", Ports: []string{"80:80"}, Pull: true, Runtime: "podman"})
	if len(cmds) < 3 {
		t.Fatalf("want pull, remove and run, got %v", cmds)
	}
	for _, want := range []string{"podman pull", "podman rm -f", "podman run -d"} {
		found := false
		for _, c := range cmds {
			if strings.Contains(c, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("no command contains %q: %v", want, cmds)
		}
	}
	for _, c := range cmds {
		if strings.Contains(c, "docker") {
			t.Errorf("a podman deploy names docker: %s", c)
		}
	}
}

func TestValidateRefusesAnUnknownRuntime(t *testing.T) {
	for _, bad := range []string{"nerdctl", "docker; reboot"} {
		err := Validate(Spec{Name: "site", Image: "nginx", Runtime: bad})
		if err == nil || !strings.Contains(err.Error(), "is not supported") {
			t.Errorf("runtime %q: err = %v", bad, err)
		}
	}
	if err := Validate(Spec{Name: "site", Image: "nginx", Runtime: "podman"}); err != nil {
		t.Errorf("podman refused: %v", err)
	}
}
