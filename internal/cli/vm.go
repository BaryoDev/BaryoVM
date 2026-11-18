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
	"github.com/BaryoDev/BaryoVM/internal/ctr"
	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"github.com/BaryoDev/BaryoVM/internal/hostkeys"
	"github.com/BaryoDev/BaryoVM/internal/sshx"
	"github.com/BaryoDev/BaryoVM/internal/ui"
	"github.com/spf13/cobra"
)

func newVMCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "vm", Short: "Manage the VMs in your fleet"}
	cmd.AddCommand(newVMAddCmd(), newVMListCmd(), newVMPingCmd(), newVMExecCmd(), newVMBootstrapCmd(), newVMRunnerCmd(), newVMRemoveCmd(), newVMProvisionCmd(), newVMHardenCmd(), newVMThreatsCmd(), newVMForgetKeyCmd(), newVMPatchCmd(), newVMPruneCmd(), newVMHealthCmd(), newVMRemoteCmd())
	return cmd
}

func newVMAddCmd() *cobra.Command {
	var host, user, key, osName, shellName, remoteMode, hostKeyFP string
	var port int
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Register an existing VM by host + SSH key",
		Long: strings.TrimSpace(`
Register an existing VM and record whether it runs Linux or Windows. The OS
is detected once, over SSH, when the VM is added; pass --os to skip the
connection. If the host cannot be reached it is saved with no OS (read as
linux) and a warning; a host key mismatch still fails. With --os windows the SSH
shell is recorded as cmd, the OpenSSH default; pass --shell powershell if the
host's default shell is powershell.exe.

An unknown host key is asked about on a terminal and refused with no terminal or
under -o json. A script or app that has had a person check the key can pass its
SHA256 fingerprint with --host-key-fingerprint: a host offering exactly that key
is recorded and the command continues, and any other key is refused, also for a
host that already has a recorded key. This holds under --strict-host-keys too,
since the flag is the checked key. It cannot be combined with --os or --remote
forced, which make no connection. A host with a different recorded key is still
refused.
`),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			store, err := fleet.Load()
			if err != nil {
				return err
			}
			if hostKeyFP != "" {
				if !strings.HasPrefix(hostKeyFP, "SHA256:") {
					return fmt.Errorf("--host-key-fingerprint must look like SHA256:..., as ssh-keygen -lf prints it")
				}
				// With a manual OS or a forced key no connection is made, so the fingerprint would never
				// be checked. Say so before anything is saved.
				if osName != "" || remoteMode == fleet.RemoteForced {
					return fmt.Errorf("--host-key-fingerprint needs a connection to check the key, but --os and --remote %s skip it; drop one of them", fleet.RemoteForced)
				}
				defer sshx.TrustFingerprint(hostKeyFP)()
			}
			vm := fleet.VM{Name: name, Host: host, Port: port, User: user, KeyPath: key, Provider: "ssh"}
			var unreached bool
			var detectErr error
			switch remoteMode {
			case "":
			case fleet.RemoteForced:
				// A forced key discards the commands OS detection would send, so there is nothing to
				// detect: the VM is Linux, the only OS the wrapper runs on.
				vm.Remote = fleet.RemoteForced
				if osName == "" {
					osName = "linux"
				}
			default:
				return fmt.Errorf("--remote must be %q, got %q", fleet.RemoteForced, remoteMode)
			}
			manualOS, manualShell, err := resolveManualHost(osName, shellName)
			if err != nil {
				return err
			}
			if manualOS != "" {
				vm.OS, vm.Shell = manualOS, manualShell
			} else {
				err = ui.Step(fmt.Sprintf("detecting the OS of %s", host), func() error {
					c, err := sshx.Dial(vm.Target())
					if err != nil {
						if sshx.IsUnreachable(err) {
							unreached = true
							return nil
						}
						return err
					}
					defer c.Close()
					if vm.OS, err = sshx.DetectOS(c); err != nil {
						vm.OS, detectErr = "", err
						return nil
					}
					if vm.OS != sshx.Windows {
						return nil
					}
					vm.Shell, err = sshx.DetectShell(c)
					return err
				})
				if err != nil {
					ui.Emit(ui.Result{OK: false, Action: "vm add", Error: err.Error(), Data: errData(err)})
					return err
				}
			}
			store.Upsert(vm)
			if err := store.Save(); err != nil {
				return err
			}
			msg := fmt.Sprintf("registered %s (%s@%s, %s)", name, user, host, vm.HostOS())
			if unreached || detectErr != nil {
				note := fmt.Sprintf("could not reach %s to detect its OS, recorded it as linux. Run vm add again when it is up, or pass --os windows", host)
				if detectErr != nil {
					note = fmt.Sprintf("connected to %s but could not detect its OS (%v), recorded it as linux. Run vm add again or pass --os windows", host, detectErr)
				}
				if ui.JSON() {
					msg += ". " + note
				} else {
					ui.Warnf("%s", note)
				}
			}
			ui.Emit(ui.Result{OK: true, Action: "vm add", Message: msg, Data: vm})
			return nil
		},
	}
	cmd.Flags().StringVar(&host, "host", "", "public IP or hostname (required)")
	cmd.Flags().StringVar(&user, "user", "ubuntu", "SSH user")
	cmd.Flags().StringVar(&key, "key", "", "path to the private SSH key (required)")
	cmd.Flags().IntVar(&port, "port", 22, "SSH port")
	cmd.Flags().StringVar(&osName, "os", "", "host OS, linux or windows (default: detect over SSH)")
	cmd.Flags().StringVar(&shellName, "shell", "", "SSH shell of a Windows host, cmd or powershell (with --os windows; default cmd)")
	cmd.Flags().StringVar(&remoteMode, "remote", "", "\"forced\" when the key is pinned to `baryovm remote serve` (see `vm remote install`)")
	cmd.Flags().StringVar(&hostKeyFP, "host-key-fingerprint", "", "trust an unknown host that offers exactly this SHA256 fingerprint, without asking")
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
			// An entry written before the OS was recorded reads as linux, here as everywhere.
			vms := make([]fleet.VM, len(store.VMs))
			for i, vm := range store.VMs {
				vm.OS = vm.HostOS()
				vms[i] = vm
			}
			if ui.JSON() {
				ui.Emit(ui.Result{OK: true, Action: "vm list", Data: vms})
				return nil
			}
			if len(store.VMs) == 0 {
				ui.Warnf("no VMs registered yet: add one with `baryovm vm add`")
				return nil
			}
			ui.Title(fmt.Sprintf("Fleet (%d)", len(store.VMs)))
			for _, vm := range vms {
				fmt.Printf("%s  %s\n", ui.AccentStyle.Render(vm.Name), ui.DimStyle.Render(fmt.Sprintf("%s@%s [%s, %s]", vm.User, vm.Host, vm.Provider, vm.OS)))
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
			if err := requireLinux(&vm, "vm ping"); err != nil {
				return err
			}
			var uname, docker string
			err = ui.Step(fmt.Sprintf("connecting to %s@%s", vm.User, vm.Host), func() error {
				c, err := dialChecked(&vm)
				if err != nil {
					return err
				}
				defer c.Close()
				uname, _ = c.Run("uname -sr")
				docker, _ = c.Run("docker --version 2>/dev/null || echo 'not installed'")
				return nil
			})
			if err != nil {
				ui.Emit(ui.Result{OK: false, Action: "vm ping", Error: err.Error(), Data: errData(err)})
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
	var dryRun bool
	var runtimeName string
	cmd := &cobra.Command{
		Use:   "bootstrap <name>",
		Short: "Install the container runtime (docker or podman) on a VM (idempotent)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := ctr.Valid(runtimeName); err != nil {
				return err
			}
			if dryRun {
				return dryVMBootstrap(args[0], runtimeName)
			}
			vm, err := requireVM(args[0])
			if err != nil {
				return err
			}
			if err := requireLinux(&vm, "vm bootstrap"); err != nil {
				return err
			}
			var rep bootstrap.Report
			err = ui.Step(fmt.Sprintf("installing %s on %s", ctr.Bin(runtimeName), vm.Name), func() error {
				c, err := dialChecked(&vm)
				if err != nil {
					return err
				}
				defer c.Close()
				rep, err = bootstrap.EnsureRuntime(c, runtimeName)
				return err
			})
			if err != nil {
				ui.Emit(ui.Result{OK: false, Action: "vm bootstrap", Error: err.Error(), Data: errData(err)})
				return err
			}
			msg := vm.Name + " ready: " + rep.DockerVersion
			if rep.AlreadyReady {
				msg = vm.Name + " already had " + ctr.Bin(runtimeName) + ": " + rep.DockerVersion
			}
			if rep.NoSystemd {
				msg += " (no systemd on this host: the service step was skipped, start it with the host's own init)"
			}
			ui.Emit(ui.Result{OK: true, Action: "vm bootstrap", Message: msg, Data: rep})
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the install script without connecting")
	cmd.Flags().StringVar(&runtimeName, "runtime", "", "runtime to install: docker (default) or podman")
	return cmd
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

// newVMForgetKeyCmd drops a recorded host key so the next connection treats the new one as unknown.
//
// This exists because refusing a changed key is only useful if there is a stated way through it. A
// rebuilt VM presents a new key legitimately, and without this command the operator's options are
// to edit known_hosts by hand or to find a flag that turns verification off, and the second is what
// people actually do. It takes one host at a time on purpose: there is no "accept everything".
func newVMForgetKeyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "forget-key <name-or-host>",
		Short: "Forget a VM's recorded SSH host key, so the next connection treats it as unknown",
		Long: strings.TrimSpace(`
Forget the host key BaryoVM recorded for a VM, for when the VM was rebuilt and now has a new one.

This clears only BaryoVM's own store. If ~/.ssh/known_hosts still holds the old key, the rebuilt
host is refused again as a changed key; remove that line with ssh-keygen -R too.

After that the host is unknown, and the next connection depends on the mode: on a terminal it asks
whether to trust the new key, with no terminal, under -o json or with --strict-host-keys it is
refused, and with --strict-host-keys=false (or BARYOVM_STRICT_HOST_KEYS=0) it learns the new key
without asking. Check the new fingerprint against the cloud console or the machine itself first.
`),
		Args: cobra.ExactArgs(1),
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
				Message: fmt.Sprintf("forgot %d host key(s) for %s, the next connection treats the new one as unknown (ask, refuse or learn, by mode)", n, target),
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
