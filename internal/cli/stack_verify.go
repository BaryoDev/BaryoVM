// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"github.com/BaryoDev/BaryoVM/internal/ui"
	"github.com/BaryoDev/BaryoVM/internal/verify"
	"github.com/spf13/cobra"
)

// newStackSetVerifyCmd stores the stack's verify spec: the one definition of "the deploy worked"
// that `stack release`, `stack update` and `stack rollback` all read.
func newStackSetVerifyCmd() *cobra.Command {
	var file string
	var clear bool

	cmd := &cobra.Command{
		Use:   "set-verify <name>",
		Short: "Set a stack's verify spec (checks, attempts, delay) read by release, update and rollback",
		Long: "Stores a verify spec on the stack in fleet.json. A check is either a route on the stack's own\n" +
			"site with assertions on what it serves (status, minBytes, contains, absent, jsonMatch), or a\n" +
			"command run on the VM in the stack's directory. Routes are fetched from the stack's healthUrl\n" +
			"origin, or from the spec's baseUrl; a check cannot name another host.\n\n" +
			"A stack with no spec keeps verifying with its healthUrl alone. A release manifest's own\n" +
			"`verify` commands still win for a release. --clear removes the spec.",
		Example: "  baryovm stack set-verify shop --file verify.json\n" +
			"  baryovm stack set-verify shop --clear",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if clear == (file != "") {
				return fmt.Errorf("give exactly one of --file <spec.json> or --clear")
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
				st.Verify = nil
			} else {
				b, err := os.ReadFile(file) // #nosec G304 -- the spec file is the user's own --file argument
				if err != nil {
					return err
				}
				var spec verify.Spec
				dec := json.NewDecoder(bytes.NewReader(b))
				if err := dec.Decode(&spec); err != nil {
					return fmt.Errorf("read %s: %w", file, err)
				}
				// Refused here rather than on the next release: Plan is the same validation they run.
				if _, err := spec.Plan(verify.Context{
					HealthURL: st.HealthURL, Dir: st.Dir, Sudo: st.Sudo,
					DefaultAttempts: 5, DefaultDelay: 3 * time.Second,
				}); err != nil {
					return err
				}
				st.Verify = &spec
			}
			if err := store.Save(); err != nil {
				return err
			}
			n := 0
			if st.Verify != nil {
				n = len(st.Verify.Checks)
			}
			if ui.JSON() {
				ui.Emit(ui.Result{OK: true, Action: "stack set-verify", Message: st.Name, Data: map[string]any{
					"checks": n, "verify": st.Verify,
				}})
				return nil
			}
			ui.Successf("%s: verify spec with %d check(s)", st.Name, n)
			return nil
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "JSON file holding the verify spec")
	cmd.Flags().BoolVar(&clear, "clear", false, "remove the stack's verify spec")
	return cmd
}
