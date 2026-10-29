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
	sshFrom      []string
	httpFrom     []string
	httpsFrom    []string
}

func (f *provisionFlags) bind(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.providerName, "provider", "lightsail", "cloud provider: lightsail (oci coming)")
	cmd.Flags().StringVar(&f.region, "region", "", "cloud region (default: from ~/.aws)")
	cmd.Flags().StringVar(&f.size, "size", "", "instance size / bundle (default: provider's smallest)")
	cmd.Flags().StringVar(&f.keyPath, "key", "", "private SSH key to authorize + connect with (required)")
	cmd.Flags().BoolVar(&f.dryRun, "dry-run", false, "print the plan without creating anything")
	cmd.Flags().StringSliceVar(&f.sshFrom, "ssh-from", nil, "limit port 22 to these CIDRs (default: open to 0.0.0.0/0)")
	cmd.Flags().StringSliceVar(&f.httpFrom, "http-from", nil, "limit port 80 to these CIDRs or the cloudflare preset (default: open)")
	cmd.Flags().StringSliceVar(&f.httpsFrom, "https-from", nil, "limit port 443 to these CIDRs or the cloudflare preset (default: open)")
}

// sourcesFor resolves one flag's list; the cloudflare preset is fetched here,
// before anything is created, so a failed fetch costs nothing.
func sourcesFor(ctx context.Context, flag string, in []string) ([]string, error) {
	out, err := provider.ResolveSources(ctx, provider.HTTPFetch, in)
	if err != nil {
		return nil, fmt.Errorf("--%s: %w", flag, err)
	}
	return out, nil
}

// portPlan is the dry-run line for one port.
func portPlan(port int, spec provider.Spec) string {
	if !spec.Limited(port) {
		return fmt.Sprintf("%d from %s", port, provider.AnyIPv4)
	}
	v4, v6 := provider.SplitFamilies(spec.CidrsFor(port))
	return fmt.Sprintf("%d from %d IPv4 + %d IPv6 range(s)", port, len(v4), len(v6))
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
	if spec.SSHFrom, err = sourcesFor(ctx, "ssh-from", f.sshFrom); err != nil {
		return fleet.VM{}, err
	}
	if spec.HTTPFrom, err = sourcesFor(ctx, "http-from", f.httpFrom); err != nil {
		return fleet.VM{}, err
	}
	if spec.HTTPSFrom, err = sourcesFor(ctx, "https-from", f.httpsFrom); err != nil {
		return fleet.VM{}, err
	}

	if err := spec.CheckFamilies(); err != nil {
		return fleet.VM{}, err
	}

	if f.dryRun {
		ui.Title("Provision plan (dry run)")
		ui.Detail("provider", f.providerName)
		ui.Detail("name", name)
		ui.Detail("region", orDefault(f.region, "from ~/.aws"))
		ui.Detail("size", orDefault(f.size, "provider default"))
		ui.Detail("key", f.keyPath)
		ui.Detail("ports", portPlan(22, spec)+", "+portPlan(80, spec)+", "+portPlan(443, spec))
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
