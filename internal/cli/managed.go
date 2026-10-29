// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"github.com/BaryoDev/BaryoVM/internal/managed"
	"github.com/BaryoDev/BaryoVM/internal/ui"
	"github.com/spf13/cobra"
)

// managedExec runs one cloud CLI call on the operator's machine and returns its combined output
// and exit code. A package variable so tests fake the CLIs; the real one uses exec.Command with
// an argv, never a shell. exit is -1 when the program could not start (not installed).
var managedExec = func(argv []string) (out string, exit int, err error) {
	if len(argv) == 0 {
		return "", -1, fmt.Errorf("empty command")
	}
	cmd := exec.Command(argv[0], argv[1:]...) // #nosec G204 -- argv comes from managed.Spec, validated, no shell
	b, err := cmd.CombinedOutput()
	if err == nil {
		return string(b), 0, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return string(b), ee.ExitCode(), err
	}
	return string(b), -1, err
}

// managedNow is the clock for snapshot names. A variable so tests pin it.
var managedNow = time.Now

// runManaged runs the calls in order and stops at the first one that fails. Every call that ran is
// returned, including the failing one, for the -o json envelope.
func runManaged(argvs [][]string) ([]managed.Call, error) {
	var calls []managed.Call
	for _, argv := range argvs {
		out, exit, err := managedExec(argv)
		c := managed.Call{Argv: argv, ExitCode: exit, Output: managed.Trim(out)}
		if err != nil {
			c.Error = err.Error()
		}
		calls = append(calls, c)
		if err != nil || exit != 0 {
			return calls, managedCallError(c)
		}
	}
	return calls, nil
}

func managedCallError(c managed.Call) error {
	if c.ExitCode == -1 {
		return fmt.Errorf("%s could not run (is it installed and on PATH?): %s", c.Argv[0], c.Error)
	}
	msg := c.Output
	if msg == "" {
		msg = c.Error
	}
	if i := strings.LastIndex(strings.TrimSpace(msg), "\n"); i >= 0 {
		msg = strings.TrimSpace(msg)[i+1:]
	}
	return fmt.Errorf("`%s` exited %d: %s", managed.Join(c.Argv), c.ExitCode, msg)
}

// managedSnapshot takes the pre-release snapshot of the stack's managed database and blocks until
// it is done. The first call is the sign-in check, so a signed-out CLI fails with its hint.
func managedSnapshot(spec managed.Spec) (id string, calls []managed.Call, err error) {
	id = managed.SnapshotID(managedNow())
	argvs, err := spec.SnapshotArgvs(id)
	if err != nil {
		return id, nil, err
	}
	calls, err = runManaged(argvs)
	if err != nil {
		if len(calls) == 1 {
			err = fmt.Errorf("%w: %s", err, spec.SignInHint())
		}
		return id, calls, fmt.Errorf("managed database snapshot of %s failed: %w", spec.Database, err)
	}
	return id, calls, nil
}

// managedDoctorChecks reports, for every distinct profile or subscription a stack names, whether
// its CLI is installed and signed in, and the command to run when it is not. It is required: a
// stack that names a CLI cannot release without it.
func managedDoctorChecks(stacks []fleet.Stack) []doctorCheck {
	var out []doctorCheck
	seen := map[string]bool{}
	for _, st := range stacks {
		if st.Managed == nil {
			continue
		}
		spec := *st.Managed
		name := spec.CLI() + " sign-in (" + spec.Label() + ")"
		if seen[name] {
			continue
		}
		seen[name] = true
		r := doctorCheck{Name: name}
		if err := spec.Validate(); err != nil {
			r.Detail = "stack " + st.Name + ": " + err.Error()
			r.Hint = "fix it with `baryovm stack set-managed " + st.Name + " ...`"
			out = append(out, r)
			continue
		}
		calls, err := runManaged([][]string{spec.SignInArgv()})
		r.Calls = calls
		if err != nil {
			r.Detail = "stack " + st.Name + ": " + err.Error()
			r.Hint = spec.SignInHint()
		} else {
			r.Present, r.Detail = true, "stack "+st.Name+": signed in"
		}
		out = append(out, r)
	}
	return out
}

// newStackSetManagedCmd stores the managed database a stack names. Names only: the AWS or Azure
// CLI's own sign-in does the authentication, so nothing secret reaches fleet.json.
func newStackSetManagedCmd() *cobra.Command {
	var provider, profile, region, subscription, group, database string
	var clear bool
	cmd := &cobra.Command{
		Use:   "set-managed <name>",
		Short: "Name the managed database beside a stack and the AWS profile or Azure subscription that reaches it",
		Long: "Stores which managed database a stack uses and which AWS profile (with a region) or Azure\n" +
			"subscription (with a resource group) reaches it. Only names are stored. BaryoVM runs the\n" +
			"`aws` or `az` already installed and signed in on this machine; no key, token or password\n" +
			"is read or written.\n\n" +
			"A release then snapshots the database first and waits for it (`aws rds create-db-snapshot`\n" +
			"and `aws rds wait db-snapshot-available`, or `az postgres flexible-server backup create`).\n" +
			"--no-backup skips it. `baryovm doctor` checks the CLI is installed and signed in.",
		Example: "  baryovm stack set-managed shop --provider aws --profile prod --region ap-southeast-1 --database shop-db\n" +
			"  baryovm stack set-managed shop --provider azure --subscription prod-sub --resource-group shop-rg --database shop-pg\n" +
			"  baryovm stack set-managed shop --clear",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := fleet.Load()
			if err != nil {
				return err
			}
			st := store.FindStack(args[0])
			if st == nil {
				return fmt.Errorf("no stack named %q", args[0])
			}
			if clear {
				if provider != "" || profile != "" || region != "" || subscription != "" || group != "" || database != "" {
					return fmt.Errorf("--clear takes no other flag")
				}
				st.Managed = nil
			} else {
				spec := managed.Spec{Provider: managed.Provider(provider), Profile: profile, Region: region,
					Subscription: subscription, ResourceGroup: group, Database: database}
				if err := spec.Validate(); err != nil {
					return err
				}
				st.Managed = &spec
			}
			if err := store.Save(); err != nil {
				return err
			}
			if ui.JSON() {
				ui.Emit(ui.Result{OK: true, Action: "stack set-managed", Message: st.Name, Data: map[string]any{"managed": st.Managed}})
				return nil
			}
			if st.Managed == nil {
				ui.Successf("%s: managed database cleared", st.Name)
			} else {
				ui.Successf("%s: managed database %s via %s", st.Name, st.Managed.Database, st.Managed.Label())
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&provider, "provider", "", "aws or azure")
	cmd.Flags().StringVar(&profile, "profile", "", "AWS CLI profile name (aws; empty uses the default profile)")
	cmd.Flags().StringVar(&region, "region", "", "AWS region (aws)")
	cmd.Flags().StringVar(&subscription, "subscription", "", "Azure subscription id or name (azure)")
	cmd.Flags().StringVar(&group, "resource-group", "", "Azure resource group (azure)")
	cmd.Flags().StringVar(&database, "database", "", "RDS instance identifier or flexible server name")
	cmd.Flags().BoolVar(&clear, "clear", false, "remove the stack's managed database")
	return cmd
}
