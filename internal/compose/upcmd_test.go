// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package compose

import (
	"strings"
	"testing"
)

func TestUpCmdBuildsTheUpInvocation(t *testing.T) {
	s := Stack{Dir: "/opt/app", Sudo: true}
	got := s.UpCmd(UpOptions{ForceRecreate: true, Services: []string{"web"}})
	for _, want := range []string{"sudo -n", "/opt/app", "up -d", "--force-recreate", "web"} {
		if !strings.Contains(got, want) {
			t.Errorf("UpCmd lacks %q: %s", want, got)
		}
	}
}
