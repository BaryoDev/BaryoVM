// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"fmt"
	"time"

	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"github.com/BaryoDev/BaryoVM/internal/release"
	"github.com/BaryoDev/BaryoVM/internal/ui"
	"github.com/spf13/cobra"
)

// tlsOptions is the TLS step's options for this run. The backup stamp is taken here so the engine
// stays free of the clock.
func tlsOptions(adopt bool) release.TLSOptions {
	return release.TLSOptions{Adopt: adopt, Stamp: time.Now().UTC().Format("20060102-150405")}
}

func newStackTLSCmd() *cobra.Command {
	var config string
	var adopt, dryRun bool
	cmd := &cobra.Command{
		Use:   "tls <name>",
		Short: "Write the nginx vhost and get the certificate from the release manifest's tls block",
		Long: "Runs the manifest's tls block on its own, without a release: write a port 80 vhost, issue the\n" +
			"certificate with certbot, write the real vhost, nginx -t, reload. Safe to rerun. A vhost file\n" +
			"BaryoVM did not write is left alone unless --adopt is given, and then it is backed up first.\n" +
			"If nginx -t rejects the new file the old one is restored and nginx is not reloaded.",
		Args: cobra.ExactArgs(1),
		Example: "  baryovm stack tls app --dry-run\n" +
			"  baryovm stack tls app --config ./baryovm.release.json",
		RunE: func(cmd *cobra.Command, args []string) error {
			fail := func(e error) error {
				ui.Emit(ui.Result{OK: false, Action: "stack tls", Error: e.Error()})
				return e
			}
			store, err := fleet.Load()
			if err != nil {
				return err
			}
			st := store.FindStack(args[0])
			if st == nil {
				return fmt.Errorf("no stack named %q", args[0])
			}
			vm := store.Find(st.VM)
			if vm == nil {
				return fmt.Errorf("stack %q references unknown VM %q", args[0], st.VM)
			}
			if err := requireLinux(vm, "stack tls"); err != nil {
				return err
			}
			manifestPath := config
			if manifestPath == "" {
				manifestPath = st.ReleaseFile
			}
			if manifestPath == "" {
				return fmt.Errorf("no release manifest: pass --config <file> or set --release-file on `stack add`")
			}
			m, err := release.Load(manifestPath, st.Sudo, st.ReleaseRoot)
			if err != nil {
				return err
			}
			if m.TLS == nil {
				return fail(fmt.Errorf("%s has no tls block: add one (hosts, email, optional proxy) and run again", manifestPath))
			}
			opts := tlsOptions(adopt)

			if dryRun {
				plan := m.PlanTLS(opts)
				if !ui.JSON() {
					fmt.Fprint(cmd.OutOrStdout(), m.TLSDryRun(opts))
				}
				ui.Emit(ui.Result{OK: true, Action: "stack tls", Message: args[0] + ": dry run, nothing was changed", Data: plan})
				return nil
			}

			c, err := dialRelease(vm)
			if err != nil {
				return fail(err)
			}
			defer c.Close()
			if err := m.ApplyTLS(c, opts, ui.Step); err != nil {
				return fail(err)
			}
			ui.Emit(ui.Result{OK: true, Action: "stack tls", Message: args[0] + ": tls done",
				Data: map[string]string{"vhost": m.TLS.VhostPath(), "canonical": m.TLS.Canonical}})
			return nil
		},
	}
	cmd.Flags().StringVar(&config, "config", "", "release manifest path (overrides the stack's --release-file)")
	cmd.Flags().BoolVar(&adopt, "adopt", false, "take over a vhost file BaryoVM did not write (the old file is backed up)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the generated configs and commands without touching the VM")
	return cmd
}
