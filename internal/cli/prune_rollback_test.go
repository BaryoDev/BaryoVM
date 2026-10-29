// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"testing"

	"github.com/BaryoDev/BaryoVM/internal/fleet"
)

type recRunner struct{ out string }

func (r recRunner) Run(string) (string, error) { return r.out, nil }

func TestRollbackIDsReadsOnlyThisVMsStacks(t *testing.T) {
	f := recRunner{`{"stack":"app","images":[{"service":"web","ref":"app:1","id":"sha256:abc"}]}`}
	stacks := []fleet.Stack{{Name: "app", VM: "vm1", Dir: "/srv/app"}, {Name: "other", VM: "vm2", Dir: "/srv/o"}}
	ids, err := rollbackIDs(f, stacks, "vm1")
	if err != nil {
		t.Fatal(err)
	}
	if ids["sha256:abc"] != "app" || len(ids) != 1 {
		t.Fatalf("ids = %v", ids)
	}
}
