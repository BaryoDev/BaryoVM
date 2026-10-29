// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
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
	env, data := healthEnvelope(t, stdout)
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
	env, data := healthEnvelope(t, stdout)
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
	env, data := healthEnvelope(t, stdout)
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

// diskHost is healthyHost with the root filesystem at 85%.
func diskHost() healthHost {
	base := healthyHost("0")
	return func(cmd string) string {
		if strings.Contains(cmd, "df -P -k '/'") {
			return "DISK|/|100000000|85000000|85\n"
		}
		return base(cmd)
	}
}

func rootDiskVerdict(t *testing.T, args ...string) (string, string) {
	t.Helper()
	healthFleet(t, "vm1")
	stubDial(t, map[string]sshx.Runner{"vm1": diskHost()})
	stdout, _, err := runCmdE(t, newVMHealthCmd(), nil, true, append([]string{"vm1"}, args...)...)
	if err != nil {
		t.Fatal(err)
	}
	_, data := healthEnvelope(t, stdout)
	var d healthData
	if err := json.Unmarshal([]byte(data), &d); err != nil {
		t.Fatal(err)
	}
	for _, c := range d.Checks {
		if c.Name == "disk-root" {
			return c.Verdict, data
		}
	}
	t.Fatal("no disk-root check")
	return "", ""
}

func TestVMHealthDiskThresholdFlags(t *testing.T) {
	v, data := rootDiskVerdict(t)
	for _, want := range []string{`"diskWarn":80`, `"diskFail":90`, `"maxUpdates":25`, `"maxRestarts":5`} {
		if !strings.Contains(data, want) {
			t.Errorf("default thresholds missing %s in %s", want, data)
		}
	}
	if v != "warn" {
		t.Fatalf("85%% by default: %s", v)
	}
	v, data = rootDiskVerdict(t, "--disk-warn", "90", "--disk-fail", "95")
	if v != "ok" || !strings.Contains(data, `"diskWarn":90`) || !strings.Contains(data, `"diskFail":95`) {
		t.Fatalf("raised: %s %s", v, data)
	}
}

func TestVMHealthRefusesBadThresholdsBeforeDialling(t *testing.T) {
	for _, args := range [][]string{
		{"--disk-warn", "95", "--disk-fail", "90"},
		{"--disk-warn", "90", "--disk-fail", "90"},
		{"--disk-warn", "0"},
		{"--disk-fail", "101"},
		{"--max-updates", "-1"},
		{"--max-restarts", "-3"},
	} {
		healthFleet(t, "vm1")
		dialled := false
		prev := dialRelease
		dialRelease = func(*fleet.VM) (remoteSession, error) {
			dialled = true
			return nil, errors.New("dialled")
		}
		stdout, _, err := runCmdE(t, newVMHealthCmd(), nil, true, append([]string{"vm1"}, args...)...)
		dialRelease = prev
		if err == nil {
			t.Errorf("%v: expected a refusal", args)
		}
		if dialled {
			t.Errorf("%v: dialled", args)
		}
		env, _ := healthEnvelope(t, stdout)
		if env.OK || env.Action != "vm health" {
			t.Errorf("%v: %+v", args, env)
		}
	}
}

func TestVMHealthMaxUpdatesZeroWarnsOnOnePending(t *testing.T) {
	healthFleet(t, "vm1")
	// One non-security update pending.
	host := healthHost(func(cmd string) string {
		if strings.Contains(cmd, "check-update") {
			return "ALL|1\nSEC|0\n"
		}
		return healthyHost("0")(cmd)
	})
	stubDial(t, map[string]sshx.Runner{"vm1": host})
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, "ok"},
		{[]string{"--max-updates", "0"}, "warn"},
	} {
		stdout, _, err := runCmdE(t, newVMHealthCmd(), nil, true, append([]string{"vm1"}, tc.args...)...)
		if err != nil {
			t.Fatal(err)
		}
		_, data := healthEnvelope(t, stdout)
		var d healthData
		if err := json.Unmarshal([]byte(data), &d); err != nil {
			t.Fatal(err)
		}
		got := ""
		for _, c := range d.Checks {
			if c.Name == "updates" {
				got = c.Verdict
			}
		}
		if got != tc.want {
			t.Errorf("%v: updates is %q, want %q", tc.args, got, tc.want)
		}
	}
}

// healthEnvelope is parseEnvelope without the demand that the result be ok: vm health emits ok
// false, with the report still in data, when a check fails.
func healthEnvelope(t *testing.T, stdout string) (envelope, string) {
	t.Helper()
	var e envelope
	if err := json.Unmarshal([]byte(stdout), &e); err != nil {
		t.Fatalf("envelope is not JSON (%v): %s", err, stdout)
	}
	var buf bytes.Buffer
	if len(e.Data) > 0 {
		if err := json.Compact(&buf, e.Data); err != nil {
			t.Fatal(err)
		}
	}
	return e, buf.String()
}

// documents counts the JSON documents in stdout.
func documents(t *testing.T, stdout string) int {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(stdout))
	n := 0
	for {
		var v json.RawMessage
		err := dec.Decode(&v)
		if err == io.EOF {
			return n
		}
		if err != nil {
			t.Fatalf("stdout is not JSON (%v): %s", err, stdout)
		}
		n++
	}
}

// Every path that already emitted its envelope must return the silent exit error, or Execute's
// reportError prints a second document ("baryovm") after it.
func TestVMHealthFailureEmitsExactlyOneDocumentAndIsSilent(t *testing.T) {
	cases := []struct {
		name string
		host healthHost
		args []string
	}{
		{"bad threshold", healthyHost("0"), []string{"vm1", "--disk-warn", "95", "--disk-fail", "90"}},
		{"failing check", healthyHost("12"), []string{"vm1"}},
		{"unknown vm", healthyHost("0"), []string{"nope"}},
		{"no name", healthyHost("0"), nil},
		{"all and a name", healthyHost("0"), []string{"vm1", "--all"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			healthFleet(t, "vm1")
			stubDial(t, map[string]sshx.Runner{"vm1": tc.host})
			stdout, _, err := runCmdE(t, newVMHealthCmd(), nil, true, tc.args...)
			if err == nil {
				t.Fatal("expected an error")
			}
			if _, silent := exitCodeOf(err); !silent {
				t.Errorf("error is not silent, so reportError would print a second document: %v", err)
			}
			if n := documents(t, stdout); n != 1 {
				t.Errorf("stdout holds %d JSON documents, want 1: %s", n, stdout)
			}
		})
	}
}
