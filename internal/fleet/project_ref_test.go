// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package fleet

import (
	"strings"
	"testing"
)

// #93: the project file uses the shared reference type (internal/secretref).
func TestProjectKeyPathAcceptsEnvAndFileButNeverCmd(t *testing.T) {
	for _, ok := range []string{`"env:K"`, `"file:/run/secrets/key-path"`} {
		if _, err := ParseProject([]byte(projectWith(ok))); err != nil {
			t.Errorf("%s should be accepted: %v", ok, err)
		}
	}
	_, err := ParseProject([]byte(projectWith(`"cmd:gh auth token"`)))
	if err == nil || !strings.Contains(err.Error(), "vms[0].keyPath") {
		t.Errorf("cmd: must be refused in a committed file, naming the field: %v", err)
	}
}

func TestProjectSecretFieldMayHoldAFileReferenceButNotCmd(t *testing.T) {
	base := `{"project":"p","vms":[],"stacks":[],"registryToken":`
	if _, err := ParseProject([]byte(base + `"file:/run/reg"}`)); err != nil {
		t.Errorf("file: reference: %v", err)
	}
	if _, err := ParseProject([]byte(base + `"cmd:echo hunter2"}`)); err == nil {
		t.Error("cmd: in a secret-named field must be refused")
	} else if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("the error repeated the value: %v", err)
	}
}
