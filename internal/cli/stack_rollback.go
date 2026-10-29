// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"fmt"
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/backup"
	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"github.com/BaryoDev/BaryoVM/internal/rollback"
	"github.com/BaryoDev/BaryoVM/internal/sshx"
	"github.com/BaryoDev/BaryoVM/internal/ui"
	"github.com/spf13/cobra"
)

func newStackRollbackCmd() *cobra.Command {
	var list bool
	cmd := &cobra.Command{
		Use:   "rollback <name>",
		Short: "Put back the images the stack ran before its last release or update",
		Long: "Retags the images recorded before the last `stack release` or `stack update` and recreates the\n" +
			"containers from them. Images only: the database is never touched. The matching database backups\n" +
			"are printed, and `stack restore --yes` stays the explicit word for the data.",
		Args:    cobra.ExactArgs(1),
		Example: "  baryovm stack rollback app --list\n  baryovm stack rollback app",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStackRollback(args[0], list)
		},
	}
	cmd.Flags().BoolVar(&list, "list", false, "show what there is to roll back to, change nothing")
	return cmd
}

func runStackRollback(name string, list bool) error {
	const action = "stack rollback"
	fail := func(err error) error {
		ui.Emit(ui.Result{OK: false, Action: action, Error: err.Error()})
		return err
	}
	store, err := fleet.Load()
	if err != nil {
		return err
	}
	st := store.FindStack(name)
	if st == nil {
		return fmt.Errorf("no stack named %q: register it with `baryovm stack add %s --vm ... --path ...`", name, name)
	}
	vm := store.Find(st.VM)
	if vm == nil {
		return fmt.Errorf("stack %q references unknown VM %q", name, st.VM)
	}
	if err := requireLinux(vm, action); err != nil {
		return err
	}

	step := fmt.Sprintf("rolling back %s", name)
	if list {
		step = fmt.Sprintf("reading the rollback record for %s", name)
	}
	var data map[string]any
	var text string
	err = ui.Step(step, func() error {
		_, e := runOnVM(vm, func(c sshx.Runner) (opOutput, error) {
			rec, e := rollback.Load(c, backupConfig(st))
			if e != nil {
				return opOutput{}, e
			}
			if rec == nil {
				return opOutput{}, fmt.Errorf("nothing to roll back to for %s: a record is written by the next `baryovm stack release %s` or `stack update %s`", name, name, name)
			}
			data = map[string]any{"record": rec}
			var b strings.Builder
			fmt.Fprintf(&b, "recorded %s by %s:\n", rec.At, rec.Source)
			for _, i := range rec.Images {
				fmt.Fprintf(&b, "  %s  %s  (%s)\n", i.Service, i.Ref, i.ID)
			}
			// The backups are listed either way, so the data step is one copy away.
			if st.DBContainer != "" && st.DBName != "" {
				if out, e := backup.List(c, backupConfig(st)); e == nil {
					r := backup.DescribeList(out)
					data["backups"] = r.Backups
					if r.Count > 0 {
						b.WriteString("database backups (newest first):\n")
						for _, f := range r.Backups {
							fmt.Fprintf(&b, "  %s\n", f)
						}
					}
				}
			}
			if list {
				text = b.String()
				return opOutput{}, nil
			}
			if e := rollback.Apply(c, composeStack(st), rec); e != nil {
				return opOutput{}, e
			}
			// The stack runs the recorded images again, so the next release or update may record
			// over them.
			if _, e := rollback.SetProtected(c, backupConfig(st), false); e != nil {
				ui.Warnf("could not unprotect the rollback record, the next release or update will keep it: %v", e)
			}
			if st.HealthURL != "" {
				out, perr := c.Run("curl -s -o /dev/null -w '%{http_code}' --max-time 10 " + sshx.Quote(st.HealthURL))
				data["healthy"] = perr == nil && strings.HasPrefix(strings.TrimSpace(out), "2")
			}
			text = b.String()
			return opOutput{}, nil
		})
		return e
	})
	if err != nil {
		return fail(err)
	}
	if !list {
		data["restoredDatabase"] = false
	}
	msg := name + ": rollback record"
	if !list {
		msg = name + " rolled back"
	}
	ui.Emit(ui.Result{OK: true, Action: action, Message: msg, Data: data})
	if t := strings.TrimRight(text, "\n"); t != "" && !ui.JSON() {
		fmt.Println(t)
	}
	if !list {
		if h, ok := data["healthy"].(bool); ok && !h {
			ui.Warnf("%s: the health check did not pass after the rollback", name)
		}
		ui.Notef("the database was not touched: `baryovm stack restore %s --file <backup> --yes` replaces it from a backup", name)
		ui.Successf("%s: rollback done", name)
	}
	return nil
}
