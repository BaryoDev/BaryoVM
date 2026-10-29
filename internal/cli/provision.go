// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"context"
	"fmt"
	"net"
	"strings"

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
	yes          bool
	hosts        []string
	sshFrom      []string
	httpFrom     []string
	httpsFrom    []string
}

func (f *provisionFlags) bind(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.providerName, "provider", "lightsail", "cloud provider: lightsail (oci coming)")
	cmd.Flags().StringVar(&f.region, "region", "", "cloud region (default: from ~/.aws)")
	cmd.Flags().StringVar(&f.size, "size", "", "instance size / bundle (default: provider's smallest)")
	cmd.Flags().StringVar(&f.keyPath, "key", "", "private SSH key to authorize + connect with (required)")
	cmd.Flags().BoolVar(&f.dryRun, "dry-run", false, "print the plan without creating anything (this is already the default without --yes)")
	cmd.Flags().BoolVar(&f.yes, "yes", false, "really create the VM (billable); without it the plan is printed and nothing is created")
	cmd.Flags().StringSliceVar(&f.hosts, "host", nil, "hostname that will be served from this VM, checked against --http-from/--https-from before creating (repeatable)")
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

	hostErr := checkHostnames(ctx, spec, f.hosts, lookupHost)

	// Provisioning bills, so the plan is the default. A real create needs --yes
	// and a passing hostname check, and --dry-run wins over --yes.
	if f.dryRun || !f.yes {
		ui.Title("Provision plan (nothing is created without --yes)")
		ui.Detail("provider", f.providerName)
		ui.Detail("name", name)
		ui.Detail("region", orDefault(f.region, "from ~/.aws"))
		ui.Detail("size", orDefault(f.size, "provider default"))
		ui.Detail("key", f.keyPath)
		ui.Detail("ports", portPlan(22, spec)+", "+portPlan(80, spec)+", "+portPlan(443, spec))
		ui.Detail("cost", costNote(f.providerName, f.size))
		check := "passed"
		switch {
		case hostErr != nil:
			check = "would refuse: " + hostErr.Error()
		case len(f.hosts) == 0 && !spec.Limited(80) && !spec.Limited(443):
			check = "not needed (ports 80 and 443 are open to everyone)"
		}
		ui.Detail("hostname check", check)
		ui.Detail("to create", "rerun with --yes")
		ui.Emit(ui.Result{OK: true, Action: "vm provision", Message: "plan only: nothing created, rerun with --yes to create it", Data: map[string]any{
			"spec": spec, "cost": costNote(f.providerName, f.size), "hostnameCheck": check, "created": false,
		}})
		return fleet.VM{}, errDryRun
	}

	if hostErr != nil {
		return fleet.VM{}, fmt.Errorf("hostname check failed, nothing created: %w", hostErr)
	}

	p, err := newProviderFn(ctx, f.providerName, f.region)
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

// newProviderFn and lookupHost are seams so tests drive the commands with fakes
// and never reach a cloud API or a resolver.
var newProviderFn = newProvider

var lookupHost = func(ctx context.Context, host string) ([]string, error) {
	return net.DefaultResolver.LookupHost(ctx, host)
}

// costNote says what the plan would bill, from what the code knows.
func costNote(providerName, size string) string {
	if providerName == "lightsail" && (size == "" || size == lightsail.DefaultBundle) {
		return lightsail.DefaultBundle + ", " + lightsail.DefaultBundleCost + ", billed until the instance is deleted"
	}
	return "not known to BaryoVM: check the provider's pricing for this size, it is billed until the instance is deleted"
}

// checkHostnames is the #106 guard. Limiting 80 or 443 cuts off any hostname
// that reaches the VM directly instead of through the allowed ranges (a site
// not proxied through Cloudflare, say). Each named host must resolve, and only
// to addresses inside the ranges of every limited web port. With no limited
// web port there is nothing to cut off. A limit with no hosts named cannot be
// checked, so it fails rather than guessing.
func checkHostnames(ctx context.Context, spec provider.Spec, hosts []string, lookup func(context.Context, string) ([]string, error)) error {
	var limited []int
	for _, port := range []int{80, 443} {
		if spec.Limited(port) {
			limited = append(limited, port)
		}
	}
	if len(limited) == 0 {
		return nil
	}
	if len(hosts) == 0 {
		return fmt.Errorf("--http-from/--https-from limit the web ports, which cuts off any site that reaches the VM directly: name every hostname it will serve with --host")
	}
	var bad []string
	for _, h := range hosts {
		addrs, err := lookup(ctx, h)
		if err != nil || len(addrs) == 0 {
			bad = append(bad, h+" does not resolve")
			continue
		}
		for _, a := range addrs {
			for _, port := range limited {
				if !inRanges(a, spec.CidrsFor(port)) {
					bad = append(bad, fmt.Sprintf("%s resolves to %s, outside the ranges allowed on port %d", h, a, port))
				}
			}
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("%s", strings.Join(bad, "; "))
	}
	return nil
}

func inRanges(addr string, cidrs []string) bool {
	ip := net.ParseIP(addr)
	if ip == nil {
		return false
	}
	for _, c := range cidrs {
		if _, n, err := net.ParseCIDR(c); err == nil && n.Contains(ip) {
			return true
		}
	}
	return false
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
		Short: "Provision a new cloud VM and register it in the fleet (plan only without --yes)",
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
