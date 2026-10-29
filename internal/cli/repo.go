// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"github.com/BaryoDev/BaryoVM/internal/repomove"
	"github.com/BaryoDev/BaryoVM/internal/ui"
	"github.com/spf13/cobra"
)

// repoExec runs git, gh and az on this machine as an argv, never a shell. A variable so tests fake
// the hosts and no test needs a network. The real one is the same runner the managed database
// snapshot uses.
var repoExec repomove.Exec = func(argv []string) (string, int, error) { return managedExec(argv) }

// repoTempDir makes the scratch directory the mirror clone lives in. A variable for the same reason.
var repoTempDir = func() (string, func(), error) {
	dir, err := os.MkdirTemp("", "baryovm-repo-move-")
	if err != nil {
		return "", func() {}, err
	}
	return dir, func() { _ = os.RemoveAll(dir) }, nil
}

// repoDial connects to a registered VM for the checkout search and repoint. A variable so tests
// hand back a fake VM. The returned func closes the connection.
var repoDial = func(vm *fleet.VM) (repomove.Remote, func(), error) {
	c, err := dialChecked(vm)
	if err != nil {
		return nil, func() {}, err
	}
	return c, func() { _ = c.Close() }, nil
}

func newRepoCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "repo",
		Short: "Move a stack's source repository to another Git host",
	}
	cmd.AddCommand(newRepoMoveCmd())
	return cmd
}

type repoMoveFlags struct {
	to, from, local, exportDir string
	vms                        []string
	readCred, readUser         string
	create, yes, dryRun        bool
}

func newRepoMoveCmd() *cobra.Command {
	var f repoMoveFlags
	cmd := &cobra.Command{
		Use:   "move <stack>",
		Short: "Move a stack's repository to another Git host and repoint what pulls from it",
		Long: strings.TrimSpace(`
Moves the source repository of a stack to another Git host without losing a branch or tag, and
repoints the places that pull from the old one. Without --yes (or with --dry-run) it prints the
plan and does nothing: no call to either host, no connection to a VM.

With --yes, in this order:

  1. read the branches and tags of both sides, and refuse when the destination already has
     commits that differ from the source (an identical destination is fine, so a rerun is safe)
  2. find checkouts of the repository on the registered VMs (--vm narrows the search), with
     the crontab and systemd lines that run from them; a checkout with local changes is
     reported and left alone
  3. export issues and pull requests, with their comments, from GitHub to issues.json and
     pulls.json in --export-dir (other hosts have nowhere to import them, so you keep the files)
  4. create the destination when --create is given (GitHub with gh, Azure Repos with az, using
     their own sign-in; any other host must be created by hand)
  5. push every branch and tag (no force, nothing deleted) and compare the two ref lists; they must
     be identical or nothing is repointed
  6. set origin of each checkout on the VMs, over SSH, with sudo -n where the stack has sudo
  7. repoint the local clone, keeping the old remote as old-origin
  8. record the new source on the stack

If a step after the push fails, the ones before it are put back. The old repository is never
deleted or archived: the commands for that are printed last. The crontab and systemd lines found
are listed so you can repoint them to the deployed copy; they are not edited.

No token goes on a command line or into an error. Both hosts use the sign-in of git, gh and az on
this machine. --read-cred takes a reference (env:NAME, file:PATH or cmd:<command>) to a read-only
token for https remotes; it is sent to the VM on stdin and stored in the checkout's .git directory
for git's credential store. URLs that carry a user or token are refused.
`),
		Example: "  baryovm repo move app --to https://dev.azure.com/org/proj/_git/app --create\n" +
			"  baryovm repo move app --to git@git.example.com:team/app.git --local ~/src/app --yes",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			err := runRepoMove(args[0], f)
			if err != nil {
				ui.Emit(ui.Result{OK: false, Action: "repo move", Error: err.Error()})
			}
			return err
		},
	}
	cmd.Flags().StringVar(&f.to, "to", "", "the new remote URL (required)")
	cmd.Flags().StringVar(&f.from, "from", "", "the current remote URL (default the stack's recorded source, then the local clone's origin)")
	cmd.Flags().StringVar(&f.local, "local", ".", "the local clone to repoint (its origin must be the old remote; \"\" skips it)")
	cmd.Flags().StringVar(&f.exportDir, "export-dir", "", "where issues.json and pulls.json go (default ./<stack>-repo-export)")
	cmd.Flags().StringSliceVar(&f.vms, "vm", nil, "VM to search for checkouts (repeatable; default every registered VM)")
	cmd.Flags().StringVar(&f.readCred, "read-cred", "", "reference to a read-only token for https checkouts: env:NAME, file:PATH or cmd:<command>")
	cmd.Flags().StringVar(&f.readUser, "read-user", "baryovm", "user name stored with the --read-cred token")
	cmd.Flags().BoolVar(&f.create, "create", false, "create the destination repository when it does not exist")
	cmd.Flags().BoolVar(&f.yes, "yes", false, "really move; without it the plan is printed and nothing is done")
	cmd.Flags().BoolVar(&f.dryRun, "dry-run", false, "print the plan and do nothing (this is already the default without --yes)")
	_ = cmd.MarkFlagRequired("to")
	return cmd
}

