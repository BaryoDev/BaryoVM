// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"strings"
	"testing"

	"github.com/BaryoDev/BaryoVM/internal/compose"
	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"github.com/spf13/cobra"
)

// The flag, the struct tag and the copy into the struct are three separate edits. This goes through
// the cobra command and the saved file, so a flag bound but never copied fails here.
func TestStackAddRuntimeIsPersisted(t *testing.T) {
	configuredStackFixture(t)
	runCmd(t, newStackAddCmd(), nil, true, "app", "--vm", "vm1", "--path", "/opt/app", "--runtime", "podman")
	got := reloadStack(t, "app")
	if got.Runtime != "podman" {
		t.Fatalf("Runtime = %q after `stack add --runtime podman`", got.Runtime)
	}
	if !got.Sudo || !got.AutoUpdate {
		t.Errorf("setting the runtime cleared other settings: %+v", got)
	}

	cs := composeStack(&got)
	if cs.Runtime != "podman" || !strings.Contains(cs.UpCmd(compose.UpOptions{}), "podman compose") {
		t.Errorf("composeStack drops the runtime: %+v", cs)
	}
	if backupConfig(&got).Runtime != "podman" {
		t.Error("backupConfig drops the runtime")
	}

	// A re-add that does not name the flag leaves it alone, like every other optional field.
	runCmd(t, newStackAddCmd(), nil, true, "app", "--vm", "vm1", "--path", "/opt/app", "--keep", "3")
	if again := reloadStack(t, "app"); again.Runtime != "podman" {
		t.Errorf("a re-add without --runtime reset it to %q", again.Runtime)
	}
	runCmd(t, newStackAddCmd(), nil, true, "app", "--vm", "vm1", "--path", "/opt/app", "--runtime", "docker")
	if back := reloadStack(t, "app"); back.Runtime != "docker" {
		t.Errorf("--runtime docker not stored: %q", back.Runtime)
	}
}

func TestUnknownRuntimeIsRefusedByTheCommands(t *testing.T) {
	configuredStackFixture(t)
	for _, bad := range []string{"nerdctl", "docker; reboot", "Podman"} {
		cases := map[string]*cobra.Command{
			"stack add":       newStackAddCmd(),
			"stack discover":  newStackDiscoverCmd(),
			"deploy":          newDeployCmd(),
			"vm bootstrap":    newVMBootstrapCmd(),
			"vm bootstrap -n": newVMBootstrapCmd(),
		}
		args := map[string][]string{
			"stack add":       {"x", "--vm", "vm1", "--path", "/opt/x", "--runtime", bad},
			"stack discover":  {"vm1", "--runtime", bad},
			"deploy":          {"--vm", "vm1", "--image", "i", "--name", "n", "--runtime", bad},
			"vm bootstrap":    {"vm1", "--runtime", bad},
			"vm bootstrap -n": {"vm1", "--runtime", bad, "--dry-run"},
		}
		for name, cmd := range cases {
			_, _, err := runCmdE(t, cmd, nil, true, args[name]...)
			if err == nil || !strings.Contains(err.Error(), "is not supported") {
				t.Errorf("%s --runtime %q: err = %v", name, bad, err)
			}
		}
	}
	store, _ := fleet.Load()
	if len(store.Stacks) != 1 {
		t.Errorf("a refused runtime was stored: %+v", store.Stacks)
	}
}
