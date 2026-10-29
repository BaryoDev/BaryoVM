// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package backup

import (
	"strings"
	"testing"
)

func podmanCfg() Config {
	return Config{Name: "app", DBContainer: "pg", DBName: "appdb", DBUser: "postgres", EnvFile: ".env", Dir: "/opt/app", Runtime: "podman"}
}

func TestAPodmanBackupAndRestoreNeverCallDocker(t *testing.T) {
	cfg := podmanCfg()
	backup, err := Backup(scriptOnly{}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	restore, err := Restore(scriptOnly{}, cfg, "app.dump")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(backup, "podman exec") {
		t.Errorf("backup does not exec through podman:\n%s", backup)
	}
	if !strings.Contains(restore, "podman exec") {
		t.Errorf("restore does not exec through podman:\n%s", restore)
	}
	for name, s := range map[string]string{"backup": backup, "restore": restore} {
		if strings.Contains(s, "docker") {
			t.Errorf("%s names docker:\n%s", name, s)
		}
	}
}

func TestAPodmanSudoBackupElevatesPodman(t *testing.T) {
	cfg := podmanCfg()
	cfg.Sudo = true
	s, err := Backup(scriptOnly{}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s, "sudo -n podman exec") {
		t.Errorf("want sudo -n podman exec:\n%s", s)
	}
}

func TestAPodmanDrillUsesPodman(t *testing.T) {
	s := DrillScript(podmanCfg(), "app.dump", "drill")
	if !strings.Contains(s, "podman") || strings.Contains(s, "docker") {
		t.Errorf("drill script runtime:\n%s", s)
	}
}

func TestADockerBackupStillUsesDocker(t *testing.T) {
	cfg := podmanCfg()
	cfg.Runtime = ""
	s, err := Backup(scriptOnly{}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s, "docker exec") || strings.Contains(s, "podman") {
		t.Errorf("default runtime changed:\n%s", s)
	}
}
