// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"github.com/BaryoDev/BaryoVM/internal/hostkeys"
	"github.com/BaryoDev/BaryoVM/internal/sshx"
	"github.com/BaryoDev/BaryoVM/internal/ui"
	"github.com/spf13/cobra"
)

func newProjectCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "project", Short: "Release from a committed project file, or write one from your fleet"}
	cmd.AddCommand(newProjectReleaseCmd(), newProjectExportCmd())
	return cmd
}

// projectStackResult is one stack's line in the project report. Data is whatever `stack release`
// reported for it, so a partial release shows how far each stack got.
type projectStackResult struct {
	Name   string `json:"name"`
	Status string `json:"status"` // released | failed | skipped
	Error  string `json:"error,omitempty"`
	Data   any    `json:"data,omitempty"`
}

func addrOf(vm *fleet.VM) string {
	port := vm.Port
	if port <= 0 {
		port = 22
	}
	return net.JoinHostPort(vm.Host, strconv.Itoa(port))
}

func newProjectReleaseCmd() *cobra.Command {
	var file string
	var noBuild, noBackup, adopt bool
	cmd := &cobra.Command{
		Use:   "release",
		Short: "Release every stack of a project file, in order, with no vm add or stack add",
		Long: "Reads the project file and nothing else: not fleet.json, so a person and a CI job release the same\n" +
			"thing. Stacks are released in the file's order, and the first failed release or verify stops the\n" +
			"rest. Every VM needs a pinned hostKey in the file, or --strict-host-keys with its key already\n" +
			"known: a project release never trusts a host on first use.",
		Example: "  baryovm project release\n" +
			"  baryovm project release --file deploy/baryovm.project.json --no-backup",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			const action = "project release"
			abs, err := filepath.Abs(file)
			if err != nil {
				return err
			}
			p, err := fleet.LoadProject(file)
			if err != nil {
				ui.Emit(ui.Result{OK: false, Action: action, Error: err.Error()})
				return err
			}
			order := p.ReleaseOrder()
			results := make([]projectStackResult, 0, len(order))
			fail := func(e error) error {
				ui.Emit(ui.Result{OK: false, Action: action, Error: e.Error(),
					Data: map[string]any{"project": p.Project, "stacks": results}})
				return e
			}
			for _, n := range order {
				if err := validStackName(n); err != nil {
					return fail(err)
				}
				results = append(results, projectStackResult{Name: n, Status: "skipped"})
			}
			store, err := p.Store(filepath.Dir(abs), os.Getenv)
			if err != nil {
				return fail(err)
			}

			// Host identity is settled before anything connects. Trust on first use has no first use
			// on a runner that remembers nothing, so a VM either carries the key it is expected to
			// present or the run is explicitly strict.
			used := map[string]bool{}
			for _, n := range order {
				used[store.FindStack(n).VM] = true
			}
			// Pins live for this run only: a committed file must not change what this machine trusts.
			hk := hostkeys.New(baryovmHome())
			hostkeys.ResetSessionPins()
			defer hostkeys.ResetSessionPins()
			for i := range store.VMs {
				vm := &store.VMs[i]
				if !used[vm.Name] {
					continue
				}
				if vm.HostKey == "" {
					if !sshx.StrictHostKeys() {
						return fail(fmt.Errorf("vms[%d].hostKey: vm %q has no pinned host key, and a project release never trusts a host on first use: add its public key to the file (`ssh-keyscan -t ed25519 %s`, check the fingerprint) or pass --strict-host-keys with the key already in known_hosts", i, vm.Name, vm.Host))
					}
					continue
				}
				if err := hk.PinForRun(addrOf(vm), vm.HostKey); err != nil {
					return fail(fmt.Errorf("vms[%d].hostKey: %w", i, err))
				}
			}

			opts := stackReleaseOpts{NoBuild: noBuild, NoBackup: noBackup, Adopt: adopt}
			for i, n := range order {
				ui.Notef("%s: releasing (%d of %d)", n, i+1, len(order))
				var res ui.Result
				rerr := releaseStack(store, n, opts, &res)
				results[i].Data = res.Data
				if rerr != nil {
					results[i].Status, results[i].Error = "failed", rerr.Error()
					return fail(fmt.Errorf("%s: %w: later stacks were not touched", n, rerr))
				}
				results[i].Status = "released"
			}
			ui.Emit(ui.Result{OK: true, Action: action, Message: fmt.Sprintf("%s: %d stack(s) released", p.Project, len(order)),
				Data: map[string]any{"project": p.Project, "stacks": results}})
			return nil
		},
	}
	cmd.Flags().StringVar(&file, "file", fleet.DefaultProjectFile, "project file to release from")
	cmd.Flags().BoolVar(&noBuild, "no-build", false, "skip building images (just sync + compose up)")
	cmd.Flags().BoolVar(&noBackup, "no-backup", false, "skip the automatic pre-release DB backup")
	cmd.Flags().BoolVar(&adopt, "adopt", false, "take over what BaryoVM did not create: see `stack release --adopt`")
	return cmd
}

