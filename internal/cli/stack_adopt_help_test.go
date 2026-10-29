// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// --adopt runs docker rm -f on a container, which cannot be undone, so the flag help must say so.
func TestAdoptFlagHelpSaysRemovalCannotBeUndone(t *testing.T) {
	for name, cmd := range map[string]*cobra.Command{"release": newStackReleaseCmd(), "deploy": newStackDeployCmd()} {
		f := cmd.Flags().Lookup("adopt")
		if f == nil || !strings.Contains(f.Usage, "NOT recoverable") {
			t.Errorf("%s --adopt help does not warn the removal is permanent: %v", name, f)
		}
	}
}
