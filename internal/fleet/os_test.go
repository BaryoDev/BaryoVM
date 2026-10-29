// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package fleet

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BaryoDev/BaryoVM/internal/sshx"
)

// A fleet.json written before the OS was recorded has to load unchanged, and its VMs are Linux.
func TestAFleetWithNoOSFieldLoadsAsLinux(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BARYOVM_HOME", dir)
	old := `{"vms":[{"name":"oracle","host":"1.2.3.4","user":"opc","keyPath":"~/.ssh/k","provider":"ssh"}]}`
	if err := os.WriteFile(filepath.Join(dir, "fleet.json"), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	vm := s.Find("oracle")
	if vm == nil {
		t.Fatal("entry did not load")
	}
	if vm.OS != "" {
		t.Errorf("an entry with no os must not gain one on load, got %q", vm.OS)
	}
	if vm.HostOS() != sshx.Linux {
		t.Errorf("HostOS = %q, want linux", vm.HostOS())
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "fleet.json"))
	if strings.Contains(string(b), `"os"`) {
		t.Errorf("saving an untouched legacy entry must not write an os field: %s", b)
	}
}

func TestTheOSSurvivesARoundTrip(t *testing.T) {
	t.Setenv("BARYOVM_HOME", t.TempDir())
	s := &Store{}
	s.Upsert(VM{Name: "win1", Host: "h", User: "u", KeyPath: "k", Provider: "ssh", OS: sshx.Windows, Shell: sshx.ShellPowerShell})
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if vm := got.Find("win1"); vm == nil || vm.HostOS() != sshx.Windows || vm.Shell != sshx.ShellPowerShell {
		t.Errorf("OS or shell lost: %+v", vm)
	}
}
