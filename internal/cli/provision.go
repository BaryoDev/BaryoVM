// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"context"
	"fmt"

	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"github.com/BaryoDev/BaryoVM/internal/provider"
	"github.com/BaryoDev/BaryoVM/internal/provider/lightsail"
	"github.com/BaryoDev/BaryoVM/internal/sshx"
	"github.com/BaryoDev/BaryoVM/internal/ui"
	"github.com/spf13/cobra"
)

// provisionFlags are shared by `vm provision` and `up`.
type provisionFlags struct {
	providerName string
	region       string
	size         string
	keyPath      string
	dryRun       bool
}

func (f *provisionFlags) bind(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.providerName, "provider", "lightsail", "cloud provider: lightsail (oci coming)")
	cmd.Flags().StringVar(&f.region, "region", "", "cloud region (default: from ~/.aws)")
	cmd.Flags().StringVar(&f.size, "size", "", "instance size / bundle (default: provider's smallest)")
	cmd.Flags().StringVar(&f.keyPath, "key", "", "private SSH key to authorize + connect with (required)")
	cmd.Flags().BoolVar(&f.dryRun, "dry-run", false, "print the plan without creating anything")
}

// provisionVM creates a cloud VM per flags and registers it in the fleet.
func provisionVM(ctx context.Context, name string, f *provisionFlags) (fleet.VM, error) {
	if f.keyPath == "" {
		return fleet.VM{}, fmt.Errorf("--key is required (the SSH key to authorize and connect with)")
	}
	pub, err := sshx.PublicKeyFromPrivate(f.keyPath)
	if err != nil {
		return fleet.VM{}, err
	}
	spec := provider.Spec{Name: name, Region: f.region, Size: f.size, PublicKey: pub}

	if f.dryRun {
		ui.Title("Provision plan (dry run)")
		ui.Detail("provider", f.providerName)
		ui.Detail("name", name)
		ui.Detail("region", orDefault(f.region, "from ~/.aws"))
		ui.Detail("size", orDefault(f.size, "provider default"))
		ui.Detail("key", f.keyPath)
		ui.Detail("ports", "22, 80, 443")
		ui.Emit(ui.Result{OK: true, Action: "vm provision", Message: "dry run: nothing created", Data: spec})
		return fleet.VM{}, errDryRun
	}

	p, err := newProvider(ctx, f.providerName, f.region)
	if err != nil {
		return fleet.VM{}, err
	}

	var inst provider.Instance
	err = ui.Step(fmt.Sprintf("provisioning %s on %s", name, f.providerName), func() error {
		inst, err = p.Provision(ctx, spec)
		return err
	})
	if err != nil {
		return fleet.VM{}, err
	}

	vm := fleet.VM{Name: name, Host: inst.PublicIP, User: inst.User, KeyPath: f.keyPath, Provider: inst.Provider, ID: inst.ID}
	store, err := fleet.Load()
	if err != nil {
		return fleet.VM{}, err
	}
	store.Upsert(vm)
	if err := store.Save(); err != nil {
		return fleet.VM{}, err
	}
	return vm, nil
}

func newProvider(ctx context.Context, name, region string) (provider.Provider, error) {
	switch name {
	case "lightsail":
		return lightsail.New(ctx, region)
	case "oci":
		return nil, fmt.Errorf("the oci provider is not implemented yet")
	default:
		return nil, fmt.Errorf("unknown provider %q (have: lightsail)", name)
	}
}

func newVMProvisionCmd() *cobra.Command {
	var f provisionFlags
	cmd := &cobra.Command{
		Use:   "provision <name>",
		Short: "Provision a new cloud VM and register it in the fleet",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vm, err := provisionVM(cmd.Context(), args[0], &f)
			if err == errDryRun {
				return nil
			}
			if err != nil {
				ui.Emit(ui.Result{OK: false, Action: "vm provision", Error: err.Error()})
				return err
			}
			ui.Emit(ui.Result{OK: true, Action: "vm provision", Message: fmt.Sprintf("%s ready at %s", vm.Name, vm.Host), Data: vm})
			return nil
		},
	}
	f.bind(cmd)
	return cmd
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
