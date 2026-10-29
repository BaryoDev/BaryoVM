// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"github.com/BaryoDev/BaryoVM/internal/ship"
	"github.com/BaryoDev/BaryoVM/internal/ui"
	"github.com/spf13/cobra"
)

const shipHead = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type shipGit struct {
	status string
	calls  []string
}

func (g *shipGit) Run(_ context.Context, _ string, args ...string) (string, error) {
	cmd := strings.Join(args, " ")
	g.calls = append(g.calls, cmd)
	switch {
	case args[0] == "status":
		return g.status, nil
	case cmd == "rev-parse HEAD":
		return shipHead + "\n", nil
	case args[0] == "symbolic-ref":
		return "origin/main\n", nil
	case cmd == "rev-parse --abbrev-ref HEAD":
		return "main\n", nil
	case args[0] == "ls-remote" && strings.Contains(cmd, "refs/heads/"):
		return shipHead + "\trefs/heads/main\n", nil
	case args[0] == "ls-remote":
		return "", nil
	case args[0] == "rev-parse":
		return "", io.EOF
	case args[0] == "remote":
		return "git@github.com:acme/lib.git\n", nil
	}
	return "", nil
}

type notFound struct{}

func (notFound) Do(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader(""))}, nil
}

// shipFixture writes a repository with a csproj and changelog and swaps the deps seam.
func shipFixture(t *testing.T, g *shipGit) (dir string, specPath string) {
	t.Helper()
	t.Setenv("BARYOVM_HOME", t.TempDir())
	dir = t.TempDir()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(filepath.Join(dir, "Lib.csproj"), []byte("<Project><PropertyGroup><Version>1.3.0</Version><PackageId>Acme.Lib</PackageId></PropertyGroup></Project>"), 0o600))
	must(os.WriteFile(filepath.Join(dir, "CHANGELOG.md"), []byte("## 1.3.0\n- a change\n"), 0o600))
	specPath = filepath.Join(dir, "spec.json")
	must(os.WriteFile(specPath, []byte(`{"registry":"nuget","versionFrom":"Lib.csproj"}`), 0o600))

	prev := newShipDeps
	newShipDeps = func(dir string, spec ship.Spec, _ string, _ bool, _ time.Duration) (ship.Deps, error) {
		reg, err := ship.NewRegistry(spec, notFound{})
		return ship.Deps{Dir: dir, Git: g, Registry: reg, ReadFile: os.ReadFile,
			GitHub: func() (ship.GitHub, error) { t.Error("a dry run built the GitHub client"); return nil, io.EOF }}, err
	}
	t.Cleanup(func() { newShipDeps = prev })
	return dir, specPath
}

func runShipCmd(t *testing.T, cmd *cobra.Command, args ...string) (string, error) {
	t.Helper()
	ui.SetJSON(true)
	t.Cleanup(func() { ui.SetJSON(false) })
	outR, outW, _ := os.Pipe()
	prev := os.Stdout
	os.Stdout = outW
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetArgs(args)
	err := cmd.Execute()
	outW.Close()
	os.Stdout = prev
	b, _ := io.ReadAll(outR)
	return string(b), err
}

func TestShipDryRunEmitsTheRecordAndTagsNothing(t *testing.T) {
	g := &shipGit{}
	dir, spec := shipFixture(t, g)
	out, err := runShipCmd(t, newShipCmd(), "lib", "--dry-run", "--repo", dir, "--file", spec)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	e, data := parseEnvelope(t, out)
	if e.Action != "ship" {
		t.Errorf("action = %q", e.Action)
	}
	m := dataField(t, data)
	if m["dryRun"] != true || m["tag"] != "v1.3.0" || m["ok"] != true {
		t.Errorf("record: %s", data)
	}
	for _, c := range g.calls {
		if strings.HasPrefix(c, "tag ") || strings.HasPrefix(c, "push ") {
			t.Errorf("dry run ran %q", c)
		}
	}
	if p, _ := ship.RecordPath(baryovmHome(), "lib", "1.3.0"); p != "" {
		if _, err := os.Stat(p); err == nil {
			t.Error("a dry run saved a record")
		}
	}
}

