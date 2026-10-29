// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"github.com/BaryoDev/BaryoVM/internal/repomove"
)

const (
	oldURL = "https://github.com/acme/app"
	newURL = "https://dev.azure.com/acme/proj/_git/app"
)

func sha(n int) string { return strings.Repeat("a", 39) + fmt.Sprint(n) }

// repoWorld fakes both Git hosts, the gh and az CLIs and the local clone, and records every argv
// in order. Nothing here touches a network.
type repoWorld struct {
	t            *testing.T
	hosts        map[string]map[string]string // url to refs
	calls        []string
	localRemotes map[string]string
	cloned       string
	createURL    string
	failPush     bool
	dropTag      bool
	rateLimit    bool
	vms          map[string]*fakeCheckoutVM
	dialed       int
}

func newRepoWorld(t *testing.T) *repoWorld {
	t.Helper()
	w := &repoWorld{
		t: t,
		hosts: map[string]map[string]string{
			oldURL: {"refs/heads/main": sha(1), "refs/heads/dev": sha(2), "refs/tags/v1": sha(3)},
		},
		localRemotes: map[string]string{"origin": oldURL + ".git"},
		createURL:    newURL,
		vms:          map[string]*fakeCheckoutVM{},
	}
	prevExec, prevTmp, prevDial := repoExec, repoTempDir, repoDial
	repoExec = w.exec
	repoTempDir = func() (string, func(), error) { return "/tmp/mirror-scratch", func() {}, nil }
	repoDial = func(vm *fleet.VM) (repomove.Remote, func(), error) {
		w.dialed++
		v := w.vms[vm.Name]
		if v == nil {
			return nil, func() {}, fmt.Errorf("no fake VM %s", vm.Name)
		}
		return v, func() {}, nil
	}
	t.Cleanup(func() { repoExec, repoTempDir, repoDial = prevExec, prevTmp, prevDial })
	return w
}

func (w *repoWorld) fail(out string) (string, int, error) {
	return out, 128, fmt.Errorf("exit status 128")
}

func (w *repoWorld) exec(argv []string) (string, int, error) {
	w.calls = append(w.calls, strings.Join(argv, " "))
	switch {
	case argv[0] == "git" && argv[1] == "ls-remote":
		refs, ok := w.hosts[argv[len(argv)-1]]
		if !ok {
			return w.fail("fatal: repository not found")
		}
		var names []string
		for r := range refs {
			names = append(names, r)
		}
		sort.Strings(names)
		var b strings.Builder
		for _, r := range names {
			b.WriteString(refs[r] + "\t" + r + "\n")
		}
		return b.String(), 0, nil
	case argv[0] == "git" && argv[1] == "clone":
		w.cloned = argv[3]
		if _, ok := w.hosts[w.cloned]; !ok {
			return w.fail("fatal: repository not found")
		}
		return "", 0, nil
	case argv[0] == "git" && argv[1] == "-C" && argv[3] == "push":
		if w.failPush {
			return w.fail("remote: access denied")
		}
		dst := argv[4]
		if _, ok := w.hosts[dst]; !ok {
			return w.fail("fatal: repository not found")
		}
		for r, s := range w.hosts[w.cloned] {
			if w.dropTag && strings.HasPrefix(r, "refs/tags/") {
				continue
			}
			w.hosts[dst][r] = s
		}
		return "", 0, nil
	case argv[0] == "git" && argv[1] == "-C" && argv[3] == "remote":
		switch argv[4] {
		case "get-url":
			if u, ok := w.localRemotes[argv[5]]; ok {
				return u + "\n", 0, nil
			}
			return w.fail("error: No such remote")
		case "rename":
			u, ok := w.localRemotes[argv[5]]
			if !ok {
				return w.fail("error: No such remote")
			}
			delete(w.localRemotes, argv[5])
			w.localRemotes[argv[6]] = u
		case "add":
			w.localRemotes[argv[5]] = argv[6]
		case "remove":
			delete(w.localRemotes, argv[5])
		}
		return "", 0, nil
	case argv[0] == "gh" && argv[1] == "api":
		if w.rateLimit {
			return "gh: API rate limit exceeded (HTTP 403)", 1, fmt.Errorf("exit status 1")
		}
		path := argv[len(argv)-1]
		switch {
		case strings.Contains(path, "/issues/comments"):
			return `[[{"id":1,"issue_url":"https://api.github.com/repos/acme/app/issues/1"},{"id":2,"issue_url":"https://api.github.com/repos/acme/app/issues/2"}]]`, 0, nil
		case strings.Contains(path, "/pulls/comments"):
			return `[[{"id":9,"pull_request_url":"https://api.github.com/repos/acme/app/pulls/4"}]]`, 0, nil
		case strings.Contains(path, "/pulls?"):
			return `[[{"number":2,"title":"pr two"}],[{"number":4,"title":"pr four"}]]`, 0, nil
		case strings.Contains(path, "/issues?"):
			return `[[{"number":1,"title":"one"},{"number":2,"title":"pr two","pull_request":{}}],[{"number":3,"title":"three"}]]`, 0, nil
		}
	case (argv[0] == "gh" && argv[1] == "repo" && argv[2] == "create") || (argv[0] == "az" && argv[1] == "repos"):
		w.hosts[w.createURL] = map[string]string{}
		return "", 0, nil
	}
	return w.fail("fake: unexpected " + strings.Join(argv, " "))
}

