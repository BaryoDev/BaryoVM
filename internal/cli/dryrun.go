// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"fmt"
	"slices"
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/backup"
	"github.com/BaryoDev/BaryoVM/internal/bootstrap"
	"github.com/BaryoDev/BaryoVM/internal/compose"
	"github.com/BaryoDev/BaryoVM/internal/deploy"
	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"github.com/BaryoDev/BaryoVM/internal/release"
	"github.com/BaryoDev/BaryoVM/internal/ui"
)

// dryStep is one thing a command would do, with the command it would run.
type dryStep struct {
	Step    string `json:"step"`
	Command string `json:"command"`
}

// dryVerify is the post-release check plan.
type dryVerify struct {
	Source   string   `json:"source"`
	Commands []string `json:"commands"`
	Attempts int      `json:"attempts"`
}

// dryHooks lists a manifest's hooks. They are shown, never run.
type dryHooks struct {
	PreDeploy  []string `json:"preDeploy"`
	PostDeploy []string `json:"postDeploy"`
}

// dryRunPlan is the Data of every --dry-run result. Commands is every command in the order it
// would run; Steps names them. The optional fields are extras for one command.
type dryRunPlan struct {
	DryRun    bool             `json:"dryRun"`
	Target    string           `json:"target"`
	Commands  []string         `json:"commands"`
	Steps     []dryStep        `json:"steps"`
	Transport string           `json:"transport,omitempty"`
	Rsync     [][]string       `json:"rsync,omitempty"`
	Hooks     *dryHooks        `json:"hooks,omitempty"`
	Verify    *dryVerify       `json:"verify,omitempty"`
	TLS       *release.TLSPlan `json:"tls,omitempty"`
	File      string           `json:"file,omitempty"`
	Notes     []string         `json:"notes,omitempty"`
}

func newDryRunPlan(target string) *dryRunPlan {
	return &dryRunPlan{DryRun: true, Target: target, Commands: []string{}, Steps: []dryStep{}}
}

func (p *dryRunPlan) add(step, command string) {
	p.Steps = append(p.Steps, dryStep{Step: step, Command: command})
	p.Commands = append(p.Commands, command)
}

func (p *dryRunPlan) note(format string, a ...any) {
	p.Notes = append(p.Notes, fmt.Sprintf(format, a...))
}

// emitDryRun prints the plan on stderr in human mode and emits it as the result's Data.
func emitDryRun(action string, p *dryRunPlan) {
	if !ui.JSON() {
		ui.Title(action + " (dry run)")
		for i, s := range p.Steps {
			ui.Detail(fmt.Sprintf("%d. %s", i+1, s.Step), s.Command)
		}
		for _, n := range p.Notes {
			ui.Notef("%s", n)
		}
	}
	ui.Emit(ui.Result{OK: true, Action: action, Message: p.Target + ": dry run, nothing was changed", Data: p})
}

// dryStackVM resolves a stack and its VM from the local inventory. It reads fleet.json and nothing
// else: no connection, no write.
func dryStackVM(name, action string) (*fleet.Stack, *fleet.VM, error) {
	store, err := fleet.Load()
	if err != nil {
		return nil, nil, err
	}
	st := store.FindStack(name)
	if st == nil {
		return nil, nil, fmt.Errorf("no stack named %q", name)
	}
	vm := store.Find(st.VM)
	if vm == nil {
		return nil, nil, fmt.Errorf("stack %q references unknown VM %q", name, st.VM)
	}
	if err := requireLinux(vm, action); err != nil {
		return nil, nil, err
	}
	return st, vm, nil
}

// dryStackBackup is the --dry-run of `stack backup`.
func dryStackBackup(name string) error {
	st, _, err := dryStackVM(name, "stack backup")
	if err != nil {
		return err
	}
	if !hasBackupStrategies(st) {
		return noBackupConfigError(st)
	}
	script, err := backup.BackupScript(backupConfig(st))
	if err != nil {
		ui.Emit(ui.Result{OK: false, Action: "stack backup", Error: err.Error()})
		return err
	}
	p := newDryRunPlan(name)
	p.add("backup", script)
	emitDryRun("stack backup", p)
	return nil
}

