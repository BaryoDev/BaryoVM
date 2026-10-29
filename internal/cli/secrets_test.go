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

	"github.com/BaryoDev/BaryoVM/internal/deploy"
	"github.com/BaryoDev/BaryoVM/internal/ui"
)

const cliSecret = "cli-s3cr3t-7731"

func TestBuildEnvResolvesEveryKind(t *testing.T) {
	t.Setenv("APP_TOKEN", cliSecret)
	f := filepath.Join(t.TempDir(), "tok")
	if err := os.WriteFile(f, []byte(cliSecret+"2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env, err := buildEnv([]string{"PLAIN=1"}, []string{"A=env:APP_TOKEN", "B=file:" + f, "C=cmd:printf " + cliSecret + "3"}, true)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"PLAIN": "1", "A": cliSecret, "B": cliSecret + "2", "C": cliSecret + "3"}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("%s = %q, want %q", k, env[k], v)
		}
	}
}

func TestBuildEnvUnresolvedFailsNamingTheReference(t *testing.T) {
	t.Setenv("SET_ONE", cliSecret)
	_, err := buildEnv(nil, []string{"OK=env:SET_ONE", "NO=env:CLI_NOT_SET_93"}, true)
	if err == nil || !strings.Contains(err.Error(), "env:CLI_NOT_SET_93") {
		t.Fatalf("want the reference named: %v", err)
	}
	if strings.Contains(err.Error(), cliSecret) {
		t.Errorf("error leaked a value: %v", err)
	}
}

func TestBuildEnvRefusesBadShapesAndDuplicates(t *testing.T) {
	for _, refs := range [][]string{{"NOEQUALS"}, {"K="}, {"K=literal"}, {"K=keychain:x"}} {
		if _, err := buildEnv(nil, refs, true); err == nil {
			t.Errorf("%v should be refused", refs)
		}
	}
	if _, err := buildEnv([]string{"K=1"}, []string{"K=env:HOME"}, true); err == nil {
		t.Error("a key given twice should be refused")
	}
}

// A dry run reads no secret: a reference that would not resolve still plans, and nothing in the
// plan or the environment holds a value.
func TestBuildEnvDryRunDoesNotResolve(t *testing.T) {
	env, err := buildEnv(nil, []string{"K=env:CLI_NOT_SET_93", "C=cmd:touch /tmp/never-run-93"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if env["K"] != "" || env["C"] != "" {
		t.Errorf("dry run resolved something: %v", env)
	}
	if _, err := os.Stat("/tmp/never-run-93"); err == nil {
		t.Error("a dry run ran the cmd reference")
	}
	if _, err := buildEnv(nil, []string{"K=env:1bad"}, false); err == nil {
		t.Error("a dry run still checks syntax")
	}
}

// The JSON envelope and the dry-run plan hold no value.
func TestNoValueInResultOrDryRunPlan(t *testing.T) {
	spec := deploy.Spec{Name: "web", Image: "app:1", Env: map[string]string{"TOKEN": cliSecret}}
	for _, c := range deploy.Commands(spec) {
		if strings.Contains(c, cliSecret) {
			t.Errorf("command carries the value: %s", c)
		}
	}
	b, err := json.Marshal(ui.Result{OK: true, Action: "deploy", Data: deploy.Report{Container: "web", Image: "app:1", ID: "abc"}})
	if err != nil || strings.Contains(string(b), cliSecret) {
		t.Errorf("result holds the value: %s %v", b, err)
	}
}