func (w *repoWorld) index(prefix string) int {
	for i, c := range w.calls {
		if strings.HasPrefix(c, prefix) {
			return i
		}
	}
	return -1
}

func (w *repoWorld) count(sub string) int {
	n := 0
	for _, c := range w.calls {
		if strings.Contains(c, sub) {
			n++
		}
	}
	return n
}

// fakeCheckoutVM answers the remote commands of the search and the repoint.
type fakeCheckoutVM struct {
	cmds       []string
	stdin      []string
	origins    map[string]string // dir to origin
	dirty      map[string]bool
	owners     map[string]string // dir to the stat output; default is one owner for both
	cron       map[string]string // dir to a reference line
	failSetURL bool
	approveErr string
}

func (v *fakeCheckoutVM) Run(cmd string) (string, error) {
	v.cmds = append(v.cmds, cmd)
	if strings.Contains(cmd, "find ") {
		var dirs []string
		for d := range v.origins {
			dirs = append(dirs, d)
		}
		sort.Strings(dirs)
		var b strings.Builder
		for _, d := range dirs {
			b.WriteString(d + "/.git\n")
		}
		return b.String(), nil
	}
	if strings.Contains(cmd, "stat -c %U") {
		for d := range v.origins {
			if strings.Contains(cmd, "'"+d+"' ") {
				if o, ok := v.owners[d]; ok {
					return o, nil
				}
				return "app\napp\n", nil
			}
		}
	}
	for d, origin := range v.origins {
		if !strings.Contains(cmd, "-C '"+d+"' ") && !strings.Contains(cmd, "-F -- '"+d+"' ") {
			continue
		}
		switch {
		case strings.Contains(cmd, "remote get-url origin"):
			return origin + "\n", nil
		case strings.Contains(cmd, "status --porcelain"):
			if v.dirty[d] {
				return " M app.go\n", nil
			}
			return "", nil
		case strings.Contains(cmd, "remote set-url origin"):
			if v.failSetURL && !strings.Contains(cmd, oldURL) {
				return "", fmt.Errorf("permission denied")
			}
			i := strings.Index(cmd, "set-url origin '")
			u := strings.TrimSuffix(cmd[i+len("set-url origin '"):], "'")
			v.origins[d] = u
			return "", nil
		case strings.Contains(cmd, "config credential.helper"):
			return "", nil
		case strings.Contains(cmd, "grep -sn"):
			return v.cron[d], nil
		}
	}
	return "", fmt.Errorf("fakeCheckoutVM: no answer for %q", cmd)
}

func (v *fakeCheckoutVM) RunStdin(cmd string, in io.Reader) (string, error) {
	b, _ := io.ReadAll(in)
	v.cmds = append(v.cmds, cmd)
	v.stdin = append(v.stdin, string(b))
	if v.approveErr != "" {
		return "", fmt.Errorf("%s", v.approveErr)
	}
	return "", nil
}

