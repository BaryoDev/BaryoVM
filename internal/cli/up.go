// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"fmt"

	"github.com/BaryoDev/BaryoVM/internal/bootstrap"
	"github.com/BaryoDev/BaryoVM/internal/deploy"
	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"github.com/BaryoDev/BaryoVM/internal/sshx"
	"github.com/BaryoDev/BaryoVM/internal/ui"
	"github.com/spf13/cobra"
)

// newUpCmd is the "one button": provision (if the VM is new) → install Docker →
// deploy a container. On an already-registered VM it just bootstraps + deploys.
func newUpCmd() *cobra.Command {
	var pf provisionFlags
	var image, container string
	var ports, envs []string
	var pull bool

	cmd := &cobra.Command{
		Use:   "up <name>",
		Short: "Provision (if new) → install Docker → deploy, in one command",
		Long: "If <name> is already registered, up just installs Docker and deploys.\n" +
			"If it is new, pass --provider/--key to provision it first.",
		Args: cobra.ExactArgs(1),
		Example: "  baryovm up web1 --image nginx:alpine --container site -p 80:80\n" +
			"  baryovm up web1 --provider lightsail --key ~/.ssh/id_ed25519 --image app:latest --container app -p 80:8080",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			name := args[0]

			// 1. Resolve or provision the VM.
			store, err := fleet.Load()
			if err != nil {
				return err
			}
			var vm fleet.VM
			if existing := store.Find(name); existing != nil {
				vm = *existing
			} else {
				if pf.keyPath == "" {
					return fmt.Errorf("%q is not registered: pass --provider and --key to provision it, or `vm add` it first", name)
				}
				provisioned, err := provisionVM(ctx, name, &pf)
				if err == errDryRun {
					return nil
				}
				if err != nil {
					ui.Emit(ui.Result{OK: false, Action: "up", Error: err.Error()})
					return err
				}
				vm = provisioned
			}

			// 2. Install Docker.
			var boot bootstrap.Report
			if err := ui.Step(fmt.Sprintf("installing Docker on %s", vm.Name), func() error {
				c, err := sshx.Dial(vm.Target())
				if err != nil {
					return err
				}
				defer c.Close()
				boot, err = bootstrap.EnsureDocker(c)
				return err
			}); err != nil {
				ui.Emit(ui.Result{OK: false, Action: "up", Error: err.Error()})
				return err
			}

			// 3. Deploy the container.
			env, err := parseEnv(envs)
			if err != nil {
				return err
			}
			var rep deploy.Report
			if err := ui.Step(fmt.Sprintf("deploying %s", image), func() error {
				c, err := sshx.Dial(vm.Target())
				if err != nil {
					return err
				}
				defer c.Close()
				rep, err = deploy.Run(c, deploy.Spec{Name: container, Image: image, Ports: ports, Env: env, Pull: pull})
				return err
			}); err != nil {
				ui.Emit(ui.Result{OK: false, Action: "up", Error: err.Error()})
				return err
			}

			ui.Emit(ui.Result{
				OK:      true,
				Action:  "up",
				Message: fmt.Sprintf("%s live on %s (%s)", container, vm.Name, vm.Host),
				Data: map[string]any{
					"vm":     vm,
					"docker": boot,
					"deploy": rep,
				},
			})
			return nil
		},
	}
	pf.bind(cmd)
	cmd.Flags().StringVar(&image, "image", "", "container image (required)")
	cmd.Flags().StringVar(&container, "container", "", "container name (required)")
	cmd.Flags().StringArrayVarP(&ports, "publish", "p", nil, "port mapping host:container (repeatable)")
	cmd.Flags().StringArrayVarP(&envs, "env", "e", nil, "environment KEY=value (repeatable)")
	cmd.Flags().BoolVar(&pull, "pull", false, "pull the image before running")
	_ = cmd.MarkFlagRequired("image")
	_ = cmd.MarkFlagRequired("container")
	return cmd
}
