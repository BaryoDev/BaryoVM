// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/discover"
	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"github.com/BaryoDev/BaryoVM/internal/sshx"
	"github.com/BaryoDev/BaryoVM/internal/ui"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// stdinIsTerminal says whether --adopt may ask a question. A variable so tests can answer either way.
var stdinIsTerminal = func(cmd *cobra.Command) bool {
	f, ok := cmd.InOrStdin().(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

func newStackDiscoverCmd() *cobra.Command {
	var adopt, yes, useSudo bool
	cmd := &cobra.Command{
		Use:   "discover <vm>",
		Short: "List the compose projects already running on a VM and the `stack add` for each (read-only unless --adopt)",
		Long: "Runs `docker compose ls` on the VM and reads each project's config for its service count and a\n" +
			"likely postgres container, then prints the `stack add` command for each project.\n\n" +
			"The postgres container is a guess: the first service whose image name contains \"postgres\".\n" +
			"Check it before you rely on it for backups.\n\n" +
			"--adopt registers the projects in the local fleet, asking about each one on a terminal.\n" +
			"Without a terminal, or under -o json, it needs --yes. Projects already registered are skipped.",
		Args: cobra.ExactArgs(1),
		Example: "  baryovm stack discover oracle\n" +
			"  baryovm stack discover oracle --sudo\n" +
			"  baryovm stack discover oracle --adopt\n" +
			"  baryovm stack discover oracle --adopt --yes -o json",
		RunE: func(cmd *cobra.Command, args []string) error {
			const action = "stack discover"
			if yes && !adopt {
				return fmt.Errorf("--yes only confirms --adopt: add --adopt, or drop --yes")
			}
			interactive := !ui.JSON() && stdinIsTerminal(cmd)
			if adopt && !yes && !interactive {
				return fmt.Errorf("--adopt needs --yes when it cannot ask: there is no terminal, or the output is JSON")
			}
			store, err := fleet.Load()
			if err != nil {
				return err
			}
			vm := store.Find(args[0])
			if vm == nil {
				return fmt.Errorf("no VM named %q: register it first with `baryovm vm add`", args[0])
			}
			if err := requireLinux(vm, action); err != nil {
				return err
			}

			var projects []discover.Project
			err = ui.Step(fmt.Sprintf("discovering compose projects on %s", vm.Name), func() error {
				_, err := runOnVM(vm, func(c sshx.Runner) (opOutput, error) {
					var err error
					projects, err = discoverProjects(c, useSudo)
					return opOutput{}, err
				})
				return err
			})
			if err != nil {
				ui.Emit(ui.Result{OK: false, Action: action, Error: err.Error()})
				return &exitError{code: 1, msg: err.Error(), silent: true}
			}
			planProjects(projects, store, vm.Name, useSudo)

			if adopt {
				if err := adoptProjects(cmd, store, projects, vm.Name, useSudo, yes); err != nil {
					ui.Emit(ui.Result{OK: false, Action: action, Error: err.Error(), Data: projects})
					return &exitError{code: 1, msg: err.Error(), silent: true}
				}
			}
			if projects == nil {
				projects = []discover.Project{}
			}
			if !ui.JSON() {
				printDiscovery(projects)
			}
			ui.Emit(ui.Result{OK: true, Action: action, Message: discoverSummary(projects, vm.Name), Data: projects})
			return nil
		},
	}
	cmd.Flags().BoolVar(&adopt, "adopt", false, "register the projects found in the local fleet, asking about each one on a terminal")
	cmd.Flags().BoolVar(&yes, "yes", false, "with --adopt, register every project without asking (required without a terminal or under -o json)")
	cmd.Flags().BoolVar(&useSudo, "sudo", false, "run docker through sudo -n, for a VM where the SSH user cannot reach the docker socket. Adopted stacks are registered with --sudo")
	return cmd
}

// discoverProjects lists the projects and reads each one's config. A project whose config cannot be
// read stays in the list with a note: the rest of the VM is still worth showing.
func discoverProjects(c sshx.Runner, sudo bool) ([]discover.Project, error) {
	out, err := c.Run(discover.LsCmd(sudo))
	if err != nil {
		return nil, fmt.Errorf("docker compose ls failed: %w", err)
	}
	projects, err := discover.ParseLs(out)
	if err != nil {
		return nil, err
	}
	for i := range projects {
		p := &projects[i]
		cfg, err := c.Run(discover.ConfigCmd(p.ConfigFiles, sudo))
		if err == nil {
			err = discover.Inspect(p, cfg)
		}
		if err != nil {
			p.Note = joinNotes(p.Note, "config not read: "+firstLine(err.Error()))
		}
	}
	return projects, nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

func joinNotes(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}

// planProjects sets each project's state and `stack add` line against what is already registered.
// A stack counts as already registered when it points at the same directory on the same VM.
func planProjects(projects []discover.Project, store *fleet.Store, vm string, sudo bool) {
	for i := range projects {
		p := &projects[i]
		p.State = "found"
		if existing := registeredAt(store, vm, p.Dir); existing != "" {
			p.State = "already-registered"
			p.Note = joinNotes(p.Note, "already registered as "+existing)
			continue
		}
		if err := validStackName(p.Name); err != nil {
			p.State = "skipped"
			p.Note = joinNotes(p.Note, err.Error())
			continue
		}
		if other := store.FindStack(p.Name); other != nil {
			p.State = "skipped"
			p.Note = joinNotes(p.Note, fmt.Sprintf("a stack named %s already points at %s on %s", p.Name, other.Dir, other.VM))
			continue
		}
		if len(p.ConfigFiles) > 1 {
			p.State = "skipped"
			continue
		}
		p.AddCommand = discover.AddCommand(*p, vm, sudo)
	}
}

func registeredAt(store *fleet.Store, vm, dir string) string {
	for _, s := range store.Stacks {
		if s.VM == vm && strings.TrimRight(s.Dir, "/") == strings.TrimRight(dir, "/") {
			return s.Name
		}
	}
	return ""
}

// adoptProjects registers every project still in state found, through the same code `stack add`
// runs. Nothing is saved until the end, so a refusal part way leaves the fleet as it was.
func adoptProjects(cmd *cobra.Command, store *fleet.Store, projects []discover.Project, vm string, sudo, yes bool) error {
	in := bufio.NewReader(cmd.InOrStdin())
	changed := 0
	for i := range projects {
		p := &projects[i]
		if p.State != "found" {
			continue
		}
		if !yes && !askYesNo(in, fmt.Sprintf("register %s (%s on %s)? [y/N] ", p.Name, p.Dir, vm)) {
			p.State = "skipped"
			p.Note = joinNotes(p.Note, "declined")
			continue
		}
		named := map[string]bool{"sudo": sudo, "file": p.File != "", "db-container": p.PostgresGuess != ""}
		_, _, err := registerStack(store, stackAddInput{
			Name: p.Name, VM: vm, Path: p.Dir, File: p.File, DBContainer: p.PostgresGuess, Sudo: sudo,
			Changed: func(n string) bool { return named[n] },
		})
		if err != nil {
			return fmt.Errorf("register %s: %w", p.Name, err)
		}
		p.State = "registered"
		changed++
	}
	if changed == 0 {
		return nil
	}
	if err := store.Save(); err != nil {
		for i := range projects {
			if projects[i].State == "registered" {
				projects[i].State = "found"
			}
		}
		return err
	}
	return nil
}

func askYesNo(in *bufio.Reader, prompt string) bool {
	fmt.Fprint(os.Stderr, prompt)
	line, _ := in.ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

func discoverSummary(projects []discover.Project, vm string) string {
	registered := 0
	for _, p := range projects {
		if p.State == "registered" {
			registered++
		}
	}
	msg := fmt.Sprintf("found %s on %s", plural(len(projects), "compose project", "compose projects"), vm)
	if registered > 0 {
		msg += fmt.Sprintf(", registered %d", registered)
	}
	return msg
}

func printDiscovery(projects []discover.Project) {
	if len(projects) == 0 {
		return
	}
	for _, p := range projects {
		db := "no postgres found"
		if p.PostgresGuess != "" {
			db = "postgres guess: " + p.PostgresGuess + " (image name contains postgres, check it)"
		}
		fmt.Printf("  %s  (%s, %s)  [%s]\n", p.Dir, plural(p.Services, "service", "services"), db, p.State)
		if p.Note != "" {
			fmt.Printf("      note: %s\n", p.Note)
		}
		if p.AddCommand != "" && p.State == "found" {
			fmt.Printf("      %s\n", p.AddCommand)
		}
	}
}