func repoFixture(t *testing.T) (*repoWorld, *fakeCheckoutVM) {
	t.Helper()
	t.Setenv("BARYOVM_HOME", t.TempDir())
	t.Setenv("BARYOVM_ACTIVITY", "")
	store := &fleet.Store{
		VMs: []fleet.VM{
			{Name: "vm1", Host: "10.0.0.1", User: "opc", KeyPath: "/keys/id"},
			{Name: "vm2", Host: "10.0.0.2", User: "opc", KeyPath: "/keys/id"},
		},
		Stacks: []fleet.Stack{{Name: "app", VM: "vm1", Dir: "/opt/deploy", Sudo: true, Source: oldURL}},
	}
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	w := newRepoWorld(t)
	v1 := &fakeCheckoutVM{
		origins: map[string]string{
			"/opt/deploy/app":   oldURL + ".git",
			"/opt/deploy/dev":   "git@github.com:acme/app.git",
			"/opt/deploy/other": "https://github.com/acme/other",
		},
		dirty: map[string]bool{"/opt/deploy/dev": true},
		cron:  map[string]string{"/opt/deploy/app": "/etc/cron.d/app:1:0 3 * * * deploy /opt/deploy/app/backup.sh\n"},
	}
	w.vms["vm1"] = v1
	return w, v1
}

func moveArgs(extra ...string) []string {
	return append([]string{"app", "--to", newURL, "--local", "/work/app", "--export-dir", "", "--vm", "vm1"}, extra...)
}

func runMove(t *testing.T, args ...string) (repomove.Report, string, string, error) {
	t.Helper()
	stdout, stderr, err := runCmdE(t, newRepoMoveCmd(), nil, true, args...)
	var env struct {
		Data repomove.Report `json:"data"`
	}
	_ = json.Unmarshal([]byte(stdout), &env)
	return env.Data, stdout, stderr, err
}

func withExport(t *testing.T, args []string) ([]string, string) {
	dir := filepath.Join(t.TempDir(), "export")
	out := append([]string{}, args...)
	for i, a := range out {
		if a == "--export-dir" {
			out[i+1] = dir
		}
	}
	return out, dir
}

func TestRepoMovePlanDoesNothingWithoutYes(t *testing.T) {
	w, _ := repoFixture(t)
	for _, extra := range [][]string{{}, {"--yes", "--dry-run"}} {
		args, dir := withExport(t, moveArgs(append([]string{"--from", oldURL}, extra...)...))
		stdout, _, err := runCmdE(t, newRepoMoveCmd(), nil, true, args...)
		if err != nil {
			t.Fatal(err)
		}
		if len(w.calls) != 0 || w.dialed != 0 {
			t.Fatalf("a plan sent calls: %v dialed %d", w.calls, w.dialed)
		}
		if !strings.Contains(stdout, `"moved": false`) || !strings.Contains(stdout, `"steps"`) {
			t.Errorf("plan envelope: %s", stdout)
		}
		if _, err := os.Stat(dir); err == nil {
			t.Error("a plan wrote the export directory")
		}
	}
	if got := reloadStack(t, "app"); got.Source != oldURL {
		t.Errorf("a plan changed the recorded source: %q", got.Source)
	}
}

