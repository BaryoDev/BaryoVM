// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"github.com/BaryoDev/BaryoVM/internal/release"
	"github.com/BaryoDev/BaryoVM/internal/site"
	"github.com/BaryoDev/BaryoVM/internal/ui"
	"github.com/spf13/cobra"
)

func newSiteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "site",
		Short: "Provision and deploy a static site on a VM (nginx, TLS, webroot)",
	}
	cmd.AddCommand(newSiteAddCmd(), newSiteDeployCmd())
	return cmd
}

// siteFail emits the failure envelope once and returns an error reportError will not emit again.
func siteFail(action string, data any, err error) error {
	ui.Emit(ui.Result{OK: false, Action: action, Data: data, Error: err.Error()})
	ui.Errorf("%s", err.Error())
	return &exitError{code: 1, msg: err.Error(), silent: true}
}

func newSiteAddCmd() *cobra.Command {
	var o site.Options
	var vmName string
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "add <domain>",
		Short: "Set up a static site on a VM: webroot, nginx server block, certificate",
		Long: strings.TrimSpace(`
Create the webroot and the ACME challenge directory, write an nginx server block for the domain
(HTTP to HTTPS redirect, TLS, try_files for a static export, a year-long cache on hashed assets,
no-cache on HTML, gzip scoped to the static locations), get a certificate with certbot in webroot
mode, test with nginx -t, and reload. Where SELinux is present the webroot is relabelled with
restorecon.

The order is fixed by the certificate: an HTTP-only block goes in first so certbot's challenge can
be served, then the full block once the certificate exists. Any failing step puts the previous
server block back (or removes the new file) and reloads nginx if it had been reloaded, so nginx
ends where it started, and the error names the step.

DNS is not touched: the domain has to resolve to the VM before the certificate can be issued. A
site that already exists, on the VM or in the local inventory, is refused unless --force.

Every command that needs root runs through sudo -n, so the SSH user needs passwordless sudo. Unlike the
billable provisioning commands, this one acts on a VM you already own and is undone by removing
one file, so it does not need --yes. --dry-run prints the server blocks and the commands first.
`),
		Args: cobra.ExactArgs(1),
		Example: "  baryovm site add example.com --vm web1 --root /var/www/example.com --email ops@example.com --dry-run\n" +
			"  baryovm site add example.com --vm web1 --root /var/www/example.com --email ops@example.com",
		RunE: func(cmd *cobra.Command, args []string) error {
			const action = "site add"
			o.Domain = args[0]
			// Before the inventory, before any connection.
			if err := o.Validate(); err != nil {
				return siteFail(action, nil, err)
			}
			store, err := fleet.Load()
			if err != nil {
				return siteFail(action, nil, err)
			}
			vm := store.Find(vmName)
			if vm == nil {
				return siteFail(action, nil, fmt.Errorf("no VM named %q: register it with `baryovm vm add %s --host ... --key ...`", vmName, vmName))
			}
			if err := requireLinux(vm, action); err != nil {
				return siteFail(action, nil, err)
			}
			if known := store.FindSite(o.Domain); known != nil && !o.Force {
				return siteFail(action, nil, fmt.Errorf("site %s is already recorded on VM %s with root %s: pass --force to set it up again", o.Domain, known.VM, known.Root))
			}

			if dryRun {
				p := newDryRunPlan(vm.Name)
				for _, s := range site.Plan(o) {
					p.add(s.Step, s.Command)
				}
				p.File = site.ConfPath(o.Domain)
				p.note("http block (stage 1):\n%s", site.HTTPBlock(o))
				p.note("full block (stage 2):\n%s", site.FullBlock(o, site.SSLFiles{Options: true, DHParam: true}))
				p.note("assumed for the plan: no existing server block, SELinux on; the probes decide at run time")
				emitDryRun(action, p)
				return nil
			}

			c, err := dialRelease(vm)
			if err != nil {
				return siteFail(action, nil, err)
			}
			defer c.Close()
			rep, err := site.Add(c, o, ui.Step)
			if err != nil {
				return siteFail(action, rep, err)
			}
			store.UpsertSite(fleet.Site{Domain: o.Domain, VM: vm.Name, Root: o.Root})
			if err := store.Save(); err != nil {
				return siteFail(action, rep, fmt.Errorf("the site is set up on %s but recording it locally failed, so `site deploy` will not find it: %w", vm.Name, err))
			}
			ui.Emit(ui.Result{OK: true, Action: action, Message: fmt.Sprintf("%s is set up on %s: deploy with `baryovm site deploy %s <dir>`", o.Domain, vm.Name, o.Domain), Data: rep})
			return nil
		},
	}
	cmd.Flags().StringVar(&vmName, "vm", "", "registered VM to set the site up on (required)")
	cmd.Flags().StringVar(&o.Root, "root", "", "absolute webroot on the VM, such as /var/www/example.com (required)")
	cmd.Flags().StringVar(&o.Email, "email", "", "ACME contact address (default: register without one)")
	cmd.Flags().BoolVar(&o.Force, "force", false, "replace a site that already exists")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the server blocks and commands without connecting or changing anything")
	_ = cmd.MarkFlagRequired("vm")
	_ = cmd.MarkFlagRequired("root")
	return cmd
}

