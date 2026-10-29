// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"

	"github.com/BaryoDev/BaryoVM/internal/backup"
	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"github.com/BaryoDev/BaryoVM/internal/ui"
	"github.com/spf13/cobra"
)

// parseBackupSpec reads a JSON array of strategies. Unknown keys are refused, so a misspelt field
// is an error now rather than a backup that quietly lacks it. An empty array is allowed: it says
// the stack has nothing to back up.
func parseBackupSpec(b []byte) ([]backup.Strategy, error) {
	list := []backup.Strategy{}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&list); err != nil {
		return nil, err
	}
	if err := backup.ValidateStrategies(list); err != nil {
		return nil, err
	}
	return list, nil
}

// newStackSetBackupCmd stores the stack's backup strategies, the list `stack backup`, `stack
// restore` and the backup that opens every release all read.
func newStackSetBackupCmd() *cobra.Command {
	var file string
	var clear bool
	cmd := &cobra.Command{
		Use:   "set-backup <name>",
		Short: "Set a stack's backup strategies (database, remote database, command, files)",
		Long: "Stores the stack's backup as a list of strategies in fleet.json. Each has a kind and its own\n" +
			"settings, and declares how it is dumped and how it is restored:\n\n" +
			"  postgres-container  container, database, user (pg_dump inside a container on the VM)\n" +
			"  postgres-remote     host, port, database, user, image, envFile or passFile (a managed database)\n" +
			"  command             dump and restore, shell lines run on the VM (dump writes stdout, restore reads stdin)\n" +
			"  files               paths, absolute (a tar)\n\n" +
			"A release runs every strategy before anything changes and stops at the first failure. The file\n" +
			"is a JSON array; an empty array, [], says the stack has nothing to back up, which is what\n" +
			"`stack update --auto` needs from a stack with no database. --clear removes the declaration.",
		Example: "  baryovm stack set-backup shop --file backup.json\n" +
			"  baryovm stack set-backup site --file empty.json\n" +
			"  baryovm stack set-backup shop --clear",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if clear == (file != "") {
				return fmt.Errorf("give exactly one of --file <backup.json> or --clear")
			}
			store, err := fleet.Load()
			if err != nil {
				return err
			}
			st := store.FindStack(args[0])
			if st == nil {
				return fmt.Errorf("no stack named %q", args[0])
			}
			if clear {
				st.Backup = nil
			} else {
				b, err := os.ReadFile(file) // #nosec G304 -- the spec file is the user's own --file argument
				if err != nil {
					return err
				}
				list, err := parseBackupSpec(b)
				if err != nil {
					return fmt.Errorf("read %s: %w", file, err)
				}
				// The list replaces the legacy fields, so the two never disagree.
				st.Backup = &list
				st.DBContainer, st.DBName, st.DBUser, st.NoDatabase = "", "", "", false
			}
			if err := store.Save(); err != nil {
				return err
			}
			list, declared := st.BackupStrategies()
			if ui.JSON() {
				ui.Emit(ui.Result{OK: true, Action: "stack set-backup", Message: st.Name, Data: map[string]any{
					"strategies": len(list), "declared": declared, "backup": list,
				}})
				return nil
			}
			ui.Successf("%s: %d backup strategies, declared=%v", st.Name, len(list), declared)
			return nil
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "JSON file holding the array of backup strategies")
	cmd.Flags().BoolVar(&clear, "clear", false, "remove the stack's backup declaration")
	return cmd
}