func TestRepoMoveHappyPathOrderAndResult(t *testing.T) {
	w, vm := repoFixture(t)
	delete(w.hosts, newURL)
	args, dir := withExport(t, moveArgs("--create", "--yes"))
	rep, stdout, stderr, err := runMove(t, args...)
	if err != nil {
		t.Fatalf("%v\n%s\n%s", err, stdout, stderr)
	}

	iSrc := w.index("git ls-remote --heads --tags " + oldURL)
	iExport := w.index("gh api")
	iCreate := w.index("az repos create")
	iClone := w.index("git clone --mirror")
	iPush := -1
	for i, c := range w.calls {
		if strings.Contains(c, " push ") {
			iPush = i
		}
	}
	iRename := w.index("git -C /work/app remote rename origin old-origin")
	iAdd := w.index("git -C /work/app remote add origin " + newURL)
	for name, i := range map[string]int{"ls-remote": iSrc, "export": iExport, "create": iCreate, "clone": iClone, "push": iPush, "rename": iRename, "add": iAdd} {
		if i < 0 {
			t.Fatalf("missing call %s in %v", name, w.calls)
		}
	}
	if !(iSrc < iExport && iExport < iCreate && iCreate < iClone && iClone < iPush && iPush < iRename && iRename < iAdd) {
		t.Fatalf("calls out of order: %v", w.calls)
	}
	if strings.Contains(strings.Join(w.calls, "\n"), "--mirror "+newURL) || strings.Contains(strings.Join(w.calls, "\n"), "--force") || strings.Contains(strings.Join(w.calls, "\n"), " -f ") {
		t.Errorf("a force or mirror push was used: %v", w.calls)
	}
	// the VM was only changed after the push and the compare
	var setURL string
	for _, c := range vm.cmds {
		if strings.Contains(c, "remote set-url") {
			setURL = c
		}
	}
	want := "sudo -n -u 'app' env GIT_CONFIG_NOSYSTEM=1 git -c core.fsmonitor= -c core.hooksPath=/dev/null -C '/opt/deploy/app' remote set-url origin '" + newURL + "'"
	if setURL != want {
		t.Errorf("set-url command: %q", setURL)
	}
	if vm.origins["/opt/deploy/app"] != newURL {
		t.Errorf("deploy copy not repointed: %q", vm.origins["/opt/deploy/app"])
	}
	if vm.origins["/opt/deploy/dev"] != "git@github.com:acme/app.git" {
		t.Errorf("a checkout with local changes was touched: %q", vm.origins["/opt/deploy/dev"])
	}
	if vm.origins["/opt/deploy/other"] != "https://github.com/acme/other" {
		t.Errorf("an unrelated repository was touched")
	}
	if w.localRemotes["origin"] != newURL || w.localRemotes["old-origin"] != oldURL+".git" {
		t.Errorf("local remotes: %v", w.localRemotes)
	}
	if got := reloadStack(t, "app"); got.Source != newURL {
		t.Errorf("source not recorded: %q", got.Source)
	}
	if !rep.Created || !rep.Mirrored || !rep.Recorded || rep.Refs != 3 {
		t.Errorf("report: %+v", rep)
	}
	var dirty bool
	for _, c := range rep.Checkouts {
		if c.Dir == "/opt/deploy/dev" && c.Dirty && !c.Repointed {
			dirty = true
		}
	}
	if !dirty {
		t.Errorf("the dirty checkout is not reported: %+v", rep.Checkouts)
	}
	if len(rep.References) != 1 || !strings.Contains(rep.References[0].Match, "/opt/deploy/app/backup.sh") {
		t.Errorf("cron reference not listed: %+v", rep.References)
	}
	if !strings.Contains(strings.Join(rep.NextCommands, "|"), "gh repo archive acme/app") {
		t.Errorf("old repository commands: %v", rep.NextCommands)
	}
	if w.count("repo delete") != 0 || w.count("repo archive") != 0 {
		t.Error("the old repository was touched")
	}
	if rep.Export == nil || rep.Export.Dir != dir {
		t.Errorf("export report: %+v", rep.Export)
	}
}

func TestRepoMoveExportHoldsEveryIssueAndPullRequestWithComments(t *testing.T) {
	w, _ := repoFixture(t)
	w.hosts[newURL] = map[string]string{}
	args, dir := withExport(t, moveArgs("--from", oldURL, "--yes", "--local", ""))
	rep, stdout, _, err := runMove(t, args...)
	if err != nil {
		t.Fatalf("%v %s", err, stdout)
	}
	if rep.Export == nil || rep.Export.Issues != 2 || rep.Export.PullRequests != 2 || rep.Export.IssueComments != 2 || rep.Export.ReviewComments != 1 {
		t.Fatalf("export report: %+v", rep.Export)
	}
	read := func(name string) []map[string]any {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		var l []map[string]any
		if err := json.Unmarshal(b, &l); err != nil {
			t.Fatal(err)
		}
		return l
	}
	issues, pulls := read("issues.json"), read("pulls.json")
	if len(issues) != 2 || issues[0]["number"].(float64) != 1 || issues[1]["number"].(float64) != 3 {
		t.Fatalf("issues: %v", issues)
	}
	if len(issues[0]["comments_data"].([]any)) != 1 || len(issues[1]["comments_data"].([]any)) != 0 {
		t.Errorf("issue comments: %v", issues)
	}
	if len(pulls) != 2 || len(pulls[0]["comments_data"].([]any)) != 1 || len(pulls[1]["review_comments_data"].([]any)) != 1 {
		t.Errorf("pull requests: %v", pulls)
	}
	if st, _ := os.Stat(filepath.Join(dir, "issues.json")); st.Mode().Perm() != 0o600 {
		t.Errorf("export mode %v", st.Mode().Perm())
	}
}