// dryStackRestore is the --dry-run of `stack restore`. It needs no --yes: nothing is replaced.
func dryStackRestore(name, file string) error {
	st, _, err := dryStackVM(name, "stack restore")
	if err != nil {
		return err
	}
	if !hasBackupStrategies(st) {
		return noBackupConfigError(st)
	}
	p := newDryRunPlan(name)
	if file != "" {
		p.File = file
		p.note("would restore %s, replacing what each backup strategy covers", file)
	} else {
		p.File = "newest"
		p.note("would restore the newest backup set in %s, replacing what each backup strategy covers (the file is picked on the VM, not read now)", backupConfig(st).DirNote())
	}
	script, err := backup.RestoreScriptE(backupConfig(st), file)
	if err != nil {
		ui.Emit(ui.Result{OK: false, Action: "stack restore", Error: err.Error()})
		return err
	}
	p.add("restore", script)
	emitDryRun("stack restore", p)
	return nil
}

// dryStackDrill is the --dry-run of `stack restore --drill`.
func dryStackDrill(name, file string) error {
	st, _, err := dryStackVM(name, "stack restore")
	if err != nil {
		return err
	}
	if !hasBackupStrategies(st) {
		return noBackupConfigError(st)
	}
	if backupConfig(st).DBContainer == "" {
		return errDrillNeedsContainer(name)
	}
	p := newDryRunPlan(name)
	p.File = file
	if file == "" {
		p.File = "newest"
	}
	p.note("%s", backup.DrillNote)
	p.add("drill", backup.DrillScript(backupConfig(st), file, backup.DrillContainerName(name, "<timestamp>")))
	emitDryRun("stack restore", p)
	return nil
}

// dryStackDeploy is the --dry-run of `stack deploy`.
func dryStackDeploy(name string, o compose.UpOptions) error {
	st, _, err := dryStackVM(name, "stack deploy")
	if err != nil {
		return err
	}
	cs := composeStack(st)
	p := newDryRunPlan(name)
	if o.Pull {
		p.add("pull", cs.PullCmd(o.Services))
	}
	p.add("compose up", cs.UpCmd(o))
	p.note("not checked: container_name conflicts with containers already on the VM (needs a read)")
	emitDryRun("stack deploy", p)
	return nil
}

// dryVMBootstrap is the --dry-run of `vm bootstrap`.
func dryVMBootstrap(name string) error {
	vm, err := requireVM(name)
	if err != nil {
		return err
	}
	if err := requireLinux(&vm, "vm bootstrap"); err != nil {
		return err
	}
	p := newDryRunPlan(vm.Name)
	p.add("install docker", bootstrap.Script())
	emitDryRun("vm bootstrap", p)
	return nil
}

