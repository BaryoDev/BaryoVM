// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"github.com/BaryoDev/BaryoVM/internal/sshx"
	"github.com/BaryoDev/BaryoVM/internal/update"
)

// cmdRunner answers the table and the JSON form of one compose command differently, which the
// substring fakeRunner cannot do.
type cmdRunner map[string]string

func (m cmdRunner) Run(cmd string) (string, error) {
	// Longest key first, so " ps --format json" wins over " ps".
	best := ""
	for k := range m {
		if strings.Contains(cmd, k) && len(k) > len(best) {
			best = k
		}
	}
	return m[best], nil
}

func stubRunOnVM(t *testing.T, r sshx.Runner) {
	t.Helper()
	prev := runOnVM
	runOnVM = func(vm *fleet.VM, fn func(c sshx.Runner) (opOutput, error)) (opOutput, error) { return fn(r) }
	t.Cleanup(func() { runOnVM = prev })
}

func TestStackPsAddsTypedContainersBesideOutput(t *testing.T) {
	stackFixture(t)
	stubRunOnVM(t, cmdRunner{
		" ps":               "NAME STATUS\napp-web-1 Up\n",
		" ps --format json": `[{"Name":"app-web-1","Service":"web","State":"running","Health":"healthy","Image":"nginx","Publishers":[{"URL":"0.0.0.0","TargetPort":80,"PublishedPort":8080,"Protocol":"tcp"}]}]`,
	})
	out, _ := runJSON(t, "-o", "json", "stack", "ps", "app")
	r := oneDocument(t, out)
	var d struct {
		Output     string `json:"output"`
		Containers []struct {
			Name, Service, State, Health, Image string
			Ports                               []struct{ Published, Target int }
		} `json:"containers"`
	}
	if err := json.Unmarshal(mustMarshal(t, r.Data), &d); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d.Output, "app-web-1 Up") {
		t.Errorf("output field lost: %s", out)
	}
	if len(d.Containers) != 1 || d.Containers[0].Service != "web" || d.Containers[0].State != "running" ||
		len(d.Containers[0].Ports) != 1 || d.Containers[0].Ports[0].Published != 8080 {
		t.Errorf("containers: %s", out)
	}
}

func TestStackLogsEntriesSplitServiceAndLine(t *testing.T) {
	stackFixture(t)
	stubRunOnVM(t, cmdRunner{" logs": "app-web-1  | GET / 200\napp-db-1   | ready | to accept\nstray line\n"})
	out, _ := runJSON(t, "-o", "json", "stack", "logs", "app")
	r := oneDocument(t, out)
	var d struct {
		Entries []struct{ Service, Line string } `json:"entries"`
	}
	if err := json.Unmarshal(mustMarshal(t, r.Data), &d); err != nil {
		t.Fatal(err)
	}
	want := [][2]string{{"app-web-1", "GET / 200"}, {"app-db-1", "ready | to accept"}, {"", "stray line"}}
	if len(d.Entries) != 3 {
		t.Fatalf("entries: %s", out)
	}
	for i, w := range want {
		if d.Entries[i].Service != w[0] || d.Entries[i].Line != w[1] {
			t.Errorf("entry %d: %+v, want %v", i, d.Entries[i], w)
		}
	}
}

func TestStackBackupsEntriesCarryFileAndTaken(t *testing.T) {
	stackFixture(t)
	stubRunOnVM(t, cmdRunner{"ls -1t": "/home/opc/app-backups/db-20260902-010203.dump\n/home/opc/app-backups/odd.dump\n"})
	out, _ := runJSON(t, "-o", "json", "stack", "backups", "app")
	r := oneDocument(t, out)
	var d struct {
		Backups []string `json:"backups"`
		Entries []struct{ Path, File, Taken string }
	}
	if err := json.Unmarshal(mustMarshal(t, r.Data), &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Backups) != 2 || len(d.Entries) != 2 {
		t.Fatalf("got %s", out)
	}
	if e := d.Entries[0]; e.File != "db-20260902-010203.dump" || e.Taken != "2026-09-02T01:02:03" || e.Path != d.Backups[0] {
		t.Errorf("entry 0: %+v", e)
	}
	if e := d.Entries[1]; e.File != "odd.dump" || e.Taken != "" {
		t.Errorf("entry 1: %+v", e)
	}
}

// stack update keeps its published string fields and adds "result" with real types.
func TestUpdateDataKeepsStringsAndAddsTypedResult(t *testing.T) {
	d := updateData(update.Result{Updated: true, Services: []string{"api", "web"}, Checked: []string{"api"}, Health: "ok"})
	b := mustMarshal(t, d)
	var got struct {
		Updated string `json:"updated"`
		Result  struct {
			Updated  bool     `json:"updated"`
			Services []string `json:"services"`
		} `json:"result"`
		Services string `json:"services"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Updated != "true" || got.Services != "api,web" || !got.Result.Updated || len(got.Result.Services) != 2 {
		t.Errorf("got %s", b)
	}
}