func TestRepoMoveRateLimitStopsTheExportAndMovesNothing(t *testing.T) {
	w, vm := repoFixture(t)
	w.rateLimit = true
	w.hosts[newURL] = map[string]string{}
	args, dir := withExport(t, moveArgs("--yes"))
	_, _, _, err := runMove(t, args...)
	if err == nil || !strings.Contains(err.Error(), "rate limit") {
		t.Fatalf("want a rate limit error, got %v", err)
	}
	if _, serr := os.Stat(dir); serr == nil {
		t.Error("a half export was written")
	}
	if w.index("git clone") >= 0 || vm.origins["/opt/deploy/app"] != oldURL+".git" || reloadStack(t, "app").Source != oldURL {
		t.Error("something moved after a failed export")
	}
}

func TestRepoMoveARefMissingOnTheNewSideStopsTheMove(t *testing.T) {
	w, vm := repoFixture(t)
	w.hosts[newURL] = map[string]string{}
	w.dropTag = true
	args, _ := withExport(t, moveArgs("--yes"))
	_, _, _, err := runMove(t, args...)
	if err == nil || !strings.Contains(err.Error(), "refs/tags/v1") || !strings.Contains(err.Error(), "missing on the new side") {
		t.Fatalf("want the missing tag named, got %v", err)
	}
	for _, c := range vm.cmds {
		if strings.Contains(c, "set-url") {
			t.Errorf("a checkout was repointed after the compare failed: %s", c)
		}
	}
	if w.localRemotes["origin"] != oldURL+".git" || w.index("git -C /work/app remote rename") >= 0 {
		t.Errorf("local clone changed: %v", w.localRemotes)
	}
	if reloadStack(t, "app").Source != oldURL {
		t.Error("source recorded despite the failed compare")
	}
}

func TestRepoMoveRefusesADestinationWithDifferentCommits(t *testing.T) {
	w, _ := repoFixture(t)
	w.hosts[newURL] = map[string]string{"refs/heads/main": sha(9)}
	args, _ := withExport(t, moveArgs("--yes"))
	_, _, _, err := runMove(t, args...)
	if err == nil || !strings.Contains(err.Error(), "never force-pushes") {
		t.Fatalf("want a refusal, got %v", err)
	}
	if w.index("git clone") >= 0 || w.count(" push ") != 0 || w.index("gh api") >= 0 {
		t.Errorf("work started against a non-empty destination: %v", w.calls)
	}
}

func TestRepoMoveMissingDestinationNeedsCreate(t *testing.T) {
	w, _ := repoFixture(t)
	delete(w.hosts, newURL)
	args, _ := withExport(t, moveArgs("--yes"))
	_, _, _, err := runMove(t, args...)
	if err == nil || !strings.Contains(err.Error(), "--create") {
		t.Fatalf("got %v", err)
	}
	if w.index("az repos") >= 0 || w.index("git clone") >= 0 {
		t.Errorf("created without --create: %v", w.calls)
	}
}

func TestRepoMoveCannotCreateOnAnUnmodelledHost(t *testing.T) {
	w, _ := repoFixture(t)
	args, _ := withExport(t, []string{"app", "--to", "https://git.example.com/team/app", "--from", oldURL, "--local", "", "--export-dir", "", "--vm", "vm1", "--create", "--yes"})
	_, _, _, err := runMove(t, args...)
	if err == nil || !strings.Contains(err.Error(), "by hand") {
		t.Fatalf("got %v", err)
	}
	if w.index("git clone") >= 0 {
		t.Error("mirrored to a repository that does not exist")
	}
}