// checkDeployDir refuses a local directory rsync --delete would turn into an empty webroot.
func checkDeployDir(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("deploy directory %s: %w", dir, err)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("deploy directory %s is not a directory", dir)
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return "", err
	}
	if len(entries) == 0 {
		return "", fmt.Errorf("deploy directory %s is empty: syncing it would delete the whole site, build first", dir)
	}
	return abs, nil
}

func newSiteDeployCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "deploy <domain> <dir>",
		Short: "Sync a built directory into a site's webroot and reload nginx",
		Long: strings.TrimSpace(`
Sync the contents of <dir> into the webroot the site was added with (read from the local
inventory, where 'site add' recorded it), then relabel with restorecon where SELinux is present,
run nginx -t and reload. The transfer is the same rsync, with the same host key check, that
'stack release' uses, and --delete removes files that are no longer in <dir>.

An empty or missing directory is refused, since syncing it would empty the webroot.
`),
		Args:    cobra.ExactArgs(2),
		Example: "  baryovm site deploy example.com ./out",
		RunE: func(cmd *cobra.Command, args []string) error {
			const action = "site deploy"
			domain := args[0]
			if err := site.ValidateDomain(domain); err != nil {
				return siteFail(action, nil, err)
			}
			dir, err := checkDeployDir(args[1])
			if err != nil {
				return siteFail(action, nil, err)
			}
			store, err := fleet.Load()
			if err != nil {
				return siteFail(action, nil, err)
			}
			s := store.FindSite(domain)
			if s == nil {
				return siteFail(action, nil, fmt.Errorf("no site %q in the local inventory: set it up with `baryovm site add %s --vm ... --root ...`", domain, domain))
			}
			// fleet.json is a file on disk; the root is checked again before it reaches rsync --delete.
			if err := site.ValidateRoot(s.Root); err != nil {
				return siteFail(action, nil, fmt.Errorf("site %s has a bad root in the inventory: %w", domain, err))
			}
			vm := store.Find(s.VM)
			if vm == nil {
				return siteFail(action, nil, fmt.Errorf("site %q references unknown VM %q", domain, s.VM))
			}
			if err := requireLinux(vm, action); err != nil {
				return siteFail(action, nil, err)
			}
			// "./" syncs the directory's contents to the webroot; "out" would land at root/out.
			m := &release.Manifest{LocalRoot: dir, RemoteRoot: s.Root, Sync: []string{"./"}, Sudo: true}
			transport := release.PickTransport(lookPath)

			if dryRun {
				p := newDryRunPlan(vm.Name)
				p.Transport = string(transport)
				if transport == release.TransportRsync {
					argv := m.RsyncCmd("./", vm.User, vm.Host, vm.Port, vm.KeyPath).Args
					p.Rsync = append(p.Rsync, argv)
					p.add("sync", strings.Join(argv, " "))
				} else if c, err := m.TarExtractCmd("./"); err == nil {
					p.add("sync (tar)", c)
				}
				p.add("inspect", site.ProbeSELinuxCmd())
				p.add("restorecon (only where SELinux is on)", site.RestoreconCmd(s.Root))
				p.add("nginx -t", site.TestCmd())
				p.add(site.StepReload, site.ReloadCmd())
				emitDryRun(action, p)
				return nil
			}

			if err := ui.Step("sync "+dir, func() error { return syncSub(m, vm, "./", transport) }); err != nil {
				return siteFail(action, nil, fmt.Errorf("site deploy failed at step \"sync\": %w", err))
			}
			c, err := dialRelease(vm)
			if err != nil {
				return siteFail(action, nil, err)
			}
			defer c.Close()
			steps, err := site.Finish(c, s.Root, ui.Step)
			data := map[string]any{"domain": domain, "root": s.Root, "dir": dir, "transport": string(transport), "steps": append([]string{"sync"}, steps...)}
			if err != nil {
				return siteFail(action, data, err)
			}
			ui.Emit(ui.Result{OK: true, Action: action, Message: fmt.Sprintf("%s deployed to %s on %s", domain, s.Root, vm.Name), Data: data})
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the rsync and reload commands without connecting or changing anything")
	return cmd
}
