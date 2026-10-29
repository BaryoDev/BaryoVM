package backup

import (
	"errors"
	"strings"
	"testing"
)

type seqRunner struct {
	cmds []string
	outs []string
	errs []error
}

func (s *seqRunner) Run(cmd string) (string, error) {
	i := len(s.cmds)
	s.cmds = append(s.cmds, cmd)
	var out string
	var err error
	if i < len(s.outs) {
		out = s.outs[i]
	}
	if i < len(s.errs) {
		err = s.errs[i]
	}
	return out, err
}

func TestDrillScriptCommands(t *testing.T) {
	s := DrillScript(baseCfg(), "", "baryovm-drill-app-1")
	for _, want := range []string{
		`f=$(ls -1t "$BK"/db-*.dump | head -1)`,
		`img=$(docker inspect -f '{{.Config.Image}}' 'app-postgres-1')`,
		`docker run -d --name "$D" --network none`,
		`docker exec -i "$D" createdb -U 'postgres' 'appdb'`,
		`docker exec -i "$D" pg_restore --no-owner --no-privileges --exit-on-error -U 'postgres' -d 'appdb' < "$f"`,
		`information_schema.tables`,
		`D='baryovm-drill-app-1'`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("script lacks %q:\n%s", want, s)
		}
	}
}

func TestDrillScriptNamedFileAndSudo(t *testing.T) {
	s := DrillScript(sudoCfg(), "db-1.dump", "d1")
	if !strings.Contains(s, `f='db-1.dump'`) || !strings.Contains(s, `case "$f" in /*)`) {
		t.Errorf("named file not resolved:\n%s", s)
	}
	for _, ln := range strings.Split(s, "\n") {
		if strings.Contains(ln, "docker") && !strings.Contains(ln, "sudo -n docker") {
			t.Errorf("docker line without sudo -n: %s", ln)
		}
	}
	if !strings.Contains(s, "-U 'app'") {
		t.Errorf("db user not used:\n%s", s)
	}
}

// The cleanup is an EXIT trap set before anything can fail, and it is not conditional on success.
func TestDrillCleanupRunsOnTheFailurePath(t *testing.T) {
	s := DrillScript(baseCfg(), "", "d1")
	trap := strings.Index(s, "trap '")
	run := strings.Index(s, "docker run")
	if trap < 0 || !strings.Contains(s[trap:], "rm -f -v \"$D\"") || !strings.Contains(s[trap:], "' EXIT") {
		t.Fatalf("no EXIT trap removing the container:\n%s", s)
	}
	if trap > run {
		t.Error("the trap must be set before the container is created")
	}
	if !strings.Contains(s, "trap 'exit 1' HUP INT TERM") {
		t.Error("signals must route through the EXIT trap")
	}
}

func TestDrillRunsCleanupAgainWhenTheScriptFails(t *testing.T) {
	r := &seqRunner{errs: []error{errors.New("pg_restore failed")}, outs: []string{"drill: file=/b/db-1.dump\n"}}
	res, err := Drill(r, baseCfg(), "", "d1")
	if err == nil {
		t.Fatal("expected the script error")
	}
	if len(r.cmds) != 2 || r.cmds[1] != baseCfg().DrillCleanupCmd("d1") {
		t.Fatalf("cleanup must run after a failure: %q", r.cmds)
	}
	if !res.Removed || res.File != "/b/db-1.dump" || res.Touched {
		t.Errorf("result = %+v", res)
	}
}

func TestDrillSuccessParsesResult(t *testing.T) {
	r := &seqRunner{outs: []string{"drill: file=/b/db-2.dump\ndrill: tables=7\ndrill ok\ndrill: removed d1\n"}}
	res, err := Drill(r, baseCfg(), "", "d1")
	if err != nil {
		t.Fatal(err)
	}
	if res.Tables != 7 || res.File != "/b/db-2.dump" || !res.Removed || !res.Drill {
		t.Errorf("result = %+v", res)
	}
}

func TestDrillCleanupFailureIsReported(t *testing.T) {
	r := &seqRunner{errs: []error{nil, errors.New("ssh gone")}}
	res, err := Drill(r, baseCfg(), "", "d1")
	if err == nil || res.Removed {
		t.Fatalf("a cleanup that failed must not read as removed: %+v %v", res, err)
	}
}

// The live container is named only by the read-only inspect. Nothing writes to it.
func TestDrillNeverWritesToTheLiveContainer(t *testing.T) {
	cfg := sudoCfg()
	for _, script := range []string{DrillScript(cfg, "", "d1"), DrillScript(cfg, "x.dump", "d1"), cfg.DrillCleanupCmd("d1")} {
		for _, ln := range strings.Split(script, "\n") {
			if !strings.Contains(ln, cfg.DBContainer) {
				continue
			}
			if !strings.Contains(ln, "docker inspect -f") {
				t.Errorf("live container named outside a read-only inspect: %s", ln)
			}
			for _, bad := range []string{" exec ", " rm ", " stop", " restart", " kill", " run ", " cp "} {
				if strings.Contains(ln, bad) {
					t.Errorf("live container named in a %q command: %s", bad, ln)
				}
			}
		}
	}
	if !strings.Contains(DrillNote, "no --yes") {
		t.Error("the note must say plainly that no --yes is needed")
	}
}