func newProjectExportCmd() *cobra.Command {
	var out, name, keyEnv string
	var stacks []string
	var force bool
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Write a project file from the stacks registered on this machine",
		Long: "Turns a hand-registered fleet into a file that can be committed. Release manifest paths under the\n" +
			"output directory become relative to it. The key path becomes an env:NAME reference, and a host key\n" +
			"already in known_hosts is pinned. Whatever cannot be made shareable is refused with the field named.",
		Example: "  baryovm project export --out deploy/baryovm.project.json --stack api --stack console",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			const action = "project export"
			store, err := fleet.Load()
			if err != nil {
				return err
			}
			abs, err := filepath.Abs(out)
			if err != nil {
				return err
			}
			if _, err := os.Stat(abs); err == nil && !force {
				e := fmt.Errorf("%s already exists: pass --force to replace it", out)
				ui.Emit(ui.Result{OK: false, Action: action, Error: e.Error()})
				return e
			}
			if name == "" {
				name = filepath.Base(filepath.Dir(abs))
			}
			known := func(addr string) string {
				lines, err := hostkeys.New(baryovmHome()).KnownHostsFor(addr)
				if err != nil {
					return ""
				}
				return authorizedFromKnownHosts(lines)
			}
			p, b, err := exportProject(store, filepath.Dir(abs), name, keyEnv, stacks, known)
			if err != nil {
				ui.Emit(ui.Result{OK: false, Action: action, Error: err.Error()})
				return err
			}
			if err := os.WriteFile(abs, b, 0o644); err != nil { // #nosec G306 -- meant to be committed
				return err
			}
			var unpinned []string
			for _, v := range p.VMs {
				if v.HostKey == "" {
					unpinned = append(unpinned, v.Name)
					ui.Warnf("vm %s has no host key in known_hosts, so it is not pinned: add hostKey before `project release`, or release with --strict-host-keys", v.Name)
				}
			}
			ui.Emit(ui.Result{OK: true, Action: action, Message: "wrote " + out,
				Data: map[string]any{"file": abs, "project": p.Project, "order": p.Order, "unpinned": unpinned}})
			return nil
		},
	}
	cmd.Flags().StringVar(&out, "out", fleet.DefaultProjectFile, "file to write")
	cmd.Flags().StringVar(&name, "name", "", "project name (default: the output directory's name)")
	cmd.Flags().StringVar(&keyEnv, "key-env", "", "environment variable that holds the key path in CI (default DEPLOY_KEY_PATH)")
	cmd.Flags().StringSliceVar(&stacks, "stack", nil, "stack to include, in release order (repeatable; default: all)")
	cmd.Flags().BoolVar(&force, "force", false, "replace the output file if it exists")
	return cmd
}

var envNameRe = regexp.MustCompile(`[^A-Za-z0-9_]`)

// exportProject builds a shareable project from store and returns it with the bytes to write. outDir
// is the directory the file will sit in: paths are made relative to it. known gives the public host
// key recorded for host:port, or "". The bytes are read back through ParseProject, so what is
// written is what `project release` will accept, and a field that cannot be made shareable fails
// here with its name instead of at release.
func exportProject(store *fleet.Store, outDir, name, keyEnv string, only []string, known func(addr string) string) (*fleet.Project, []byte, error) {
	if keyEnv != "" && !fleet.IsEnvRef("env:"+keyEnv) {
		return nil, nil, fmt.Errorf("--key-env %q is not a valid environment variable name", keyEnv)
	}
	selected := store.Stacks
	if len(only) > 0 {
		selected = nil
		for _, n := range only {
			st := store.FindStack(n)
			if st == nil {
				return nil, nil, fmt.Errorf("--stack %q: no such stack in the fleet", n)
			}
			selected = append(selected, *st)
		}
	}
	p := &fleet.Project{Project: name}
	needed := map[string]bool{}
	for i, st := range selected {
		if st.ReleaseFile == "" {
			return nil, nil, fmt.Errorf("stacks[%d].releaseFile: stack %q has no release manifest, so a project file cannot release it: set one with `stack add --release-file` or leave the stack out with --stack", i, st.Name)
		}
		rf := st.ReleaseFile
		if !filepath.IsAbs(rf) {
			a, err := filepath.Abs(rf)
			if err != nil {
				return nil, nil, err
			}
			rf = a
		}
		rel, err := filepath.Rel(outDir, rf)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, nil, fmt.Errorf("stacks[%d].releaseFile: %s is outside %s, so it cannot be made relative: move the manifest under that directory or write the file higher up with --out", i, st.ReleaseFile, outDir)
		}
		st.ReleaseFile = filepath.ToSlash(rel)
		p.Stacks = append(p.Stacks, st)
		p.Order = append(p.Order, st.Name)
		needed[st.VM] = true
	}
	for _, v := range store.VMs {
		if !needed[v.Name] {
			continue
		}
		if !fleet.IsRef(v.KeyPath) {
			switch {
			case keyEnv != "":
				v.KeyPath = "env:" + keyEnv
			case len(needed) == 1:
				v.KeyPath = "env:DEPLOY_KEY_PATH"
			default:
				v.KeyPath = "env:DEPLOY_KEY_PATH_" + strings.ToUpper(envNameRe.ReplaceAllString(v.Name, "_"))
			}
		}
		if v.HostKey == "" && known != nil {
			v.HostKey = known(addrOf(&v))
		}
		p.VMs = append(p.VMs, v)
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	b = append(b, '\n')
	if _, err := fleet.ParseProject(b); err != nil {
		return nil, nil, err
	}
	return p, b, nil
}

// authorizedFromKnownHosts picks one public key out of known_hosts lines and returns it as
// "type base64", the form a project file pins. ed25519 is preferred, then the first plain key.
func authorizedFromKnownHosts(lines string) string {
	first := ""
	for _, l := range strings.Split(lines, "\n") {
		f := strings.Fields(l)
		if len(f) < 3 || strings.HasPrefix(f[0], "#") || strings.HasPrefix(f[0], "@") {
			continue
		}
		k := f[1] + " " + f[2]
		if f[1] == "ssh-ed25519" {
			return k
		}
		if first == "" {
			first = k
		}
	}
	return first
}
