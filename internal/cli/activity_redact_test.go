// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/BaryoDev/BaryoVM/internal/activity"
	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"github.com/spf13/cobra"
)

func fakeRun(use string, parent string, args ...string) *ranCommand {
	root := &cobra.Command{Use: "baryovm"}
	leaf := &cobra.Command{Use: use}
	if parent != "" {
		p := &cobra.Command{Use: parent}
		root.AddCommand(p)
		p.AddCommand(leaf)
	} else {
		root.AddCommand(leaf)
	}
	return &ranCommand{cmd: leaf, args: args}
}

const leakyStderr = "remote `docker run -e TOKEN=ghp_secret123`: exit status 125: ghp_secret123 denied"

func TestErrorTextIsNeverLogged(t *testing.T) {
	activityHome(t)
	cases := map[string]error{
		"session failure": fmt.Errorf("%s", leakyStderr),
		"wrapped":         fmt.Errorf("deploy: %w", errors.New(leakyStderr)),
		"validation":      fmt.Errorf("invalid --env %q", "ghp_secret123"),
	}
	for name, err := range cases {
		for _, run := range []*ranCommand{fakeRun("exec <name> -- <command>...", "vm", "web1"), fakeRun("deploy", "")} {
			e, ok := activityEntry(run, err, time.Second)
			if !ok {
				t.Fatalf("%s: not recorded", name)
			}
			b, _ := json.Marshal(e)
			if strings.Contains(string(b), "ghp_") || strings.Contains(string(b), "docker") {
				t.Errorf("%s: error text leaked: %s", name, b)
			}
			if e.Error != "failed" {
				t.Errorf("%s: error = %q, want the fixed string", name, e.Error)
			}
		}
	}
}

func TestRemoteExitIsAFixedString(t *testing.T) {
	activityHome(t)
	err := &exitError{code: 3, msg: leakyStderr}
	e, _ := activityEntry(fakeRun("exec <name> -- <command>...", "vm", "web1"), err, time.Second)
	if e.Error != "remote command failed (exit 3)" {
		t.Fatalf("error = %q", e.Error)
	}
}

func TestOnlyKnownNamesAreRecorded(t *testing.T) {
	home := activityHome(t)
	_ = runLogged(t, "vm", "remove", "ghp_secret123")
	_ = runLogged(t, "stack", "remove", "ghp_secret123")
	for _, l := range logLines(t, home) {
		if strings.Contains(l, "ghp_") {
			t.Errorf("unknown name recorded: %s", l)
		}
	}
}

func TestARemovedVMIsStillNamed(t *testing.T) {
	home := activityHome(t)
	s := &fleet.Store{}
	s.Upsert(fleet.VM{Name: "web1", Host: "h", User: "u", KeyPath: "/k"})
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	if err := runLogged(t, "vm", "remove", "web1"); err != nil {
		t.Fatal(err)
	}
	got := logLines(t, home)
	if len(got) != 1 || !strings.Contains(got[0], "\"vm\":\"web1\"") {
		t.Fatalf("got %v", got)
	}
}

func TestVMExecHelpAndNoArgsAreNotRecorded(t *testing.T) {
	home := activityHome(t)
	_ = runLogged(t, "vm", "exec", "-h")
	_ = runLogged(t, "vm", "exec")
	if got := logLines(t, home); len(got) != 0 {
		t.Fatalf("want nothing, got %v", got)
	}
}

func TestEntryCarriesVersionAndDuration(t *testing.T) {
	activityHome(t)
	e, _ := activityEntry(fakeRun("deploy", ""), nil, 1500*time.Millisecond)
	if e.Version != Version || e.DurationMS != 1500 {
		t.Fatalf("got version %q duration %d", e.Version, e.DurationMS)
	}
	var _ = activity.Entry{}
}