// dryDeploy is the --dry-run of the top-level `deploy`. Env values are masked: they are often
// secrets and the plan goes to the terminal and to JSON.
func dryDeploy(vmName string, spec deploy.Spec) error {
	vm, err := requireVM(vmName)
	if err != nil {
		return err
	}
	if err := requireLinux(&vm, "deploy"); err != nil {
		return err
	}
	masked := make(map[string]string, len(spec.Env))
	for k := range spec.Env {
		masked[k] = "***"
	}
	spec.Env = masked
	p := newDryRunPlan(vm.Name)
	for i, c := range deploy.Commands(spec) {
		step := "run"
		switch {
		case spec.Pull && i == 0:
			step = "pull"
		case strings.HasPrefix(c, "docker rm") || strings.Contains(c, " rm -f"):
			step = "remove old container"
		}
		p.add(step, c)
	}
	if len(masked) > 0 {
		keys := make([]string, 0, len(masked))
		for k := range masked {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		p.note("environment %s is sent on stdin to an owner-only file; values are not shown", strings.Join(keys, ", "))
	}
	emitDryRun("deploy", p)
	return nil
}

// releaseDryOpts carries the release flags the plan depends on.
type releaseDryOpts struct {
	NoBuild, NoBackup, Adopt, Resume bool
}

// planRelease builds the --dry-run plan of a release in the order the real command runs it. It
// makes no connection: the remote reads the real release does are listed as notes instead.
func planRelease(name string, st *fleet.Stack, vm *fleet.VM, m *release.Manifest, o releaseDryOpts) *dryRunPlan {
	p := newDryRunPlan(name)
	if o.Resume {
		p.note("--resume is ignored in a dry run: the earlier release record is on the VM and is not read")
	}

	// 1. Backup, ahead of everything that changes the host.
	switch {
	case o.NoBackup:
		p.note("backup skipped: --no-backup")
	case !hasBackupStrategies(st):
		p.note("backup skipped: the stack has no backup configured")
	default:
		if script, err := backup.BackupScript(releaseBackupConfig(st, m)); err != nil {
			p.note("backup would fail: %v", err)
		} else {
			p.add("backup", script)
			p.note("a postgres-container strategy is skipped at run time if its container is not on the VM (not probed now)")
		}
	}

	// 2. Sync.
	p.Transport = string(release.PickTransport(lookPath))
	for _, sub := range m.Sync {
		if p.Transport == string(release.TransportRsync) {
			// The known_hosts file is a temp file written at run time; it is left out of the argv.
			argv := m.RsyncCmd(sub, vm.User, vm.Host, vm.Port, vm.KeyPath).Args
			p.Rsync = append(p.Rsync, argv)
			p.add("sync "+sub, strings.Join(argv, " "))
			continue
		}
		cmd, err := m.TarExtractCmd(sub)
		if err != nil {
			p.note("sync %s would fail: %v", sub, err)
			continue
		}
		p.add("sync "+sub+" (tar)", cmd)
	}
	if p.Transport == string(release.TransportRsync) {
		p.note("rsync also gets a UserKnownHostsFile with the keys already trusted for the host, written at run time")
	} else {
		p.note("tar transport: the host listing and the prune of stale files are decided at run time")
	}

	if !m.NoCompose {
		p.note("not checked: container_name conflicts, and the image names recorded for rollback (both read the VM)")
	}

	// 3. Builds, hooks, compose up.
	if !o.NoBuild {
		for _, b := range m.Builds {
			p.add("build "+b.Image, m.BuildCmd(b))
			if b.HasInput() {
				p.note("build %s: args and secrets are sent on stdin and are not read or shown in a dry run", b.Image)
			}
		}
	} else if len(m.Builds) > 0 {
		p.note("builds skipped: --no-build")
	}
	pre, post := m.PreDeployCmds(), m.PostDeployCmds()
	p.Hooks = &dryHooks{PreDeploy: pre, PostDeploy: post}
	for _, c := range pre {
		p.add("preDeploy hook (listed, not run)", c)
	}
	if !m.NoCompose {
		p.add("compose up", releaseComposeStack(st, m).UpCmd(compose.UpOptions{}))
	}
	if m.TLS != nil {
		plan := m.PlanTLS(tlsOptions(o.Adopt))
		p.TLS = &plan
		for _, c := range plan.Commands {
			p.add("tls", c)
		}
	}
	for _, c := range post {
		p.add("postDeploy hook (listed, not run)", c)
	}

	// 4. Verification.
	vp, err := releaseVerification(m, st)
	if err != nil {
		p.note("verify plan not built: %v", err)
	}
	p.Verify = &dryVerify{Source: vp.Source, Commands: vp.Commands, Attempts: vp.Attempts}
	for _, c := range vp.Commands {
		p.add("verify ("+vp.Source+")", c)
	}
	return p
}
