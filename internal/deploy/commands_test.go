// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package deploy

import (
	"strings"
	"testing"
)

func TestCommandsListsPullRemoveAndRun(t *testing.T) {
	cmds := Commands(Spec{Name: "site", Image: "nginx:alpine", Ports: []string{"80:80"}, Pull: true})
	if len(cmds) < 3 {
		t.Fatalf("want pull, remove and run, got %v", cmds)
	}
	if !strings.Contains(cmds[0], "pull") || !strings.Contains(cmds[0], "nginx:alpine") {
		t.Errorf("first command should pull the image: %s", cmds[0])
	}
	last := cmds[len(cmds)-1]
	if !strings.Contains(last, "run") || !strings.Contains(last, "80:80") {
		t.Errorf("last command should run the container: %s", last)
	}
}