// repoMovePlan is the Data of a plan-only run.
type repoMovePlan struct {
	DryRun    bool     `json:"dryRun"`
	Moved     bool     `json:"moved"`
	Stack     string   `json:"stack"`
	From      string   `json:"from"`
	To        string   `json:"to"`
	ExportDir string   `json:"exportDir"`
	VMs       []string `json:"vms"`
	Steps     []string `json:"steps"`
}

func runRepoMove(stack string, f repoMoveFlags) error {
	store, err := fleet.Load()
	if err != nil {
		return err
	}
	st := store.FindStack(stack)
	if st == nil {
		return fmt.Errorf("no stack named %q: register it with `baryovm stack add %s --vm ... --path ...`", stack, stack)
	}
	to, err := repomove.Parse(f.to)
	if err != nil {
		return fmt.Errorf("--to: %w", err)
	}
	fromURL := f.from
	if fromURL == "" {
		fromURL = st.Source
	}
	if fromURL == "" && f.local != "" {
		if out, exit, err := repoExec([]string{"git", "-C", f.local, "remote", "get-url", "origin"}); err == nil && exit == 0 {
			fromURL = strings.TrimSpace(out)
		}
	}
	if fromURL == "" {
		return fmt.Errorf("the stack has no recorded source: pass --from <current remote URL>, or run from the local clone")
	}
	from, err := repomove.Parse(fromURL)
	if err != nil {
		return fmt.Errorf("--from: %w", err)
	}
	if f.readCred != "" {
		if err := cmdResolver().Check(f.readCred); err != nil {
			return fmt.Errorf("--read-cred: %w", err)
		}
	}
	exportDir := f.exportDir
	if exportDir == "" {
		exportDir = "./" + stack + "-repo-export"
	}
	var targets []repomove.VMTarget
	byName := map[string]*fleet.VM{}
	var notes []string
	for i := range store.VMs {
		vm := &store.VMs[i]
		if len(f.vms) > 0 && !containsStr(f.vms, vm.Name) {
			continue
		}
		if vm.Forced() && len(f.vms) == 0 {
			notes = append(notes, fmt.Sprintf("VM %s only runs a forced command and was not searched", vm.Name))
			continue
		}
		t := repomove.VMTarget{Name: vm.Name}
		for _, s := range store.Stacks {
			if s.VM == vm.Name {
				t.Roots = append(t.Roots, s.Dir)
				t.Sudo = t.Sudo || s.Sudo
			}
		}
		targets = append(targets, t)
		byName[vm.Name] = vm
	}
	for _, n := range f.vms {
		if byName[n] == nil {
			return fmt.Errorf("no VM named %q: register it with `baryovm vm add %s --host ... --key ...`", n, n)
		}
	}

	if f.dryRun || !f.yes {
		return emitRepoMovePlan(stack, from, to, exportDir, targets, f, notes)
	}
	if repomove.Same(from, to) && st.Source == to.URL {
		ui.Emit(ui.Result{OK: true, Action: "repo move", Message: "the stack already uses " + to.URL + ": nothing changed",
			Data: &repomove.Report{Stack: stack, From: from.URL, To: to.URL, AlreadyMoved: true, Checkouts: []repomove.Checkout{}, References: []repomove.Reference{}, Done: []string{}}})
		return nil
	}

	var closers []func()
	defer func() {
		for _, c := range closers {
			c()
		}
	}()
	o := repomove.Options{
		Stack: stack, From: from, To: to, Create: f.create, ExportDir: exportDir,
		LocalDir: f.local, VMs: targets, CredUser: f.readUser,
	}
	if f.readCred != "" {
		o.Cred = func() (string, error) { return cmdResolver().Resolve(f.readCred) }
	}
	d := repomove.Deps{
		Exec: repoExec, TempDir: repoTempDir,
		Dial: func(name string) (repomove.Remote, error) {
			r, closeFn, err := repoDial(byName[name])
			if err != nil {
				return nil, err
			}
			closers = append(closers, closeFn)
			return r, nil
		},
		Record: func(url string) (bool, error) {
			if st.Source == url {
				return false, nil
			}
			s, err := fleet.Load()
			if err != nil {
				return false, err
			}
			cur := s.FindStack(stack)
			if cur == nil {
				return false, fmt.Errorf("stack %q is gone", stack)
			}
			cur.Source = url
			return true, s.Save()
		},
	}
	var rep *repomove.Report
	err = ui.Step("moving "+from.URL+" to "+to.URL, func() error {
		var err error
		rep, err = repomove.Run(o, d)
		return err
	})
	if rep != nil {
		rep.Notes = append(rep.Notes, notes...)
	}
	if err != nil {
		ui.Emit(ui.Result{OK: false, Action: "repo move", Data: rep, Error: err.Error()})
		return err
	}
	if !ui.JSON() {
		printRepoMove(rep)
	}
	msg := fmt.Sprintf("moved %s to %s: %d refs on both sides", stack, to.URL, rep.Refs)
	changed := rep.Mirrored || rep.Created || rep.Recorded || countRepointed(rep) > 0 || strings.Contains(rep.Local, "now points")
	if !changed {
		msg = "the repository is already at " + to.URL + ": nothing changed"
	}
	ui.Emit(ui.Result{OK: true, Action: "repo move", Message: msg, Data: rep})
	return nil
}

