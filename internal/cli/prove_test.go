// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BaryoDev/BaryoVM/internal/ship"
)

func writeProveRecord(t *testing.T, r ship.Record) {
	t.Helper()
	p, err := ship.RecordPath(baryovmHome(), r.Name, r.Version)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeShipRecord(p, &r); err != nil {
		t.Fatal(err)
	}
}

func proveRec(v string, ok bool) ship.Record {
	return ship.Record{Schema: 1, Name: "lib", Stage: ship.StageFull, Version: v, Tag: "v" + v, Commit: "fcaac08aaaa",
		StartedAt: "2026-08-19T10:00:00Z", Checks: []ship.Check{}, Steps: []ship.StepResult{},
		Published:  []ship.Published{{Package: "Acme.Lib", Version: v}},
		Assertions: []ship.Assertion{{Package: "Acme.Lib", Assert: "version", OK: ok, Observed: v}}, OK: true}
}

func TestProveJSONEnvelopeCarriesTheProofAndFailsOnAFailedAssertion(t *testing.T) {
	t.Setenv("BARYOVM_HOME", t.TempDir())
	writeProveRecord(t, proveRec("1.0.0", true))
	writeProveRecord(t, proveRec("1.1.0", false))

	out, err := runShipCmd(t, newProveCmd(), "lib", "--version", "1.0.0")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	_, data := parseEnvelope(t, out)
	m := dataField(t, data)
	dep, _ := m["deployed"].(map[string]any)
	if m["version"] != "1.0.0" || dep["status"] != "not checked" {
		t.Errorf("proof: %s", data)
	}

	// The default is the highest version, and it failed: the envelope says so and the command errors.
	out, err = runShipCmd(t, newProveCmd(), "lib")
	if err == nil {
		t.Fatalf("a failed assertion exited zero\n%s", out)
	}
	var e struct {
		OK   bool           `json:"ok"`
		Data map[string]any `json:"data"`
	}
	if jerr := json.Unmarshal([]byte(out), &e); jerr != nil || e.OK || e.Data["version"] != "1.1.0" || e.Data["ok"] != false {
		t.Errorf("envelope: %v %s", jerr, out)
	}
}

func TestProveRefusesAMissingRecordAndABadName(t *testing.T) {
	t.Setenv("BARYOVM_HOME", t.TempDir())
	out, err := runShipCmd(t, newProveCmd(), "lib", "--version", "3.0.0")
	want := filepath.Join(baryovmHome(), "ship", "lib", "3.0.0.json")
	if err == nil || !strings.Contains(out, want) {
		t.Errorf("err=%v out=%s", err, out)
	}
	if _, err := runShipCmd(t, newProveCmd(), "../x"); err == nil {
		t.Error("a path-like name was accepted")
	}
	if _, err := runShipCmd(t, newProveCmd(), "lib", "--version", "../../x"); err == nil {
		t.Error("a path-like version was accepted")
	}
}

func TestProveReadsOnlyTheRecord(t *testing.T) {
	// No deps seam, no network client and no fleet are set up: the command must not need any.
	t.Setenv("BARYOVM_HOME", t.TempDir())
	writeProveRecord(t, proveRec("1.0.0", true))
	if _, err := runShipCmd(t, newProveCmd(), "lib"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(baryovmHome(), "fleet.json")); err == nil {
		t.Error("prove touched the fleet")
	}
}
