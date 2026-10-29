// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"github.com/BaryoDev/BaryoVM/internal/release"
	"github.com/BaryoDev/BaryoVM/internal/releasestate"
	"github.com/BaryoDev/BaryoVM/internal/sshx"
	"github.com/BaryoDev/BaryoVM/internal/status"
	"github.com/BaryoDev/BaryoVM/internal/ui"
	"github.com/BaryoDev/BaryoVM/internal/verify"
	"github.com/spf13/cobra"
)

// dialForcedSession opens the one connection a forced VM gets. A variable so tests can run the
// forced path without a host. Nothing but baryovm-remote requests is ever sent over it.
var dialForcedSession = func(vm *fleet.VM) (remoteSession, error) {
	c, err := sshx.Dial(vm.Target())
	if err != nil {
		return nil, err
	}
	return c, nil
}

func newStatusCmd() *cobra.Command {
	var vmName, stackName string
	var timeout time.Duration
	var workers int
	var noVerify bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Every registered stack on one screen (read only)",
		Long: "Asks each registered VM once, over one SSH connection, and prints a row per stack: whether its\n" +
			"containers are up, the commit of the last finished release, a release that failed or stopped part\n" +
			"way, the verify assertions run now, and the database backups on the VM.\n\n" +
			"A dash means the column is not configured for that stack, which is not a failure. A question\n" +
			"mark means it is configured and could not be read; the reason is in the notes. A VM that does\n" +
			"not answer within --timeout is shown as unreachable with the reason, and every other row still\n" +
			"prints. A VM behind a forced command (`vm remote set`) is asked its status request only and\n" +
			"shows as limited.\n\n" +
			"Nothing is changed on any host. The verify assertions are the stack's own and are run once;\n" +
			"--no-verify skips them. The command exits zero when it produced a report, however many rows\n" +
			"need a look; read `attention` in -o json.",
		Example: "  baryovm status\n" +
			"  baryovm status --vm oracle -o json\n" +
			"  baryovm status --stack shop --timeout 10s",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			const action = "status"
			fail := func(err error) error {
				ui.Emit(ui.Result{OK: false, Action: action, Error: err.Error()})
				return &exitError{code: 1, msg: err.Error(), silent: true}
			}
			if workers < 1 {
				return fail(errors.New("--concurrency must be at least 1"))
			}
			if timeout <= 0 {
				return fail(errors.New("--timeout must be longer than zero"))
			}
			store, err := fleet.Load()
			if err != nil {
				return fail(err)
			}
			jobs, err := statusJobs(store, vmName, stackName)
			if err != nil {
				return fail(err)
			}
			var rep status.Report
			_ = ui.Step("asking the fleet", func() error {
				rep = status.Run(jobs, status.Options{Workers: workers, Timeout: timeout, NoVerify: noVerify})
				return nil
			})
			msg := fmt.Sprintf("%d stack(s) on %d VM(s), %d need a look", rep.Summary.Stacks, rep.Summary.VMs, rep.Summary.Attention)
			if !ui.JSON() {
				printStatus(rep)
			}
			ui.Emit(ui.Result{OK: true, Action: action, Message: msg, Data: rep})
			return nil
		},
	}
	cmd.Flags().StringVar(&vmName, "vm", "", "only this VM")
	cmd.Flags().StringVar(&stackName, "stack", "", "only this stack")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Second, "how long one VM may take, connection included")
	cmd.Flags().IntVar(&workers, "concurrency", 4, "VMs asked at once")
	cmd.Flags().BoolVar(&noVerify, "no-verify", false, "do not run the stacks' verify assertions")
	return cmd
}

// statusJobs turns the registered fleet into one job per VM, scoped by --vm and --stack.
func statusJobs(store *fleet.Store, vmName, stackName string) ([]status.Job, error) {
	if vmName != "" && store.Find(vmName) == nil {
		return nil, fmt.Errorf("no VM named %q: register it with `baryovm vm add %s --host ... --key ...`", vmName, vmName)
	}
	if stackName != "" {
		st := store.FindStack(stackName)
		if st == nil {
			return nil, fmt.Errorf("no stack named %q: register it with `baryovm stack add %s --vm ... --path ...`", stackName, stackName)
		}
		if vmName != "" && st.VM != vmName {
			return nil, fmt.Errorf("stack %q is on VM %q, not %q", stackName, st.VM, vmName)
		}
	}
	if len(store.VMs) == 0 {
		return nil, errors.New("no VMs registered yet: add one with `baryovm vm add`")
	}
	var jobs []status.Job
	for i := range store.VMs {
		vm := store.VMs[i]
		if vmName != "" && vm.Name != vmName {
			continue
		}
		j := status.Job{Name: vm.Name, Host: vm.Host, Forced: vm.Forced(), Stacks: []status.StackInput{}}
		for k := range store.Stacks {
			st := &store.Stacks[k]
			if st.VM != vm.Name || (stackName != "" && st.Name != stackName) {
				continue
			}
			j.Stacks = append(j.Stacks, statusInput(st))
		}
		if stackName != "" && len(j.Stacks) == 0 {
			continue
		}
		vmCopy := vm
		switch {
		case vmCopy.Forced():
			j.Dial = func() (status.Session, error) { return dialForcedSession(&vmCopy) }
			j.Call = callForced
		default:
			if err := requireLinux(&vmCopy, "status"); err != nil {
				e := err
				j.Dial = func() (status.Session, error) { return nil, e }
				break
			}
			j.Dial = func() (status.Session, error) { return dialRelease(&vmCopy) }
		}
		jobs = append(jobs, j)
	}
	return jobs, nil
}

