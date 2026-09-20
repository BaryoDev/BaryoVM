// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/bootstrap"
	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"github.com/BaryoDev/BaryoVM/internal/hostkeys"
	"github.com/BaryoDev/BaryoVM/internal/sshx"
	"github.com/BaryoDev/BaryoVM/internal/ui"
	"github.com/spf13/cobra"
)

func newVMCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "vm", Short: "Manage the VMs in your fleet"}
	cmd.AddCommand(newVMAddCmd(), newVMListCmd(), newVMPingCmd(), newVMExecCmd(), newVMBootstrapCmd(), newVMRemoveCmd(), newVMProvisionCmd(), newVMHardenCmd(), newVMThreatsCmd(), newVMForgetKeyCmd())
	return cmd
}

func newVMAddCmd() *cobra.Command {
	var host, user, key string
	var port int
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Register an existing VM by host + SSH key",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			store, err := fleet.Load()
			if err != nil {
				return err
			}
			vm := fleet.VM{Name: name, Host: host, Port: port, User: user, KeyPath: key, Provider: "ssh"}
			store.Upsert(vm)
			if err := store.Save(); err != nil {
				return err
			}
			ui.Emit(ui.Result{OK: true, Action: "vm add", Message: fmt.Sprintf("registered %s (%s@%s)", name, user, host), Data: vm})
			return nil
		},
	}
	cmd.Flags().StringVar(&host, "host", "", "public IP or hostname (required)")
	cmd.Flags().StringVar(&user, "user", "ubuntu", "SSH user")
	cmd.Flags().StringVar(&key, "key", "", "path to the private SSH key (required)")
	cmd.Flags().IntVar(&port, "port", 22, "SSH port")
	_ = cmd.MarkFlagRequired("host")
	_ = cmd.MarkFlagRequired("key")
	return cmd
}

func newVMListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List registered VMs",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := fleet.Load()
			if err != nil {
				return err
			}
			if ui.JSON() {
				ui.Emit(ui.Result{OK: true, Action: "vm list", Data: store.VMs})
				return nil
			}
			if len(store.VMs) == 0 {
				ui.Warnf("no VMs registered yet: add one with `baryovm vm add`")
				return nil
			}
			ui.Title(fmt.Sprintf("Fleet (%d)", len(store.VMs)))
			for _, vm := range store.VMs {
				fmt.Printf("%s  %s\n", ui.AccentStyle.Render(vm.Name), ui.DimStyle.Render(fmt.Sprintf("%s@%s [%s]", vm.User, vm.Host, vm.Provider)))
			}
			return nil
		},
	}
}

func newVMRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove a VM from the fleet (does not destroy the machine)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := fleet.Load()
			if err != nil {
				return err
			}
			if !store.Remove(args[0]) {
				return fmt.Errorf("no VM named %q", args[0])
			}
			if err := store.Save(); err != nil {
				return err
			}
			ui.Emit(ui.Result{OK: true, Action: "vm remove", Message: "removed " + args[0]})
			return nil
		},
	}
}

func newVMPingCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ping <name>",
		Short: "SSH in and report host + Docker status",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vm, err := requireVM(args[0])
			if err != nil {
				return err
			}
			var uname, docker string
			err = ui.Step(fmt.Sprintf("connecting to %s@%s", vm.User, vm.Host), func() error {
				c, err := sshx.Dial(vm.Target())
				if err != nil {
					return err
				}
				defer c.Close()
				uname, _ = c.Run("uname -sr")
				docker, _ = c.Run("docker --version 2>/dev/null || echo 'not installed'")
				return nil
			})
			if err != nil {
				ui.Emit(ui.Result{OK: false, Action: "vm ping", Error: err.Error()})
				return err
			}
			data := map[string]string{"host": strings.TrimSpace(uname), "docker": strings.TrimSpace(docker)}
			if !ui.JSON() {
				ui.Detail("host", data["host"])
				ui.Detail("docker", data["docker"])
			}
			ui.Emit(ui.Result{OK: true, Action: "vm ping", Message: vm.Name + " reachable", Data: data})
			return nil
		},
	}
}

func newVMBootstrapCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "bootstrap <name>",
		Short: "Install Docker on a VM (idempotent)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vm, err := requireVM(args[0])
			if err != nil {
				return err
			}
			var rep bootstrap.Report
			err = ui.Step(fmt.Sprintf("installing Docker on %s", vm.Name), func() error {
				c, err := sshx.Dial(vm.Target())
				if err != nil {
					return err
				}
				defer c.Close()
				rep, err = bootstrap.EnsureDocker(c)
				return err
			})
			if err != nil {
				ui.Emit(ui.Result{OK: false, Action: "vm bootstrap", Error: err.Error()})
				return err
			}
			msg := vm.Name + " ready: " + rep.DockerVersion
			if rep.AlreadyReady {
				msg = vm.Name + " already had Docker: " + rep.DockerVersion
			}
			ui.Emit(ui.Result{OK: true, Action: "vm bootstrap", Message: msg, Data: rep})
			return nil
		},
	}
}

// requireVM loads a registered VM by name or returns a helpful error.
func requireVM(name string) (fleet.VM, error) {
	store, err := fleet.Load()
	if err != nil {
		return fleet.VM{}, err
	}
	vm := store.Find(name)
	if vm == nil {
		return fleet.VM{}, fmt.Errorf("no VM named %q: register it with `baryovm vm add %s --host ... --key ...`", name, name)
	}
	return *vm, nil
}

// newVMForgetKeyCmd drops a recorded host key so the next connection learns the new one.
//
// This exists because refusing a changed key is only useful if there is a stated way through it. A
// rebuilt VM presents a new key legitimately, and without this command the operator's options are
// to edit known_hosts by hand or to find a flag that turns verification off, and the second is what
// people actually do. It takes one host at a time on purpose: there is no "accept everything".
func newVMForgetKeyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "forget-key <name-or-host>",
		Short: "Forget a VM's recorded SSH host key, so the next connection learns it again",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := args[0]

			// A fleet name is the convenient thing to type; the key is stored under host:port.
			if store, err := fleet.Load(); err == nil {
				if vm := store.Find(target); vm != nil {
					t := vm.Target()
					port := t.Port
					if port == 0 {
						port = 22
					}
					target = net.JoinHostPort(t.Host, strconv.Itoa(port))
				}
			}

			hk := hostkeys.New(baryovmHome())
			n, err := hk.Forget(target)
			if err != nil {
				return err
			}
			if n == 0 {
				return fmt.Errorf("no recorded host key for %s in %s", target, hk.Path())
			}
			ui.Emit(ui.Result{
				OK:      true,
				Action:  "vm forget-key",
				Message: fmt.Sprintf("forgot %d host key(s) for %s, the next connection will learn the new one", n, target),
			})
			return nil
		},
	}
}

// baryovmHome mirrors fleet's rule: BARYOVM_HOME if set, else ~/.baryovm.
func baryovmHome() string {
	if d := os.Getenv("BARYOVM_HOME"); d != "" {
		return d
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".baryovm")
}
