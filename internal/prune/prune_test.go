package prune

import (
	"errors"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

func opts() Options {
	return Options{WorkDirs: []string{"/home/u/work"}, WorktreeDirs: []string{"/home/u/wt"}, KeepImages: 2, Now: func() time.Time { return now }}
}

func has(p Plan, kind, target string) bool {
	for _, it := range p.Items {
		if it.Kind == kind && it.Target == target {
			return true
		}
	}
	return false
}

func TestNamedVolumeIsNeverPlanned(t *testing.T) {
	anon := strings.Repeat("ab", 32)
	p := BuildPlan(Facts{HasDocker: true, Volumes: []Volume{{Name: "pgdata"}, {Name: "app_uploads"}, {Name: anon}}}, opts())
	if has(p, "docker-volume", "pgdata") || has(p, "docker-volume", "app_uploads") {
		t.Fatalf("named volume planned: %+v", p.Items)
	}
	if !has(p, "docker-volume", anon) {
		t.Fatalf("anonymous volume should be planned: %+v", p.Items)
	}
}

func TestImageUsedByAnyContainerIsNeverPlanned(t *testing.T) {
	f := Facts{HasDocker: true,
		Images: []Image{
			{ID: "aaa111aaa111", Repo: "<none>", Tag: "<none>"},
			{ID: "bbb222bbb222", Repo: "<none>", Tag: "<none>"},
			{ID: "ccc333ccc333", Repo: "app", Tag: "v1", Created: now.Add(-3 * time.Hour)},
			{ID: "ddd444ddd444", Repo: "app", Tag: "v2", Created: now.Add(-2 * time.Hour)},
			{ID: "eee555eee555", Repo: "app", Tag: "v3", Created: now.Add(-1 * time.Hour)},
			{ID: "fff666fff666", Repo: "app", Tag: "v0", Created: now.Add(-9 * time.Hour)},
		},
		Containers: []Container{
			{Name: "/run", State: "running", ImageID: "sha256:aaa111aaa111ffff"},
			{Name: "/old", State: "exited", Finished: now.Add(-1 * time.Hour), ImageID: "sha256:x", ImageRef: "app:v0"},
		}}
	p := BuildPlan(f, opts())
	if has(p, "docker-image", "aaa111aaa111") {
		t.Fatal("dangling image of a running container planned")
	}
	if !has(p, "docker-image", "bbb222bbb222") {
		t.Fatal("unused dangling image should be planned")
	}
	if has(p, "docker-image", "app:v0") {
		t.Fatal("image used by a stopped container planned")
	}
	// v3 and v2 are the newest two, v1 is older and unused.
	if !has(p, "docker-image", "app:v1") || has(p, "docker-image", "app:v2") || has(p, "docker-image", "app:v3") {
		t.Fatalf("rollback images wrong: %+v", p.Items)
	}
}

func TestRollbackKeepsNewestN(t *testing.T) {
	var ims []Image
	for i := 0; i < 5; i++ {
		ims = append(ims, Image{ID: string(rune('a'+i)) + "0000000000x", Repo: "web", Tag: string(rune('1' + i)), Created: now.Add(time.Duration(i) * time.Hour)})
	}
	o := opts()
	o.KeepImages = 3
	p := BuildPlan(Facts{HasDocker: true, Images: ims}, o)
	n := 0
	for _, it := range p.Items {
		if it.Kind == "docker-image" {
			n++
		}
	}
	if n != 2 || !has(p, "docker-image", "web:1") || !has(p, "docker-image", "web:2") {
		t.Fatalf("want the 2 oldest planned: %+v", p.Items)
	}
}

func TestStoppedContainerAge(t *testing.T) {
	f := Facts{HasDocker: true, Containers: []Container{
		{Name: "/old", State: "exited", Finished: now.Add(-15 * 24 * time.Hour)},
		{Name: "/new", State: "exited", Finished: now.Add(-2 * 24 * time.Hour)},
		{Name: "/up", State: "running"},
	}}
	p := BuildPlan(f, opts())
	if !has(p, "docker-container", "old") || has(p, "docker-container", "new") || has(p, "docker-container", "up") {
		t.Fatalf("%+v", p.Items)
	}
}

func TestBinWithoutProjectFileIsNeverPlanned(t *testing.T) {
	f := Facts{
		Projects: []string{"/home/u/work/api"},
		Dirs: []Dir{
			{Path: "/home/u/work/api/bin", Bytes: 5},
			{Path: "/home/u/work/api/obj", Bytes: 5},
			{Path: "/home/u/work/scripts/bin", Bytes: 5},
		}}
	p := BuildPlan(f, opts())
	if has(p, "build-output", "/home/u/work/scripts/bin") {
		t.Fatal("bin without a project file planned")
	}
	if !has(p, "build-output", "/home/u/work/api/bin") || !has(p, "build-output", "/home/u/work/api/obj") {
		t.Fatalf("%+v", p.Items)
	}
}

func TestRootOwnedObjIsDeletedWithSudo(t *testing.T) {
	f := Facts{Projects: []string{"/home/u/work/api"}, Dirs: []Dir{{Path: "/home/u/work/api/obj"}}}
	p := BuildPlan(f, opts())
	if len(p.Items) != 1 || !strings.HasPrefix(p.Items[0].Cmd, "sudo -n rm -rf -- ") {
		t.Fatalf("%+v", p.Items)
	}
}

func TestBuildOutputSkippedWithoutWorkDirs(t *testing.T) {
	o := opts()
	o.WorkDirs = nil
	f := Facts{Projects: []string{"/home/u/work/api"}, Dirs: []Dir{{Path: "/home/u/work/api/bin"}}}
	p := BuildPlan(f, o)
	if len(p.Items) != 0 || len(p.Notes) == 0 {
		t.Fatalf("%+v", p)
	}
}

func TestDirtyWorktreeIsNeverPlanned(t *testing.T) {
	f := Facts{HasGit: true, Worktrees: []Worktree{
		{Path: "/home/u/wt/dirty", Main: "/home/u/repo", Dirty: true, Unpushed: 0, Branch: "a"},
		{Path: "/home/u/wt/ahead", Main: "/home/u/repo", Unpushed: 2, Branch: "b"},
		{Path: "/home/u/wt/unknown", Main: "/home/u/repo", Unpushed: -1, Branch: "c"},
		{Path: "/home/u/wt/clean", Main: "/home/u/repo", Unpushed: 0, Branch: "d"},
	}}
	p := BuildPlan(f, opts())
	if len(p.Items) != 1 || p.Items[0].Target != "/home/u/wt/clean" {
		t.Fatalf("%+v", p.Items)
	}
	if strings.Contains(p.Items[0].Cmd, "--force") || strings.Contains(p.Items[0].Cmd, " -f") {
		t.Fatalf("worktree remove must not force: %s", p.Items[0].Cmd)
	}
}

func TestHostsWithoutDockerOrGitAreSkippedWithNotes(t *testing.T) {
	p := BuildPlan(Facts{}, opts())
	if len(p.Items) != 0 || len(p.Notes) != 2 {
		t.Fatalf("%+v", p)
	}
}

func TestCachesAndEditorServers(t *testing.T) {
	f := Facts{Home: "/home/u",
		Caches: []Dir{{Path: "/home/u/.cache/go-build", Bytes: 9}, {Path: "/etc/.cache", Bytes: 9}},
		Editors: []Editor{
			{Path: "/home/u/.vscode-server/bin/old", MTime: 1},
			{Path: "/home/u/.vscode-server/bin/new", MTime: 2},
		}}
	p := BuildPlan(f, opts())
	if !has(p, "user-cache", "/home/u/.cache/go-build") || has(p, "user-cache", "/etc/.cache") {
		t.Fatalf("%+v", p.Items)
	}
	if !has(p, "editor-server", "/home/u/.vscode-server/bin/old") || has(p, "editor-server", "/home/u/.vscode-server/bin/new") {
		t.Fatalf("%+v", p.Items)
	}
}

func TestParseFactsUnreadableWorktreeStaysUnsafe(t *testing.T) {
	f := ParseFacts("GIT|1\nWT|x|oops|1|/m|b|/home/u/wt/a\nWT|0|0|10|/home/u/repo|feat|/home/u/wt/b\n")
	if len(f.Worktrees) != 2 || !f.Worktrees[0].Dirty || f.Worktrees[0].Unpushed != -1 {
		t.Fatalf("%+v", f.Worktrees)
	}
	if f.Worktrees[1].Dirty || f.Worktrees[1].Branch != "feat" || f.Worktrees[1].Path != "/home/u/wt/b" {
		t.Fatalf("%+v", f.Worktrees[1])
	}
}

func TestParseSize(t *testing.T) {
	for in, want := range map[string]int64{"16.6GB (100%)": 16600000000, "500MB": 500000000, "0B": 0, "12.5kB": 12500, "junk": 0} {
		if got := ParseSize(in); got != want {
			t.Errorf("%q: got %d want %d", in, got, want)
		}
	}
}

type fakeRunner struct {
	cmds []string
	out  func(cmd string) string
}

func (f *fakeRunner) Run(cmd string) (string, error) {
	f.cmds = append(f.cmds, cmd)
	return f.out(cmd), nil
}

func df(freeKB int) string { return "DF|100000000|1000|" + itoa(freeKB) + "\n" }

func itoa(n int) string {
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	if s == "" {
		return "0"
	}
	return s
}

func TestPlanRunsNoDeleteCommand(t *testing.T) {
	r := &fakeRunner{out: func(cmd string) string {
		if strings.HasPrefix(cmd, "df ") {
			return df(1000)
		}
		return "HOME|/home/u\nDOCKER|1\nDFDOCKER|Build Cache|2GB\nCACHE|100|/home/u/.cache/pip\n"
	}}
	p, err := PlanFor(r, opts())
	if err != nil || len(p.Items) == 0 {
		t.Fatalf("%v %+v", err, p)
	}
	for _, c := range r.cmds {
		for _, bad := range []string{"rm -rf", " rmi ", "docker rm ", "volume rm", "builder prune", "worktree remove", "flock"} {
			if strings.Contains(c, bad) {
				t.Fatalf("plan ran a delete (%q): %s", bad, c)
			}
		}
	}
}

func TestFloorAboveFreeSpacePlansNothing(t *testing.T) {
	r := &fakeRunner{out: func(cmd string) string { return df(50 * 1024 * 1024) }}
	o := opts()
	o.FloorGB = 20
	p, err := PlanFor(r, o)
	if err != nil || !p.AboveFloor || len(p.Items) != 0 || len(r.cmds) != 1 {
		t.Fatalf("%v %+v cmds=%d", err, p, len(r.cmds))
	}
}

func TestFloorBelowFreeSpaceStillPlans(t *testing.T) {
	r := &fakeRunner{out: func(cmd string) string {
		if strings.HasPrefix(cmd, "df ") {
			return df(1024 * 1024)
		}
		return "HOME|/home/u\nDOCKER|1\nDFDOCKER|Build Cache|2GB\n"
	}}
	o := opts()
	o.FloorGB = 20
	p, _ := PlanFor(r, o)
	if p.AboveFloor || len(p.Items) != 1 {
		t.Fatalf("%+v", p)
	}
}

func TestApplyTakesFlockAndReportsFreed(t *testing.T) {
	r := &fakeRunner{out: func(cmd string) string {
		if strings.HasPrefix(cmd, "flock ") {
			return "OK|0\nFAIL|1|permission denied\nDONE\n"
		}
		return df(5000)
	}}
	p := Plan{Disk: Disk{FreeBytes: 1000 * 1024}, Items: []Item{
		{Kind: "docker-build-cache", Target: "build cache", Cmd: "sudo -n docker builder prune -f"},
		{Kind: "build-output", Target: "/home/u/work/a/obj", Cmd: "sudo -n rm -rf -- '/home/u/work/a/obj'"}}}
	res, err := Apply(r, p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(r.cmds[0], "flock -n -E 75 ") {
		t.Fatalf("apply must take the lock first: %s", r.cmds[0])
	}
	if len(res.Removed) != 1 || len(res.Failed) != 1 || res.Failed[0].Error != "permission denied" {
		t.Fatalf("%+v", res)
	}
	if res.FreedBytes != 4000*1024 {
		t.Fatalf("freed %d", res.FreedBytes)
	}
}

func TestSecondApplyWhileLockHeldExitsWithClearMessage(t *testing.T) {
	r := &fakeRunner{out: func(cmd string) string { return "LOCKED\n" }}
	_, err := Apply(r, Plan{Items: []Item{{Cmd: "true"}}})
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(err.Error(), "already running") {
		t.Fatal(err)
	}
	if len(r.cmds) != 1 {
		t.Fatalf("nothing else may run after a busy lock: %v", r.cmds)
	}
}

func TestApplyEmptyPlanRunsNothing(t *testing.T) {
	r := &fakeRunner{out: func(cmd string) string { return "" }}
	if _, err := Apply(r, Plan{}); err != nil || len(r.cmds) != 0 {
		t.Fatalf("%v %v", err, r.cmds)
	}
}

func TestDiscoverQuotesDirs(t *testing.T) {
	o := opts()
	o.WorkDirs = []string{"/home/u/it's"}
	if !strings.Contains(DiscoverCmd(o), `'/home/u/it'\''s'`) {
		t.Fatal("work dir not quoted")
	}
}