// statusInput resolves, from the local registration alone, what the engine needs for one stack.
func statusInput(st *fleet.Stack) status.StackInput {
	in := status.StackInput{Name: st.Name, Runtime: st.Runtime, Compose: composeStack(st)}
	if st.Managed != nil {
		in.Managed = st.Managed.Label()
	}
	// The release record sits next to the release root, which only a manifest knows. A recipe
	// stack and a stack with no manifest keep it beside the stack's own directory.
	root := st.Dir
	if st.RecipeFile == "" && st.ReleaseFile != "" {
		if m, err := release.Load(st.ReleaseFile, st.Sudo); err != nil {
			in.Note = "release record: manifest not readable, looked beside the stack directory: " + err.Error()
		} else {
			root = m.RemoteRoot
		}
	}
	in.Loc = releasestate.For(root, st.Name, st.Sudo)
	if list, declared := st.BackupStrategies(); declared && len(list) > 0 {
		cfg := backupConfig(st)
		in.Backup = &cfg
	}
	plan, err := st.Verify.Plan(verify.Context{
		Mode: verify.ForUpdate, HealthURL: st.HealthURL, Dir: st.Dir, Sudo: st.Sudo,
		DefaultAttempts: 1, DefaultDelay: time.Second,
	})
	if err != nil {
		if in.Note != "" {
			in.Note += "; "
		}
		in.Note += "verify: " + err.Error()
	} else {
		in.Verify = plan
	}
	return in
}

// ---- rendering: the table is drawn from the same status.Report the JSON carries ----

func printStatus(rep status.Report) {
	if len(rep.Stacks) == 0 && len(rep.VMs) == 0 {
		ui.Notef("nothing registered")
		return
	}
	tw := tabwriter.NewWriter(os.Stderr, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "VM\tSTACK\tSTATE\tUP\tDEPLOYED\tRELEASE\tVERIFY\tBACKUP")
	for _, s := range rep.Stacks {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", s.VM, s.Name, s.State, upCell(s),
			deployedCell(s, time.Now()), releaseCell(s), verifyCell(s), backupCell(s))
	}
	_ = tw.Flush()
	for _, v := range rep.VMs {
		switch {
		case !v.Reachable:
			ui.Errorf("%s (%s): unreachable: %s", v.Name, v.Host, v.Error)
		case len(rep.Stacks) > 0 && v.Forced:
			ui.Notef("%s: forced-command VM, limited to the container list", v.Name)
		}
	}
	for _, s := range rep.Stacks {
		for _, a := range s.Attention {
			if s.State == status.StateUnreachable {
				continue // the VM line above already says why
			}
			ui.Warnf("%s/%s: %s", s.VM, s.Name, a)
		}
		for _, n := range s.Notes {
			if s.Limited && strings.HasPrefix(n, "forced-command") {
				continue
			}
			ui.Notef("%s/%s: could not read %s", s.VM, s.Name, n)
		}
	}
	ui.Notef("%d stack(s) on %d VM(s), %d unreachable, %d need a look",
		rep.Summary.Stacks, rep.Summary.VMs, rep.Summary.Unreachable, rep.Summary.Attention)
}

func unread(s status.Stack, what string) bool {
	for _, n := range s.Notes {
		if strings.HasPrefix(n, what+":") {
			return true
		}
	}
	return false
}

// blank is the cell for a column the stack cannot answer: "limited" on a forced VM, "?" when the
// VM did not answer, "-" when it is not configured.
func blank(s status.Stack, what string) string {
	switch {
	case s.State == status.StateUnreachable:
		return "?"
	case s.Limited:
		return "limited"
	case unread(s, what):
		return "?"
	}
	return "-"
}

func upCell(s status.Stack) string {
	if s.State == status.StateUnreachable || (s.State == status.StateUnknown && s.Total == 0) {
		return "?"
	}
	return fmt.Sprintf("%d/%d", s.Running, s.Total)
}

func deployedCell(s status.Stack, now time.Time) string {
	if s.Deployed == nil {
		return blank(s, "deployed")
	}
	c := s.Deployed.Commit
	if len(c) > 7 {
		c = c[:7]
	}
	if c == "" {
		c = "release"
	}
	if at, err := time.Parse(time.RFC3339, s.Deployed.At); err == nil {
		return c + " " + ago(now.Sub(at))
	}
	return c
}

func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

func releaseCell(s status.Stack) string {
	if s.Release != nil {
		if s.Release.Failed != "" {
			return "FAILED " + s.Release.Failed
		}
		return s.Release.State
	}
	if s.Limited || s.State == status.StateUnreachable || unread(s, "release") {
		return blank(s, "release")
	}
	return "idle"
}

func verifyCell(s status.Stack) string {
	switch {
	case s.Verify == nil:
		return blank(s, "verify")
	case s.Verify.Pass:
		return fmt.Sprintf("%d/%d", s.Verify.Steps, s.Verify.Steps)
	}
	return "FAIL"
}

func backupCell(s status.Stack) string {
	switch {
	case s.Backups == nil:
		return blank(s, "backups")
	case s.Backups.Count == 0:
		return "none"
	}
	if s.Backups.Latest != "" {
		return fmt.Sprintf("%d, %s", s.Backups.Count, strings.SplitN(s.Backups.Latest, "T", 2)[0])
	}
	return fmt.Sprintf("%d", s.Backups.Count)
}
