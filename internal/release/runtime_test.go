// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package release

import (
	"strings"
	"testing"
)

func TestAPodmanReleaseBuildsWithPodman(t *testing.T) {
	m := &Manifest{RemoteRoot: "/srv/app", Runtime: "podman"}
	got := m.buildScript(Build{Image: "app:1", Dockerfile: "Dockerfile", Context: "."})
	if !strings.Contains(got, "podman build") {
		t.Errorf("want podman build:\n%s", got)
	}
	if strings.Contains(got, "docker") || strings.Contains(got, "DOCKER_BUILDKIT") {
		t.Errorf("a podman build must not name docker or BuildKit:\n%s", got)
	}
	d := (&Manifest{RemoteRoot: "/srv/app"}).buildScript(Build{Image: "app:1", Dockerfile: "Dockerfile", Context: "."})
	if !strings.Contains(d, "docker build") || !strings.Contains(d, "DOCKER_BUILDKIT=1") {
		t.Errorf("the default build changed:\n%s", d)
	}
}
