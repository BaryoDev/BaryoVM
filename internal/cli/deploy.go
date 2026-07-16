// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"fmt"
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/deploy"
	"github.com/BaryoDev/BaryoVM/internal/sshx"
	"github.com/BaryoDev/BaryoVM/internal/ui"
	"github.com/spf13/cobra"
)

func newDeployCmd() *cobra.Command {
	var vmName, image, name string
	var ports, envs []string
	var pull bool
	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Deploy a container image to a VM",
		Example: "  baryovm deploy --vm web1 --image nginx:alpine --name site -p 80:80\n" +
			"  baryovm deploy --vm web1 --image app:latest --name app -p 127.0.0.1:8080:8080 -e KEY=val --pull",
		RunE: func(cmd *cobra.Command, args []string) error {
			vm, err := requireVM(vmName)
			if err != nil {
				return err
			}
			env, err := parseEnv(envs)
			if err != nil {
				return err
			}
			spec := deploy.Spec{Name: name, Image: image, Ports: ports, Env: env, Pull: pull}

			var rep deploy.Report
			err = ui.Step(fmt.Sprintf("deploying %s to %s", image, vm.Name), func() error {
				c, err := sshx.Dial(vm.Target())
				if err != nil {
					return err
				}
				defer c.Close()
				rep, err = deploy.Run(c, spec)
				return err
			})
			if err != nil {
				ui.Emit(ui.Result{OK: false, Action: "deploy", Error: err.Error()})
				return err
			}
			ui.Emit(ui.Result{
				OK:      true,
				Action:  "deploy",
				Message: fmt.Sprintf("%s running on %s", name, vm.Name),
				Data:    rep,
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&vmName, "vm", "", "target VM name (required)")
	cmd.Flags().StringVar(&image, "image", "", "container image (required)")
	cmd.Flags().StringVar(&name, "name", "", "container name (required)")
	cmd.Flags().StringArrayVarP(&ports, "publish", "p", nil, "port mapping host:container (repeatable)")
	cmd.Flags().StringArrayVarP(&envs, "env", "e", nil, "environment KEY=value (repeatable)")
	cmd.Flags().BoolVar(&pull, "pull", false, "pull the image before running")
	_ = cmd.MarkFlagRequired("vm")
	_ = cmd.MarkFlagRequired("image")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func parseEnv(pairs []string) (map[string]string, error) {
	env := make(map[string]string, len(pairs))
	for _, p := range pairs {
		k, v, ok := strings.Cut(p, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("invalid --env %q, expected KEY=value", p)
		}
		env[k] = v
	}
	return env, nil
}
