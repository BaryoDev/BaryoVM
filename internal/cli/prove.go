// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"fmt"
	"os"

	"github.com/BaryoDev/BaryoVM/internal/prove"
	"github.com/BaryoDev/BaryoVM/internal/ship"
	"github.com/BaryoDev/BaryoVM/internal/ui"
	"github.com/spf13/cobra"
)

// runProve renders the stored release record. It reads one local file and calls nothing else.
func runProve(name, version string) error {
	fail := func(err error, data any) error {
		ui.Emit(ui.Result{OK: false, Action: "prove", Error: err.Error(), Data: data})
		return err
	}
	if err := validStackName(name); err != nil {
		return fail(err, nil)
	}
	if version != "" {
		if err := ship.ValidVersion(version); err != nil {
			return fail(err, nil)
		}
	}
	rec, err := prove.Load(baryovmHome(), name, version)
	if err != nil {
		return fail(err, nil)
	}
	p, err := prove.Build(rec)
	if err != nil {
		return fail(err, nil)
	}
	if !p.OK {
		err := fmt.Errorf("%s %s did not pass: %d failure(s) recorded", p.Name, p.Version, len(p.Failures))
		if ui.JSON() {
			return fail(err, p)
		}
		fmt.Fprint(os.Stdout, p.Text())
		return err
	}
	if ui.JSON() {
		ui.Emit(ui.Result{OK: true, Action: "prove", Message: p.Name + " " + p.Version, Data: p})
		return nil
	}
	fmt.Fprint(os.Stdout, p.Text())
	return nil
}

func newProveCmd() *cobra.Command {
	var version string
	cmd := &cobra.Command{
		Use:   "prove <name>",
		Short: "Print the release record of a project as one page: what shipped, what was published, what was deployed",
		Long: "Renders the record `baryovm ship` saved under $BARYOVM_HOME/ship/<name>/, and nothing else:\n" +
			"it never calls GitHub, a registry or a VM. A fact the record does not hold is printed as\n" +
			"not checked, so a project with no deployment check says \"deployment not verified\". Without\n" +
			"--version it picks the highest recorded version by semver order. A failed assertion or\n" +
			"failed step is shown as failed and the command exits non-zero. A dry run is refused.",
		Example: "  baryovm prove shop\n  baryovm prove shop --version 1.2.4\n  baryovm prove shop -o json",
		Args:    cobra.ExactArgs(1),
		RunE:    func(cmd *cobra.Command, args []string) error { return runProve(args[0], version) },
	}
	cmd.Flags().StringVar(&version, "version", "", "the version to prove (default: the highest recorded)")
	return cmd
}
