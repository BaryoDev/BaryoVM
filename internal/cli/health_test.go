package cli

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"github.com/BaryoDev/BaryoVM/internal/sshx"
)

type healthHost func(cmd string) string

func (h healthHost) Run(cmd string) (string, error) { return h(cmd), nil }

// healthyHost answers like a dnf host; secUpdates is the security count it reports.
func healthyHost(secUpdates string) healthHost {
	return func(cmd string) string {
		switch {
		case strings.Contains(cmd, `echo "PM|dnf"`):
			return "PM|dnf\n"
		case strings.Contains(cmd, "df -P -k '/'"):
			return "DISK|/|100000000|10000000|10\n"
		case strings.Contains(cmd, "df -P -k '/var/lib/docker'"):
			return "DISK|/var|100000000|10000000|10\n"
		case strings.Contains(cmd, "DockerRootDir"):
			return "DOCKERROOT|/var/lib/docker\n"
		case strings.Contains(cmd, "check-update"):
			return "ALL|" + secUpdates + "\nSEC|" + secUpdates + "\n"
		case strings.Contains(cmd, "needs-restarting"):
			return "REBOOT|no\nUPTIME|86400\n"
		case strings.Contains(cmd, "docker inspect"):
			return "DOCKER|1\nCTR|/web|running|0|0|always|healthy\n"
		case strings.Contains(cmd, "UNATTENDED"):
			return "UNATTENDED|yes|dnf-automatic-install.timer\n"
		}
		return ""
	}
}

func healthFleet(t *testing.T, names ...string) {
	t.Helper()
	t.Setenv("BARYOVM_HOME", t.TempDir())
	store := &fleet.Store{}
	for _, n := range names {
		store.VMs = append(store.VMs, fleet.VM{Name: n, Host: "10.0.0." + n[len(n)-1:], User: "u", KeyPath: "/k"})
	}
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
}

func stubDial(t *testing.T, byName map[string]sshx.Runner) {
	t.Helper()
	prev := dialRelease
	dialRelease = func(vm *fleet.VM) (remoteSession, error) {
		r, ok := byName[vm.Name]
		if !ok {
			return nil, errors.New("dial " + vm.Name + ": connection refused")
		}
		return closableRunner{r}, nil
	}
	t.Cleanup(func() { dialRelease = prev })
}

type healthData struct {
	VM      string `json:"vm"`
	Verdict string `json:"verdict"`
	Error   string `json:"error"`
	Checks  []struct {
		Name    string `json:"name"`
		Verdict string `json:"verdict"`
	} `json:"checks"`
}

func TestVMHealthJSONHealthyHost(t *testing.T) {
	healthFleet(t, "vm1")
	stubDial(t, map[string]sshx.Runner{"vm1": healthyHost("0")})
	stdout, _, err := runCmdE(t, newVMHealthCmd(), nil, true, "vm1")
	if err != nil {
		t.Fatal(err)
	}
	env, data := parseEnvelope(t, stdout)
	if !env.OK || env.Action != "vm health" {
		t.Fatalf("%+v", env)
	}
	var d healthData
	if err := json.Unmarshal([]byte(data), &d); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range d.Checks {
		names = append(names, c.Name)
	}
	for _, want := range []string{"disk-root", "disk-docker", "updates", "reboot", "containers", "unattended-updates"} {
		if !strings.Contains(strings.Join(names, ","), want) {
			t.Errorf("missing check %s in %v", want, names)
		}
	}
	if d.VM != "vm1" || d.Verdict != "ok" {
		t.Fatalf("%+v", d)
	}
}

func TestVMHealthSecurityUpdateExitsNonZeroButKeepsTheReport(t *testing.T) {
	healthFleet(t, "vm1")
	stubDial(t, map[string]sshx.Runner{"vm1": healthyHost("12")})
	stdout, _, err := runCmdE(t, newVMHealthCmd(), nil, true, "vm1")
	if err == nil {
		t.Fatal("expected a non-zero exit")
	}
	env, data := parseEnvelope(t, stdout)
	if env.OK {
		t.Fatal("ok should be false")
	}
	var d healthData
	if err := json.Unmarshal([]byte(data), &d); err != nil {
		t.Fatalf("report missing from data: %v", err)
	}
	if d.Verdict != "fail" || len(d.Checks) != 6 {
		t.Fatalf("%+v", d)
	}
}

func TestVMHealthAllReportsTheReachableVMWhenOneIsDown(t *testing.T) {
	healthFleet(t, "vm1", "vm2")
	stubDial(t, map[string]sshx.Runner{"vm2": healthyHost("0")})
	stdout, _, err := runCmdE(t, newVMHealthCmd(), nil, true, "--all")
	if err == nil {
		t.Fatal("an unreachable VM should make the run exit non-zero")
	}
	env, data := parseEnvelope(t, stdout)
	if env.OK {
		t.Fatal("ok should be false")
	}
	var ds []healthData
	if err := json.Unmarshal([]byte(data), &ds); err != nil {
		t.Fatal(err)
	}
	if len(ds) != 2 {
		t.Fatalf("%+v", ds)
	}
	if ds[0].VM != "vm1" || ds[0].Verdict != "fail" || !strings.Contains(ds[0].Error, "connection refused") {
		t.Errorf("vm1: %+v", ds[0])
	}
	if ds[1].VM != "vm2" || ds[1].Verdict != "ok" || len(ds[1].Checks) != 6 {
		t.Errorf("vm2: %+v", ds[1])
	}
}