func TestRepoMoveFailureMidwayLeavesTheOldRemoteInPlace(t *testing.T) {
	w, v1 := repoFixture(t)
	v2 := &fakeCheckoutVM{
		origins:    map[string]string{"/srv/dev-stack": oldURL},
		failSetURL: true,
		cron:       map[string]string{},
	}
	w.vms["vm2"] = v2
	args, _ := withExport(t, []string{"app", "--to", newURL, "--local", "/work/app", "--export-dir", "", "--yes"})
	w.hosts[newURL] = map[string]string{}
	_, _, _, err := runMove(t, args...)
	if err == nil || !strings.Contains(err.Error(), "repoint vm2:/srv/dev-stack") {
		t.Fatalf("got %v", err)
	}
	if v1.origins["/opt/deploy/app"] != oldURL+".git" {
		t.Errorf("the first checkout was left on the new remote: %q", v1.origins["/opt/deploy/app"])
	}
	if v2.origins["/srv/dev-stack"] != oldURL {
		t.Errorf("failed checkout changed: %q", v2.origins["/srv/dev-stack"])
	}
	if w.localRemotes["origin"] != oldURL+".git" || w.localRemotes["old-origin"] != "" {
		t.Errorf("local clone changed: %v", w.localRemotes)
	}
	if reloadStack(t, "app").Source != oldURL {
		t.Error("source recorded after a failed move")
	}
}

func TestRepoMoveRerunAfterAFinishedMoveChangesNothing(t *testing.T) {
	w, vm := repoFixture(t)
	w.hosts[newURL] = map[string]string{}
	args, dir := withExport(t, moveArgs("--yes"))
	if _, out, _, err := runMove(t, args...); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	before := len(w.calls)
	cmdsBefore := len(vm.cmds)
	info, _ := os.Stat(filepath.Join(dir, "issues.json"))

	// explicit --from: the hosts are compared and found identical
	rep, out, _, err := runMove(t, args...)
	if err != nil {
		t.Fatalf("%v %s", err, out)
	}
	for _, c := range w.calls[before:] {
		for _, bad := range []string{"clone", " push ", "create", "rename", "remote add", "gh api"} {
			if strings.Contains(c, bad) {
				t.Errorf("rerun made a change: %s", c)
			}
		}
	}
	for _, c := range vm.cmds[cmdsBefore:] {
		if strings.Contains(c, "set-url") || strings.Contains(c, "credential") {
			t.Errorf("rerun repointed a VM: %s", c)
		}
	}
	if rep.Mirrored || rep.Created || rep.Recorded {
		t.Errorf("rerun report claims work: %+v", rep)
	}
	after, _ := os.Stat(filepath.Join(dir, "issues.json"))
	if !after.ModTime().Equal(info.ModTime()) {
		t.Error("rerun rewrote the export")
	}

	// no --from: the stack already records the new source, so nothing is even read
	before = len(w.calls)
	noFrom, _ := withExport(t, []string{"app", "--to", newURL, "--local", "/work/app", "--vm", "vm1", "--export-dir", dir, "--yes"})
	rep, out, _, err = runMove(t, noFrom...)
	if err != nil || !rep.AlreadyMoved || len(w.calls) != before {
		t.Fatalf("second rerun: %v %+v calls %v", err, rep, w.calls[before:])
	}
}

