// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"strings"
	"testing"
	"time"
)

// realRepoExec is the production runner, captured before any test swaps repoExec.
var realRepoExec = repoExec

func TestRepoMoveSkippedCheckoutKeepsTheOldSourceRecordedAndARerunFindsIt(t *testing.T) {
	w, vm := repoFixture(t)
	w.hosts[newURL] = map[string]string{}
	vm.origins["/opt/deploy/dev"] = "git@github.com:acme/app.git"
	vm.dirty["/opt/deploy/dev"] = true
	args, _ := withExport(t, moveArgs("--yes"))
	rep, out, _, err := runMove(t, args...)
	if err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if vm.origins["/opt/deploy/dev"] != "git@github.com:acme/app.git" {
		t.Errorf("a checkout with local changes was touched: %q", vm.origins["/opt/deploy/dev"])
	}
	var reported bool
	for _, c := range rep.Checkouts {
		if c.Dir == "/opt/deploy/dev" && c.Dirty && !c.Repointed {
			reported = true
		}
	}
	if !reported {
		t.Errorf("the dirty checkout is not reported: %+v", rep.Checkouts)
	}
	if rep.Recorded || reloadStack(t, "app").Source != oldURL {
		t.Errorf("the new source was recorded while a checkout still uses the old remote: %q", reloadStack(t, "app").Source)
	}
	if !strings.Contains(strings.Join(rep.Notes, "|"), "was not recorded") {
		t.Errorf("no note says why: %v", rep.Notes)
	}

	// the rerun is not "already moved": it comes back to the skipped checkout
	vm.dirty["/opt/deploy/dev"] = false
	rep, out, _, err = runMove(t, args...)
	if err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if rep.AlreadyMoved || vm.origins["/opt/deploy/dev"] != newURL {
		t.Errorf("the rerun did not finish the skipped checkout: %+v %q", rep, vm.origins["/opt/deploy/dev"])
	}
	if !rep.Recorded || reloadStack(t, "app").Source != newURL {
		t.Error("the new source was not recorded once every checkout moved")
	}
}

func TestRepoMoveLocalBranchesFollowTheNewOrigin(t *testing.T) {
	w, _ := repoFixture(t)
	w.hosts[newURL] = map[string]string{}
	args, _ := withExport(t, moveArgs("--yes"))
	if _, out, _, err := runMove(t, args...); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if w.branches["main"] != "origin" || w.branches["dev"] != "origin" {
		t.Errorf("branches still track old-origin: %v", w.branches)
	}
	if w.branches["scratch"] != "other" {
		t.Errorf("a branch of another remote was changed: %v", w.branches)
	}
}

func TestRepoMoveUndoPutsLocalBranchTrackingBack(t *testing.T) {
	w, _ := repoFixture(t)
	w.hosts[newURL] = map[string]string{}
	w.failBranch = "main"
	args, _ := withExport(t, moveArgs("--yes"))
	_, _, _, err := runMove(t, args...)
	if err == nil || !strings.Contains(err.Error(), "point branch main") {
		t.Fatalf("got %v", err)
	}
	if w.localRemotes["origin"] != oldURL+".git" || w.localRemotes["old-origin"] != "" {
		t.Errorf("local remotes after the undo: %v", w.localRemotes)
	}
	if w.branches["main"] != "origin" || w.branches["dev"] != "origin" {
		t.Errorf("branch tracking after the undo: %v", w.branches)
	}
	if reloadStack(t, "app").Source != oldURL {
		t.Error("source recorded after a failed move")
	}
}

func TestRepoMoveUndoRemovesTheCredentialFileAndHelper(t *testing.T) {
	t.Setenv("BARYOVM_RM_TOKEN", "tok-undo-1")
	w, vm := repoFixture(t)
	w.hosts[newURL] = map[string]string{}
	vm.approveErr = "store failed"
	args, _ := withExport(t, moveArgs("--yes", "--read-cred", "env:BARYOVM_RM_TOKEN"))
	if _, _, _, err := runMove(t, args...); err == nil {
		t.Fatal("want the store failure")
	}
	all := strings.Join(vm.cmds, "\n")
	if !strings.Contains(all, "rm -f -- ") || !strings.Contains(all, "baryovm-credentials") || !strings.Contains(all, "--unset-all credential.helper") {
		t.Errorf("the undo left the credential behind: %s", all)
	}
	if vm.origins["/opt/deploy/app"] != oldURL+".git" {
		t.Error("origin not restored")
	}
}

func TestRepoMoveFinishesAPushThatWasCutOffHalfway(t *testing.T) {
	w, _ := repoFixture(t)
	w.hosts[newURL] = map[string]string{"refs/heads/main": sha(1)}
	args, _ := withExport(t, moveArgs("--yes"))
	rep, out, _, err := runMove(t, args...)
	if err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if !rep.Mirrored || w.count(" push ") != 1 || len(w.hosts[newURL]) != 3 {
		t.Errorf("the push was not finished: %+v %v", rep, w.hosts[newURL])
	}
	if !strings.Contains(strings.Join(rep.Notes, "|"), "finishing the push") {
		t.Errorf("no note: %v", rep.Notes)
	}
}