func TestShipRefusalIsAFailedEnvelopeNamingTheCause(t *testing.T) {
	g := &shipGit{status: " M a.cs\n"}
	dir, spec := shipFixture(t, g)
	out, err := runShipCmd(t, newStackPublishCmd(), "lib", "--repo", dir, "--file", spec, "--yes")
	if err == nil {
		t.Fatal("a dirty tree must refuse")
	}
	var e struct {
		OK     bool   `json:"ok"`
		Action string `json:"action"`
		Error  string `json:"error"`
	}
	if jerr := json.Unmarshal([]byte(out), &e); jerr != nil {
		t.Fatalf("not an envelope: %s", out)
	}
	if e.OK || e.Action != "stack publish" || !strings.Contains(e.Error, "working tree is not clean") {
		t.Errorf("envelope: %+v", e)
	}
	for _, c := range g.calls {
		if strings.HasPrefix(c, "tag ") || strings.HasPrefix(c, "push ") {
			t.Errorf("a refused release ran %q", c)
		}
	}
}

func TestStackSetPublishRoundTripsAndClears(t *testing.T) {
	stackFixture(t)
	spec := filepath.Join(t.TempDir(), "p.json")
	if err := os.WriteFile(spec, []byte(`{"registry":"npm","versionFrom":"package.json","requireDeployed":"app","assert":["x"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := runShipCmd(t, newStackSetPublishCmd(), "app", "--file", spec); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	store, _ := fleet.Load()
	got := store.FindStack("app").Publish
	if got == nil || got.Registry != "npm" || got.RequireDeployed != "app" || len(got.Assert) != 1 {
		t.Fatalf("publish not stored: %+v", got)
	}
	// The stack's setting is what ship reads when no --file is given.
	s, src, err := loadPublishSpec("app", "", t.TempDir())
	if err != nil || s.Registry != "npm" || src != "stack app" {
		t.Errorf("loadPublishSpec: %v %q %+v", err, src, s)
	}
	if out, err := runShipCmd(t, newStackSetPublishCmd(), "app", "--clear"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	store, _ = fleet.Load()
	if store.FindStack("app").Publish != nil {
		t.Error("--clear left the spec")
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	_ = os.WriteFile(bad, []byte(`{"registry":"pypi","versionFrom":"a"}`), 0o600)
	if _, err := runShipCmd(t, newStackSetPublishCmd(), "app", "--file", bad); err == nil {
		t.Error("an invalid spec was stored")
	}
}

type deployedRunner struct {
	out  string
	seen []string
}

func (r *deployedRunner) Run(cmd string) (string, error) {
	r.seen = append(r.seen, cmd)
	return r.out, nil
}
func (r *deployedRunner) Close() error { return nil }

// requireDeployed reads the stack's release record over its VM: this runs that path against a fake VM.
func TestDeployedCommitReadsTheReleaseRecordOverTheVM(t *testing.T) {
	stackFixture(t)
	r := &deployedRunner{out: `{"stack":"app","commit":"abc123","at":"2026-10-30T00:00:00Z"}`}
	prev := dialRelease
	dialRelease = func(vm *fleet.VM) (remoteSession, error) {
		if vm.Name != "vm1" {
			t.Errorf("dialed %q, want the stack's VM vm1", vm.Name)
		}
		return r, nil
	}
	t.Cleanup(func() { dialRelease = prev })

	commit, found, err := deployedCommit(context.Background(), "app")
	if err != nil || !found || commit != "abc123" {
		t.Fatalf("commit=%q found=%v err=%v", commit, found, err)
	}
	if len(r.seen) != 1 || !strings.Contains(r.seen[0], ".baryovm-release-app.deployed.json") {
		t.Errorf("the record was not read from the stack record file: %v", r.seen)
	}

	r.out = ""
	if _, found, err := deployedCommit(context.Background(), "app"); err != nil || found {
		t.Errorf("a stack with no record: found=%v err=%v", found, err)
	}
	if _, _, err := deployedCommit(context.Background(), "nope"); err == nil || !strings.Contains(err.Error(), `no stack named "nope"`) {
		t.Errorf("unknown stack: %v", err)
	}
}