func TestRepoMoveReadCredNeverReachesArgvErrorsOrTheLog(t *testing.T) {
	const token = "s3cr3t-token-value-123"
	t.Setenv("BARYOVM_RM_TOKEN", token)
	w, vm := repoFixture(t)
	w.hosts[newURL] = map[string]string{}
	args, _ := withExport(t, moveArgs("--yes", "--read-cred", "env:BARYOVM_RM_TOKEN"))
	_, stdout, stderr, err := runMove(t, args...)
	if err != nil {
		t.Fatalf("%v %s", err, stdout)
	}
	all := strings.Join(w.calls, "\n") + "\n" + strings.Join(vm.cmds, "\n") + stdout + stderr
	if strings.Contains(all, token) {
		t.Fatalf("the token leaked into argv, commands or output")
	}
	if len(vm.stdin) != 1 || !strings.Contains(vm.stdin[0], "password="+token) || !strings.Contains(vm.stdin[0], "host=dev.azure.com") {
		t.Errorf("token not sent on stdin: %q", vm.stdin)
	}
	if !strings.Contains(strings.Join(vm.cmds, "\n"), "umask 077") {
		t.Error("credential file is not created private")
	}

	// a failing store whose own error carries the token
	w2, vm2 := repoFixture(t)
	w2.hosts[newURL] = map[string]string{}
	vm2.approveErr = "credential store failed for " + token
	args2, _ := withExport(t, moveArgs("--yes", "--read-cred", "env:BARYOVM_RM_TOKEN"))
	root := newRoot()
	root.SetArgs(append([]string{"repo", "move"}, args2...))
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	err = runRoot(root)
	if err == nil || strings.Contains(err.Error(), token) {
		t.Fatalf("want an error without the token, got %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(os.Getenv("BARYOVM_HOME"), "activity.jsonl"))
	if len(b) == 0 || strings.Contains(string(b), token) {
		t.Errorf("activity log: %q", b)
	}
	if vm2.origins["/opt/deploy/app"] != oldURL+".git" {
		t.Error("a failed credential store did not undo the repoint")
	}
}

func TestRepoMoveRefusesURLsThatCarryATokenAndSendsNothing(t *testing.T) {
	w, _ := repoFixture(t)
	for _, to := range []string{"https://ghp_secret123@github.com/o/r", "https://user:pw0rd@git.example.com/o/r", "--upload-pack=x"} {
		args, _ := withExport(t, []string{"app", "--to=" + to, "--from", oldURL, "--local", "", "--export-dir", "", "--yes"})
		_, _, _, err := runMove(t, args...)
		if err == nil || strings.Contains(err.Error(), "ghp_secret123") || strings.Contains(err.Error(), "pw0rd") {
			t.Errorf("%s: %v", to, err)
		}
	}
	if len(w.calls) != 0 {
		t.Errorf("calls were made: %v", w.calls)
	}
}

func TestRepoMoveRefusesACheckoutOwnedByTheWrongUserBeforeAnyGitCommand(t *testing.T) {
	w, vm := repoFixture(t)
	w.hosts[newURL] = map[string]string{}
	vm.owners = map[string]string{"/opt/deploy/app": "app\nmallory\n"}
	args, _ := withExport(t, moveArgs("--yes"))
	rep, stdout, stderr, err := runMove(t, args...)
	if err != nil {
		t.Fatalf("%v %s %s", err, stdout, stderr)
	}
	for _, c := range vm.cmds {
		if strings.Contains(c, "git ") && strings.Contains(c, "/opt/deploy/app'") {
			t.Errorf("git ran on a checkout with mixed owners: %s", c)
		}
	}
	if vm.origins["/opt/deploy/app"] != oldURL+".git" {
		t.Error("a refused checkout was repointed")
	}
	var refused bool
	for _, c := range rep.Checkouts {
		if c.Dir == "/opt/deploy/app" && strings.Contains(c.Skipped, "refused") && !c.Repointed {
			refused = true
		}
	}
	if !refused {
		t.Errorf("the refusal is not reported: %+v", rep.Checkouts)
	}
}

func TestRepoMoveOnlyLooksUnderTheDirectoriesOfRegisteredStacks(t *testing.T) {
	_, vm := repoFixture(t)
	vm.origins["/srv/elsewhere"] = oldURL + ".git"
	args, _ := withExport(t, moveArgs("--from", oldURL, "--yes"))
	if _, _, _, err := runMove(t, args...); err != nil {
		t.Fatal(err)
	}
	for _, c := range vm.cmds {
		if strings.Contains(c, "/srv/elsewhere") && !strings.Contains(c, "find ") {
			t.Errorf("a command touched a checkout outside every stack directory: %s", c)
		}
	}
	if vm.origins["/srv/elsewhere"] != oldURL+".git" {
		t.Error("a checkout outside every stack directory was repointed")
	}
}

func TestRepoMoveNeedsAKnownStackAndSource(t *testing.T) {
	repoFixture(t)
	if _, _, _, err := runMove(t, "ghost", "--to", newURL); err == nil {
		t.Error("an unknown stack was accepted")
	}
	store, _ := fleet.Load()
	store.FindStack("app").Source = ""
	_ = store.Save()
	if _, _, _, err := runMove(t, "app", "--to", newURL, "--local", "", "--yes"); err == nil || !strings.Contains(err.Error(), "--from") {
		t.Errorf("got %v", err)
	}
}