func TestRepoMoveStillRefusesADestinationWithAnExtraRefOrAChangedOne(t *testing.T) {
	for name, refs := range map[string]map[string]string{
		"extra":   {"refs/heads/main": sha(1), "refs/heads/other": sha(7)},
		"changed": {"refs/heads/main": sha(1), "refs/heads/dev": sha(8)},
	} {
		w, _ := repoFixture(t)
		w.hosts[newURL] = refs
		args, _ := withExport(t, moveArgs("--yes"))
		_, _, _, err := runMove(t, args...)
		if err == nil || !strings.Contains(err.Error(), "never force-pushes") {
			t.Errorf("%s: got %v", name, err)
		}
		if w.count(" push ") != 0 {
			t.Errorf("%s: pushed to a destination that differs", name)
		}
	}
}

func TestRepoTimeoutPicksTheDeadlinePerCommand(t *testing.T) {
	for argv, want := range map[string]time.Duration{
		"git clone --mirror u d":         repoCloneTimeout,
		"git -C d push u refs/heads/*:x": repoCloneTimeout,
		"git ls-remote u":                repoReadTimeout,
		"gh api repos/a/b/issues":        repoReadTimeout,
		"az repos create":                repoReadTimeout,
	} {
		if got := repoTimeout(strings.Fields(argv)); got != want {
			t.Errorf("%s: %v want %v", argv, got, want)
		}
	}
}

func TestRepoExecStopsAHungProgramAndNeverPromptsForCredentials(t *testing.T) {
	prev := repoReadTimeout
	repoReadTimeout = 200 * time.Millisecond
	t.Cleanup(func() { repoReadTimeout = prev })
	start := time.Now()
	_, exit, err := realRepoExec([]string{"sleep", "30"})
	if exit != 124 || err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("exit %d err %v", exit, err)
	}
	if time.Since(start) > 10*time.Second {
		t.Errorf("took %v", time.Since(start))
	}
	out, exit, err := realRepoExec([]string{"sh", "-c", "echo $GIT_TERMINAL_PROMPT"})
	if err != nil || exit != 0 || strings.TrimSpace(out) != "0" {
		t.Errorf("GIT_TERMINAL_PROMPT: %q %d %v", out, exit, err)
	}
}

func TestRepoMoveReportsAnOriginWithACredentialAsSkippedWithoutEchoingIt(t *testing.T) {
	w, vm := repoFixture(t)
	w.hosts[newURL] = map[string]string{}
	const credOrigin = "https://deploy:tok-in-origin-9@github.com/acme/app.git"
	vm.origins["/opt/deploy/cred"] = credOrigin
	args, _ := withExport(t, moveArgs("--yes"))
	rep, out, errOut, err := runMove(t, args...)
	if err != nil {
		t.Fatalf("%v %s", err, out)
	}
	var found bool
	for _, c := range rep.Checkouts {
		if c.Dir == "/opt/deploy/cred" && c.Skipped == "origin carries a credential: repoint by hand" && !c.Repointed {
			found = true
		}
	}
	if !found {
		t.Errorf("not reported as skipped: %+v", rep.Checkouts)
	}
	if strings.Contains(out+errOut+strings.Join(w.calls, "\n"), "tok-in-origin-9") {
		t.Error("the credential in the origin was echoed")
	}
	if vm.origins["/opt/deploy/cred"] != credOrigin {
		t.Error("a credential-bearing origin was changed")
	}
	if rep.Recorded {
		t.Error("source recorded while a checkout was left on the old remote")
	}
}

func TestRepoMoveCreateThatFailsBecauseTheRepositoryExistsContinuesWhenReadable(t *testing.T) {
	w, _ := repoFixture(t)
	delete(w.hosts, newURL)
	w.failCreate, w.createExists = true, true
	args, _ := withExport(t, moveArgs("--create", "--yes"))
	rep, out, _, err := runMove(t, args...)
	if err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if !rep.Mirrored || rep.Created || len(w.hosts[newURL]) != 3 {
		t.Errorf("report %+v hosts %v", rep, w.hosts[newURL])
	}
	if !strings.Contains(strings.Join(rep.Notes, "|"), "already existed") {
		t.Errorf("notes: %v", rep.Notes)
	}
}

func TestRepoMoveCreateThatFailsOnAnUnreadableDestinationSaysSo(t *testing.T) {
	w, _ := repoFixture(t)
	delete(w.hosts, newURL)
	w.failCreate = true
	args, _ := withExport(t, moveArgs("--create", "--yes"))
	_, _, _, err := runMove(t, args...)
	if err == nil || !strings.Contains(err.Error(), "exists but is unreadable") {
		t.Fatalf("got %v", err)
	}
	if w.index("git clone") >= 0 {
		t.Error("mirrored to an unreadable destination")
	}
}