func countRepointed(r *repomove.Report) int {
	n := 0
	for _, c := range r.Checkouts {
		if c.Repointed {
			n++
		}
	}
	return n
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func emitRepoMovePlan(stack string, from, to repomove.Repo, exportDir string, targets []repomove.VMTarget, f repoMoveFlags, notes []string) error {
	p := &repoMovePlan{DryRun: f.dryRun, Stack: stack, From: from.URL, To: to.URL, ExportDir: filepath.Clean(exportDir), VMs: []string{}}
	for _, t := range targets {
		p.VMs = append(p.VMs, t.Name)
	}
	p.Steps = append(p.Steps,
		"read the branches and tags of "+from.URL+" and "+to.URL+"; refuse if the destination has commits that differ",
		"find checkouts of the repository on: "+orNone(strings.Join(p.VMs, ", "))+" (a checkout with local changes is left alone)",
		"list crontab and systemd lines that run from those checkouts",
	)
	if from.Kind == repomove.GitHub {
		p.Steps = append(p.Steps, "export issues and pull requests with comments to "+p.ExportDir)
	} else {
		p.Steps = append(p.Steps, "no export: "+from.Host+" is not GitHub")
	}
	if f.create {
		p.Steps = append(p.Steps, "create "+to.URL+" if it does not exist")
	}
	p.Steps = append(p.Steps,
		"push every branch and tag to "+to.URL+" (no force) and compare the two ref lists",
		"set origin of each checkout to "+to.URL,
	)
	if f.local != "" {
		p.Steps = append(p.Steps, "repoint the local clone "+f.local+", old remote kept as old-origin")
	}
	p.Steps = append(p.Steps, "record the new source on stack "+stack, "print the commands to retire the old repository (never run)")
	if !ui.JSON() {
		ui.Title("repo move plan (nothing is done without --yes)")
		for i, s := range p.Steps {
			ui.Detail(fmt.Sprintf("%d", i+1), s)
		}
		for _, n := range notes {
			ui.Notef("%s", n)
		}
	}
	ui.Emit(ui.Result{OK: true, Action: "repo move", Message: "plan only: nothing was done, rerun with --yes to move it", Data: p})
	return nil
}

func orNone(s string) string {
	if s == "" {
		return "no VM"
	}
	return s
}

func printRepoMove(r *repomove.Report) {
	for _, l := range r.Done {
		ui.Detail("done", l)
	}
	for _, c := range r.Checkouts {
		if c.Skipped != "" {
			ui.Notef("%s:%s %s", c.VM, c.Dir, c.Skipped)
		}
	}
	for _, ref := range r.References {
		ui.Notef("%s runs from %s: %s (repoint it to the deployed copy)", ref.VM, ref.Checkout, ref.Match)
	}
	for _, n := range r.Notes {
		ui.Notef("%s", n)
	}
	for _, c := range r.NextCommands {
		ui.Detail("when you are sure", c)
	}
}
