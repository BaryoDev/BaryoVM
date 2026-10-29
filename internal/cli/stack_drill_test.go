package cli

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// drillRunner answers the drill script with out/err and anything else (the client-side cleanup)
// with nothing. fakeRunner cannot do this: its answers match by substring and the drill script
// contains every other pattern.
type drillRunner struct {
	seen      []string
	scriptOut string
	scriptErr error
}

func (d *drillRunner) Run(cmd string) (string, error) {
	d.seen = append(d.seen, cmd)
	if strings.Contains(cmd, "set -e") {
		return d.scriptOut, d.scriptErr
	}
	return "", nil
}

type drillEnvelope struct {
	OK    bool           `json:"ok"`
	Error string         `json:"error"`
	Data  map[string]any `json:"data"`
}

func TestStackRestoreDrillSuccessEnvelope(t *testing.T) {
	stackFixture(t)
	r := &drillRunner{scriptOut: "drill: file=/home/opc/app-backups/db-1.dump\ndrill: tables=5\ndrill ok\n"}
	stdout, _ := runCmd(t, newStackRestoreCmd(), r, true, "app", "--drill")
	var env drillEnvelope
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("no envelope: %v (%q)", err, stdout)
	}
	if !env.OK || env.Data["tables"] != float64(5) || env.Data["liveTouched"] != false || env.Data["removed"] != true {
		t.Errorf("envelope = %+v", env)
	}
}

func TestStackRestoreDrillFailureStillCleansUpAndReportsIt(t *testing.T) {
	stackFixture(t)
	r := &drillRunner{scriptOut: "drill: file=/b/db-1.dump\n", scriptErr: errors.New("pg_restore failed")}
	stdout, _, err := runCmdE(t, newStackRestoreCmd(), r, true, "app", "--drill")
	if err == nil {
		t.Fatal("expected the drill to fail")
	}
	var env drillEnvelope
	if e := json.Unmarshal([]byte(stdout), &env); e != nil {
		t.Fatalf("no envelope: %v (%q)", e, stdout)
	}
	if env.OK || env.Error == "" {
		t.Errorf("failure envelope = %+v", env)
	}
	name, _ := env.Data["container"].(string)
	if !strings.HasPrefix(name, "baryovm-drill-app-") || env.Data["removed"] != true || env.Data["liveTouched"] != false {
		t.Errorf("failure data = %v", env.Data)
	}
	last := r.seen[len(r.seen)-1]
	if len(r.seen) != 2 || !strings.HasPrefix(last, "docker rm -f -v '"+name+"'") {
		t.Errorf("cleanup did not run after the failure: %q", r.seen)
	}
}

func TestStackRestoreDrillRefusesYesBeforeAnyRemoteCommand(t *testing.T) {
	stackFixture(t)
	r := &drillRunner{}
	_, _, err := runCmdE(t, newStackRestoreCmd(), r, true, "app", "--drill", "--yes")
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("err = %v", err)
	}
	if len(r.seen) != 0 {
		t.Errorf("ran remote commands: %q", r.seen)
	}
}
