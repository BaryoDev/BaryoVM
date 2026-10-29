// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/BaryoDev/BaryoVM/internal/backup"
	"github.com/BaryoDev/BaryoVM/internal/compose"
	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"github.com/BaryoDev/BaryoVM/internal/hostkeys"
	"github.com/BaryoDev/BaryoVM/internal/release"
	"github.com/BaryoDev/BaryoVM/internal/releasestate"
	"github.com/BaryoDev/BaryoVM/internal/rollback"
	"github.com/BaryoDev/BaryoVM/internal/sshx"
	"github.com/BaryoDev/BaryoVM/internal/ui"
	"github.com/spf13/cobra"
)

func newStackCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "stack", Short: "Manage docker compose stacks on your VMs"}
	cmd.AddCommand(
		newStackAddCmd(), newStackListCmd(), newStackRemoveCmd(),
		newStackDeployCmd(), newStackReleaseCmd(), newStackPsCmd(), newStackPullCmd(), newStackLogsCmd(),
		newStackBackupCmd(), newStackRestoreCmd(), newStackBackupsCmd(),
		newStackUpdateCmd(), newStackSetUpdateCmd(), newStackTLSCmd(), newStackEnvCmd(),
		newStackRollbackCmd(),
	)
	return cmd
}

var stackNameRe = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9_.-]*$`)

// validStackName keeps the name safe to use in a file name on the host (the release record) and
// in messages. Names already stored are not checked again.
func validStackName(name string) error {
	if !stackNameRe.MatchString(name) {
		return fmt.Errorf("stack name %q is not allowed: use letters, digits, '-', '_' and '.', and do not start with '.'", name)
	}
	return nil
}

func newStackAddCmd() *cobra.Command {
	var vm, path, file string
	var dbContainer, dbName, dbUser, envFile, backupDir, releaseFile string
	var keep int
	var useSudo bool
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Register a compose stack (a project dir on a VM)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validStackName(args[0]); err != nil {
				return err
			}
			store, err := fleet.Load()
			if err != nil {
				return err
			}
			if store.Find(vm) == nil {
				return fmt.Errorf("no VM named %q: register it first with `baryovm vm add`", vm)
			}
			// Re-adding a stack changes only what the flags name. It used to replace the record,
			// which cleared the update policy that only `stack set-update` writes, and `stack add`
			// is where the backup error sends you to register a database (#63).
			st := fleet.Stack{Name: args[0]}
			verb := "registered"
			if existing := store.FindStack(args[0]); existing != nil {
				st = *existing
				verb = "updated"
			}
			st.VM, st.Dir = vm, path
			if err := backup.ValidateEnvFile(envFile); err != nil {
				return err
			}
			f := cmd.Flags()
			for name, apply := range map[string]func(){
				"file":         func() { st.File = file },
				"db-container": func() { st.DBContainer = dbContainer },
				"db-name":      func() { st.DBName = dbName },
				"db-user":      func() { st.DBUser = dbUser },
				"env-file":     func() { st.EnvFile = envFile },
				"backup-dir":   func() { st.BackupDir = backupDir },
				"keep":         func() { st.Keep = keep },
				"release-file": func() { st.ReleaseFile = releaseFile },
				"sudo":         func() { st.Sudo = useSudo },
			} {
				if f.Changed(name) {
					apply()
				}
			}
			store.UpsertStack(st)
			if err := store.Save(); err != nil {
				return err
			}
			ui.Emit(ui.Result{OK: true, Action: "stack add", Message: fmt.Sprintf("%s stack %s (%s on %s)", verb, args[0], path, vm), Data: st})
			return nil
		},
	}
	cmd.Flags().StringVar(&vm, "vm", "", "VM the stack runs on (required)")
	cmd.Flags().StringVar(&path, "path", "", "remote project directory, e.g. /opt/barakocms (required)")
	cmd.Flags().StringVar(&file, "file", "", "compose file name (default: compose's own default)")
	cmd.Flags().StringVar(&dbContainer, "db-container", "", "postgres container for backups, e.g. deploy-postgres-1")
	cmd.Flags().StringVar(&dbName, "db-name", "", "database to back up")
	cmd.Flags().StringVar(&dbUser, "db-user", "", "database user (default: postgres)")
	cmd.Flags().StringVar(&envFile, "env-file", "", "config file to back up, relative to the project dir (e.g. .env)")
	cmd.Flags().StringVar(&backupDir, "backup-dir", "", "remote dir for backups (default: ~/<name>-backups)")
	cmd.Flags().IntVar(&keep, "keep", 0, "backups to retain per kind (default 14)")
	cmd.Flags().StringVar(&releaseFile, "release-file", "", "local JSON release manifest for `stack release`")
	cmd.Flags().BoolVar(&useSudo, "sudo", false,
		"run this stack's remote commands via sudo -n, for a stack whose .env or deploy root is "+
			"root-owned. See USAGE.md for what it covers and the three things that change with it")
	_ = cmd.MarkFlagRequired("vm")
	_ = cmd.MarkFlagRequired("path")
	return cmd
}

// checkContainerNames refuses a compose up that would collide with a container this project does
// not own. A failure to read the names is a warning rather than a refusal: compose up reads the same
// file and reports what is wrong with it, and these commands ran without this check before.
//
// With adopt, a conflict held only by hand-run containers is resolved by removing them (#7), after
// their mounts are read: a named volume the compose file does not declare external refuses the
// adoption, with nothing removed. A container owned by another compose project is never adopted.
func checkContainerNames(c sshx.Runner, cs compose.Stack, adopt bool) error {
	err := compose.CheckContainerNames(c, cs)
	var conflict *compose.NameConflictError
	if errors.As(err, &conflict) {
		if !adopt {
			return err
		}
		var names []string
		for _, cf := range conflict.Conflicts {
			if cf.Project != "" {
				return err
			}
			names = append(names, cf.Name)
		}
		plan, perr := compose.PlanAdoption(c, cs, names)
		if perr != nil {
			return perr
		}
		for _, a := range plan {
			ui.Notef("adopting: removing container %s (image %s, created %s); there is no way back", a.Name, a.Image, a.Created)
		}
		return compose.Adopt(c, cs, plan)
	}
	if err != nil {
		// A stack dir that does not exist yet has no compose file to read, so there is nothing
		// to collide with and nothing worth a warning (#121).
		// Match the stack's own path: any other missing file (an env file, a bind mount) is a real
		// problem the operator should hear about.
		if strings.Contains(err.Error(), strings.TrimRight(cs.Dir, "/")+": No such file or directory") {
			return nil
		}
		ui.Warnf("could not check container names before compose up: %v", err)
	}
	return nil
}

// backupCause turns a failed pre-release backup into a message that names the cause. The raw error
// carries the whole remote script, which says nothing about why it failed.
func backupCause(err error, container string) string {
	msg := err.Error()
	if strings.Contains(msg, "No such container") {
		return fmt.Sprintf("no container named %s", container)
	}
	if i := strings.LastIndex(msg, "`: "); i >= 0 {
		msg = msg[i+3:]
	}
	return strings.TrimSpace(msg)
}

// runLocal runs a local command (e.g. rsync) and returns combined output. A variable so a release
// can be driven in tests without transferring anything.
var runLocal = func(c *exec.Cmd) (string, error) {
	out, err := c.CombinedOutput()
	return string(out), err
}

// lookPath finds a local tool. A variable so the transport choice can be driven without
// changing PATH.
var lookPath = exec.LookPath

// syncTar copies one sync entry over its own SSH session as a tar stream.
func syncTar(m *release.Manifest, vm *fleet.VM, sub string) error {
	c, err := dialRelease(vm)
	if err != nil {
		return err
	}
	defer c.Close()
	sr, ok := c.(sshx.StdinRunner)
	if !ok {
		return fmt.Errorf("this connection cannot stream a tar archive")
	}
	return m.TarSync(sr, sub)
}

// remoteSession is what a release needs from a connection: run commands, then hang up.
// knownHostsFileFor writes the host keys BaryoVM trusts for a VM to a private temp file, so rsync's
// ssh checks the same keys the rest of the command does instead of ~/.ssh/known_hosts. A host with
// no recorded key is dialled once first, which records it (or refuses it) the normal way. The
// caller removes the file with cleanup. A variable so tests do not need a real host.
var knownHostsFileFor = func(vm *fleet.VM) (string, func(), error) {
	port := vm.Port
	if port <= 0 {
		port = 22
	}
	addr := net.JoinHostPort(vm.Host, strconv.Itoa(port))
	store := hostkeys.New(baryovmHome())
	keys, err := store.KnownHostsFor(addr)
	if err != nil {
		return "", func() {}, err
	}
	if keys == "" {
		c, err := dialRelease(vm)
		if err != nil {
			return "", func() {}, err
		}
		_ = c.Close()
		if keys, err = store.KnownHostsFor(addr); err != nil {
			return "", func() {}, err
		}
	}
	if keys == "" {
		return "", func() {}, fmt.Errorf("no recorded host key for %s: rsync will not learn one", addr)
	}
	f, err := os.CreateTemp("", "baryovm-known-hosts-*") // created 0600
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.Remove(f.Name()) }
	if _, err := f.WriteString(keys); err != nil {
		_ = f.Close()
		cleanup()
		return "", func() {}, err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return f.Name(), cleanup, nil
}

type remoteSession interface {
	sshx.Runner
	Close() error
}

// dialRelease connects to a VM for a release. A variable for the same reason as runOnVM: so the whole
// command, in its real order, can run against a fake VM.
var dialRelease = func(vm *fleet.VM) (remoteSession, error) {
	c, err := sshx.Dial(vm.Target())
	if err != nil {
		return nil, err
	}
	return c, nil
}

func newStackReleaseCmd() *cobra.Command {
	var config string
	var noBuild, noBackup, adopt, resume, clearState, dryRun bool
	cmd := &cobra.Command{
		Use:   "release <name>",
		Short: "Sync source to the VM, build images there, then compose up (config-driven)",
		Args:  cobra.ExactArgs(1),
		Example: "  baryovm stack release baryoclub\n" +
			"  baryovm stack release baryoclub --config ./baryovm.release.json --no-backup",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := fleet.Load()
			if err != nil {
				return err
			}
			st := store.FindStack(args[0])
			if st == nil {
				return fmt.Errorf("no stack named %q", args[0])
			}
			vm := store.Find(st.VM)
			if vm == nil {
				return fmt.Errorf("stack %q references unknown VM %q", args[0], st.VM)
			}
			if err := requireLinux(vm, "stack release"); err != nil {
				return err
			}
			manifestPath := config
			if manifestPath == "" {
				manifestPath = st.ReleaseFile
			}
			if manifestPath == "" {
				return fmt.Errorf("no release manifest: pass --config <file> or set --release-file on `stack add`")
			}
			m, err := release.Load(manifestPath, st.Sudo)
			if err != nil {
				return err
			}

			// rr is the progress record once a release has begun one. Until then a failure has
			// nothing on the host to report or clean up.
			var rr *relRun
			fail := func(e error) error {
				res := ui.Result{OK: false, Action: "stack release", Error: e.Error()}
				if rr != nil && rr.st != nil {
					if len(rr.st.Steps) == 0 {
						_ = releasestate.Remove(rr.sc, rr.loc)
					} else {
						res.Data = stateData(rr.loc, rr.st, args[0])
						ui.Notef("release state kept at %s: %s", rr.loc.File, stateHint(args[0], rr.st))
					}
				}
				ui.Emit(res)
				return e
			}

			if resume && clearState {
				return fail(fmt.Errorf("--resume and --clear cannot be used together: resume continues the earlier release, clear drops its record"))
			}
			loc := releasestate.For(m.RemoteRoot, args[0], m.Sudo)
			if clearState {
				if dryRun {
					return fail(fmt.Errorf("--dry-run and --clear cannot be used together: clear changes the release record"))
				}
				return runReleaseClear(vm, loc, releaseBackupConfig(st, m), args[0])
			}

			// Local checks first, so a refusal that needs no host never touches the state file.
			// A static site's artifact is built outside the release. Refuse before anything is
			// backed up, synced or run if it is not there (#68).
			if err := m.CheckRequireLocal(); err != nil {
				return fail(err)
			}

			// The compose dir holds a root-owned .env. Refuse before anything is transferred, rather
			// than documenting the exclusion and hoping: rsync --delete removes whatever is on the
			// VM and not in the local source, and a database password is exactly such a file.
			if err := m.CheckComposeDir(st.Dir); err != nil {
				return fail(err)
			}

			// A dry run stops here: it has read only local files and fleet.json, and opens no
			// connection, takes no lock and writes no record.
			if dryRun {
				emitDryRun("stack release", planRelease(args[0], st, vm, m, releaseDryOpts{NoBuild: noBuild, NoBackup: noBackup, Adopt: adopt, Resume: resume}))
				return nil
			}

			// The progress record. It is written under the host lock before the first step that
			// changes anything, so two releases cannot both read "nothing in progress" (#136).
			sc, err := dialRelease(vm)
			if err != nil {
				return fail(err)
			}
			defer sc.Close()
			commit := gitHead(m.LocalPath())
			rr = &relRun{sc: sc, loc: loc}
			if resume {
				prev, err := releasestate.Open(sc, loc)
				if err != nil {
					rr = nil
					return fail(err)
				}
				if prev == nil {
					rr = nil
					return fail(fmt.Errorf("nothing to resume: no release record at %s", loc.File))
				}
				// Resuming while the first release is still running would start a second one over the
				// same build and containers. The record's last write is the only sign of life we have.
				if prev.Active(time.Now()) {
					rr = nil
					return fail(fmt.Errorf("the earlier release of %s wrote its record at %s and may still be running: wait about %d minutes with no new write, or drop the record with `baryovm stack release %s --clear` if you know it is dead",
						args[0], prev.Updated, int(releasestate.ActiveWindow/time.Minute), args[0]))
				}
				if _, bad := prev.Unsafe(); bad {
					rr = nil
					return refuseState(loc, prev, args[0])
				}
				if commit != "" && prev.Commit != "" && commit != prev.Commit {
					rr = nil
					return fail(fmt.Errorf("the earlier release was at commit %s and the source is now at %s: check out the earlier commit to resume, or drop the record with `baryovm stack release %s --clear`",
						prev.Commit, commit, args[0]))
				}
				rr.st, rr.resume = prev, true
				ui.Notef("resuming the earlier release of %s", args[0])
			} else {
				cur, existing, err := releasestate.Begin(sc, loc, releasestate.New(args[0], commit))
				if err != nil {
					rr = nil
					return fail(err)
				}
				if existing != nil {
					rr = nil
					return refuseState(loc, existing, args[0])
				}
				rr.st = cur
			}

			// Safety first: back up before releasing (unless opted out / no DB configured).
			if !noBackup && st.DBContainer != "" && st.DBName != "" {
				// A first release has no database container yet, so there is nothing to dump. That is
				// skipped with one line. A container that exists and fails to dump still stops the
				// release, and so does a probe that cannot tell (it falls through to the backup).
				bcfg := releaseBackupConfig(st, m)
				if err := rr.step("backup", func() error {
					skip := false
					if err := ui.Step("check database container", func() error {
						c, err := dialRelease(vm)
						if err != nil {
							return err
						}
						defer c.Close()
						if ok, e := backup.ContainerExists(c, bcfg); e == nil && !ok {
							skip = true
						}
						return nil
					}); err != nil {
						return fmt.Errorf("pre-release backup failed: %w", err)
					}
					if skip {
						ui.Notef("no database container named %s yet, skipping the pre-release backup", st.DBContainer)
						return nil
					}
					if err := ui.Step("backup", func() error {
						c, err := dialRelease(vm)
						if err != nil {
							return err
						}
						defer c.Close()
						out, err := backup.Backup(c, bcfg)
						if err == nil {
							// Recorded at once, so a failure further on names the backup to restore.
							rr.st.Backup, rr.st.BackupFile = bcfg.DirNote(), releasestate.ParseBackupFile(out)
							_ = releasestate.Write(rr.sc, rr.loc, rr.st)
						}
						return err
					}); err != nil {
						return fmt.Errorf("pre-release backup failed: %s", backupCause(err, st.DBContainer))
					}
					return nil
				}); err != nil {
					return fail(err)
				}
			}

			// 1. Sync each source subdir. The compose dir (with .env) is never synced. rsync when the
			// operator's machine has it, otherwise a tar stream over the SSH session.
			transport := release.PickTransport(lookPath)
			ui.Notef("sync transport: %s", transport)
			for _, sub := range m.Sync {
				sub := sub
				if err := rr.step("sync:"+sub, func() error {
					return ui.Step("sync "+sub, func() error {
						if transport == release.TransportTar {
							return syncTar(m, vm, sub)
						}
						khFile, cleanup, e := knownHostsFileFor(vm)
						if e != nil {
							return e
						}
						defer cleanup()
						out, e := runLocal(m.RsyncCmdKnownHosts(sub, vm.User, vm.Host, vm.Port, vm.KeyPath, khFile))
						if e != nil {
							return fmt.Errorf("%w: %s", e, strings.TrimSpace(out))
						}
						return nil
					})
				}); err != nil {
					return fail(err)
				}
			}

			// 2 + 3: build images on the VM, then compose up.
			c := sc

			// A container_name held by a container this project does not own, typically one started
			// by hand with docker run, fails compose up at the very end. It is checked here, after
			// the sync, because compose reads the compose file from the VM and on a first release
			// that file is not there until the sync has put it there (#150). It still comes before
			// the image builds and compose up, which are the expensive and disruptive steps.
			if !m.NoCompose {
				if err := rr.step("check-names", func() error {
					return ui.Step("check container names", func() error {
						return checkContainerNames(c, releaseComposeStack(st, m), adopt)
					})
				}); err != nil {
					return fail(err)
				}
				// The images the stack runs now, saved next to the backups for `stack rollback`
				// (#37). After the name check because that is the first `compose config`. Not on
				// a resume past deploy, where "now" is already the new release.
				if !rr.st.Done("deploy") {
					recordRollback(c, st, m, args[0])
				}
			}

			if !noBuild {
				for _, b := range m.Builds {
					b := b
					if err := rr.step("build:"+b.Image, func() error {
						return ui.Step("build "+b.Image, func() error {
							out, e := c.Run(m.BuildCmd(b))
							if e != nil {
								return fmt.Errorf("%w: %s", e, lastLines(out, 8))
							}
							return nil
						})
					}); err != nil {
						return fail(err)
					}
				}
			}

			// Guards run before compose up, not after. A check that runs afterwards can only report
			// that the stack is now running something it should have refused, which is a post-mortem
			// wearing a gate's clothes.
			preCmds := m.PreDeployCmds()
			for i, cmdStr := range preCmds {
				cmdStr := cmdStr
				label := fmt.Sprintf("pre %d/%d", i+1, len(preCmds))
				if err := rr.step(fmt.Sprintf("pre:%d", i+1), func() error {
					return ui.Step(label, func() error {
						out, e := c.Run(cmdStr)
						if e != nil {
							return fmt.Errorf("%w: %s", e, lastLines(out, 8))
						}
						return nil
					})
				}); err != nil {
					return fail(err)
				}
			}

			// A static site has no compose file: it is files the host's own web server serves.
			// Running compose up there fails after the sync has already landed, which is the
			// worst place to fail, so the manifest says up front that there is nothing to bring up.
			if !m.NoCompose {
				if err := rr.step("deploy", func() error {
					return ui.Step("deploy", func() error {
						_, e := compose.Up(c, releaseComposeStack(st, m), compose.UpOptions{})
						return e
					})
				}); err != nil {
					return fail(err)
				}
			}

			// The vhost and certificate, when the manifest declares them. After compose up so the
			// upstream exists, before postDeploy so a hook that reloads nginx sees the new vhost.
			if m.TLS != nil {
				if err := rr.step("tls", func() error { return m.ApplyTLS(c, tlsOptions(adopt), ui.Step) }); err != nil {
					return fail(err)
				}
			}

			// Whatever has to happen on the VM once the files are in place: restore an SELinux
			// context, reload nginx, warm a cache. Ordered, and the first failure stops the release
			// rather than reporting success over a half-applied deploy.
			postCmds := m.PostDeployCmds()
			for i, cmdStr := range postCmds {
				cmdStr := cmdStr
				label := fmt.Sprintf("post %d/%d", i+1, len(postCmds))
				if err := rr.step(fmt.Sprintf("post:%d", i+1), func() error {
					return ui.Step(label, func() error {
						out, e := c.Run(cmdStr)
						if e != nil {
							return fmt.Errorf("%w: %s", e, lastLines(out, 8))
						}
						return nil
					})
				}); err != nil {
					return fail(err)
				}
			}

			// Ask the running site whether the deploy worked, rather than inferring it from the
			// fact that nothing errored on the way here. A sync can land, compose can come up, and
			// the site can still serve the wrong thing: that is how the umbraco-pwa demo served
			// Umbraco's install screen through two releases, a perfectly valid 200 at the exact URL
			// every listing pointed at.
			m.ReleaseTag = commit
			if m.ReleaseTag == "" {
				m.ReleaseTag = time.Now().UTC().Format("20060102T150405Z")
			}
			plan := m.Verification(st.HealthURL)
			attemptsUsed := 0
			if len(plan.Commands) == 0 {
				ui.Warnf("nothing to verify: set verify in the manifest or healthUrl on the stack")
			} else {
				if err := rr.step("verify", func() error {
					return ui.Step("verify", func() error {
						var last error
						for attempt := 1; attempt <= plan.Attempts; attempt++ {
							attemptsUsed = attempt
							last = nil
							for _, cmdStr := range plan.Commands {
								out, e := c.Run(cmdStr)
								if e != nil {
									last = fmt.Errorf("%w: %s", e, lastLines(out, 8))
									break
								}
							}
							if last == nil {
								return nil
							}
							// A service that restarts is normal; one that never answers is not.
							if attempt < plan.Attempts {
								time.Sleep(plan.Delay)
							}
						}
						return last
					})
				}); err != nil {
					return fail(fmt.Errorf("released but failed verification, the stack may need attention: %w", err))
				}
			}

			// Finished: drop the progress record and note which commit is deployed.
			if err := releasestate.Remove(sc, loc); err != nil {
				ui.Warnf("release done but the record at %s could not be removed, clear it with `baryovm stack release %s --clear`: %v", loc.File, args[0], err)
			}
			// A finished release is a good state again, so the next one may record over it. Not only
			// for compose manifests: a clear protects whatever the manifest, so the finish lifts it the same way.
			if _, err := rollback.SetProtected(sc, releaseBackupConfig(st, m), false); err != nil {
				ui.Warnf("could not unprotect the rollback record, the next release will keep it: %v", err)
			}
			if commit != "" {
				if err := releasestate.RecordDeployed(sc, loc, args[0], commit); err != nil {
					ui.Warnf("could not record the deployed commit: %v", err)
				}
			}

			// Say which check ran, so a release that verified nothing cannot read as one that did.
			ui.Emit(ui.Result{OK: true, Action: "stack release", Message: args[0] + " released",
				Data: map[string]any{
					"transport": string(transport),
					"commit":    commit,
					"verify": map[string]any{
						"source":   plan.Source,
						"checks":   len(plan.Commands),
						"attempts": attemptsUsed,
					},
				}})
			if len(plan.Commands) > 0 {
				ui.Successf("%s: verified by %s, %d check(s) passed on attempt %d", args[0],
					verifyLabel(plan.Source), len(plan.Commands), attemptsUsed)
			}
			ui.Successf("%s: release done", args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&config, "config", "", "release manifest path (overrides the stack's --release-file)")
	cmd.Flags().BoolVar(&noBuild, "no-build", false, "skip building images (just sync + compose up)")
	cmd.Flags().BoolVar(&noBackup, "no-backup", false, "skip the automatic pre-release DB backup")
	cmd.Flags().BoolVar(&adopt, "adopt", false, "take over what BaryoVM did not create: a hand-run container holding a container_name (removed with docker rm -f and NOT recoverable: its writable layer and anonymous volumes are lost; refused if it has a named volume not declared external), and with a tls block a vhost file (backed up first)")
	cmd.Flags().BoolVar(&resume, "resume", false, "continue an earlier release that stopped, from the first step not done, reusing its backup")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the rsync, build, hook, backup and compose commands without connecting or changing anything")
	cmd.Flags().BoolVar(&clearState, "clear", false, "drop the record of an earlier release that stopped, without running anything")
	return cmd
}

// verifyLabel names a verify plan's source for the success line.
func verifyLabel(source string) string {
	switch source {
	case "manifest":
		return "the manifest verify list"
	case "healthUrl":
		return "the stack healthUrl"
	case "verifyVisual":
		return "the manifest verifyVisual block"
	}
	return source
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// composeStack is the compose target for a registered stack. Every command that drives compose for
// a stack goes through here, so Sudo cannot be dropped by a call site assembling the struct by hand,
// which is how `stack deploy`, `stack release` and the release hooks each lost it in turn.
func composeStack(st *fleet.Stack) compose.Stack {
	return compose.Stack{Dir: st.Dir, File: st.File, Sudo: st.Sudo}
}

// releaseComposeStack is the compose target for a release's `compose up`. The release made one
// decision about root, in release.Load, which folds the stack's --sudo into the manifest; this reads
// that decision rather than asking the stack a second time and answering differently from the build
// and the hooks beside it.
func releaseComposeStack(st *fleet.Stack, m *release.Manifest) compose.Stack {
	cs := composeStack(st)
	cs.Sudo = m.Sudo
	return cs
}

// releaseBackupConfig is the pre-release backup of the stack being released, for the same reason:
// one command asks for root once. The dump and the .env copy are the steps that most need the
// answer to be the same as the release's, since the .env is the file the flag exists for and a copy
// of it attempted as the SSH user can leave the release running with no backup of the secret.
func releaseBackupConfig(st *fleet.Stack, m *release.Manifest) backup.Config {
	cfg := backupConfig(st)
	cfg.Sudo = m.Sudo
	return cfg
}

func backupConfig(st *fleet.Stack) backup.Config {
	return backup.Config{
		Sudo: st.Sudo,
		Name: st.Name, Dir: st.Dir, EnvFile: st.EnvFile,
		DBContainer: st.DBContainer, DBName: st.DBName, DBUser: st.DBUser,
		BackupDir: st.BackupDir, Keep: st.Keep,
	}
}

// runStackBackup resolves a stack (which must have backup config), dials its VM,
// and runs a backup op with a spinner.
func runStackBackup(name, action, step string, fn func(c sshx.Runner, st *fleet.Stack) (opOutput, error)) error {
	store, err := fleet.Load()
	if err != nil {
		return err
	}
	st := store.FindStack(name)
	if st == nil {
		return fmt.Errorf("no stack named %q", name)
	}
	if st.DBContainer == "" || st.DBName == "" {
		return fmt.Errorf("stack %q has no backup config: set --db-container and --db-name via `baryovm stack add %s ...`", name, name)
	}
	vm := store.Find(st.VM)
	if vm == nil {
		return fmt.Errorf("stack %q references unknown VM %q", name, st.VM)
	}
	if err := requireLinux(vm, action); err != nil {
		return err
	}
	var o opOutput
	err = ui.Step(step, func() error {
		var err error
		o, err = runOnVM(vm, func(c sshx.Runner) (opOutput, error) { return fn(c, st) })
		return err
	})
	if err != nil {
		ui.Emit(ui.Result{OK: false, Action: action, Error: err.Error()})
		return err
	}
	emitOp(name, action, o)
	return nil
}

func newStackBackupCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "backup <name>",
		Short: "Back up the stack's database + config (pg_dump + .env)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if dryRun {
				return dryStackBackup(args[0])
			}
			return runStackBackup(args[0], "stack backup", fmt.Sprintf("backing up %s", args[0]),
				func(c sshx.Runner, st *fleet.Stack) (opOutput, error) {
					return bareOutput(backup.Backup(c, backupConfig(st)))
				})
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the backup script without connecting")
	return cmd
}

func newStackBackupsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "backups <name>",
		Short: "List the stack's database backups",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStackBackup(args[0], "stack backups", fmt.Sprintf("listing backups for %s", args[0]),
				func(c sshx.Runner, st *fleet.Stack) (opOutput, error) {
					out, err := backup.List(c, backupConfig(st))
					if err != nil {
						return opOutput{}, err
					}
					// A listing is the one backup op where an empty answer is ordinary, so it says how
					// many it found rather than leaving the count to be read out of the text.
					r := backup.DescribeList(out)
					return opOutput{text: out, data: r, note: r.Note}, nil
				})
		},
	}
}

func newStackRestoreCmd() *cobra.Command {
	var file string
	var yes, dryRun bool
	cmd := &cobra.Command{
		Use:   "restore <name>",
		Short: "Restore the stack's database from a backup (REPLACES current data)",
		Args:  cobra.ExactArgs(1),
		Example: "  baryovm stack restore baryoclub --yes\n" +
			"  baryovm stack restore baryoclub --file db-20260717-011514.dump --yes",
		RunE: func(cmd *cobra.Command, args []string) error {
			if dryRun {
				return dryStackRestore(args[0], file)
			}
			if !yes {
				return fmt.Errorf("restore REPLACES the %q database: re-run with --yes to confirm", args[0])
			}
			return runStackBackup(args[0], "stack restore", fmt.Sprintf("restoring %s", args[0]),
				func(c sshx.Runner, st *fleet.Stack) (opOutput, error) {
					return bareOutput(backup.Restore(c, backupConfig(st), file))
				})
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "backup to restore (name or path); default: newest")
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm the destructive restore")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the restore script and the backup it would use, without connecting (needs no --yes)")
	return cmd
}

func newStackListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List registered stacks",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := fleet.Load()
			if err != nil {
				return err
			}
			if ui.JSON() {
				ui.Emit(ui.Result{OK: true, Action: "stack list", Data: store.Stacks})
				return nil
			}
			if len(store.Stacks) == 0 {
				ui.Warnf("no stacks registered: add one with `baryovm stack add`")
				return nil
			}
			ui.Title(fmt.Sprintf("Stacks (%d)", len(store.Stacks)))
			for _, s := range store.Stacks {
				fmt.Printf("%s  %s\n", ui.AccentStyle.Render(s.Name), ui.DimStyle.Render(fmt.Sprintf("%s:%s", s.VM, s.Dir)))
			}
			return nil
		},
	}
}

func newStackRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove a stack registration (does not touch the VM)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := fleet.Load()
			if err != nil {
				return err
			}
			if !store.RemoveStack(args[0]) {
				return fmt.Errorf("no stack named %q", args[0])
			}
			if err := store.Save(); err != nil {
				return err
			}
			ui.Emit(ui.Result{OK: true, Action: "stack remove", Message: "removed " + args[0]})
			return nil
		},
	}
}

func newStackDeployCmd() *cobra.Command {
	var svcs []string
	var pull, force, noDeps, adopt, dryRun bool
	cmd := &cobra.Command{
		Use:   "deploy <name>",
		Short: "compose up -d the stack (optionally pull + recreate)",
		Args:  cobra.ExactArgs(1),
		Example: "  baryovm stack deploy barako --force-recreate --service api,console\n" +
			"  baryovm stack deploy barako --pull",
		RunE: func(cmd *cobra.Command, args []string) error {
			if dryRun {
				return dryStackDeploy(args[0], compose.UpOptions{Services: svcs, Pull: pull, ForceRecreate: force, NoDeps: noDeps})
			}
			return runStackOp(args[0], "stack deploy", fmt.Sprintf("deploying stack %s", args[0]),
				func(c sshx.Runner, cs compose.Stack) (string, error) {
					if err := checkContainerNames(c, cs, adopt); err != nil {
						return "", err
					}
					return compose.Up(c, cs, compose.UpOptions{Services: svcs, Pull: pull, ForceRecreate: force, NoDeps: noDeps})
				})
		},
	}
	cmd.Flags().StringSliceVar(&svcs, "service", nil, "limit to these services (comma-separated)")
	cmd.Flags().BoolVar(&pull, "pull", false, "pull images before recreating")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the compose commands without connecting")
	cmd.Flags().BoolVar(&force, "force-recreate", false, "recreate containers even if unchanged")
	cmd.Flags().BoolVar(&noDeps, "no-deps", false, "don't touch linked services")
	cmd.Flags().BoolVar(&adopt, "adopt", false, "remove a container started by hand that holds a container_name the compose file wants (docker rm -f, NOT recoverable: its writable layer and anonymous volumes are lost; refused if it uses a named volume the compose file does not declare external)")
	return cmd
}

func newStackPsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ps <name>",
		Short: "Show the stack's containers",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStackOp(args[0], "stack ps", fmt.Sprintf("querying stack %s", args[0]),
				func(c sshx.Runner, cs compose.Stack) (string, error) { return compose.Ps(c, cs) })
		},
	}
}

func newStackPullCmd() *cobra.Command {
	var svcs []string
	cmd := &cobra.Command{
		Use:   "pull <name>",
		Short: "Pull the stack's images",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStackOp(args[0], "stack pull", fmt.Sprintf("pulling images for %s", args[0]),
				func(c sshx.Runner, cs compose.Stack) (string, error) { return compose.Pull(c, cs, svcs) })
		},
	}
	cmd.Flags().StringSliceVar(&svcs, "service", nil, "limit to these services (comma-separated)")
	return cmd
}

func newStackLogsCmd() *cobra.Command {
	var svcs []string
	var tail int
	cmd := &cobra.Command{
		Use:   "logs <name>",
		Short: "Show recent logs for the stack",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStackOpDescribed(args[0], "stack logs", fmt.Sprintf("fetching logs for %s", args[0]),
				func(c sshx.Runner, cs compose.Stack) (opOutput, error) {
					r, err := compose.ReadLogs(c, cs, svcs, tail)
					if err != nil {
						return opOutput{}, err
					}
					return opOutput{text: r.Output, data: r, note: r.Note}, nil
				})
		},
	}
	cmd.Flags().StringSliceVar(&svcs, "service", nil, "limit to these services (comma-separated)")
	cmd.Flags().IntVar(&tail, "tail", 100, "lines per service")
	return cmd
}

// opOutput is what a stack op hands back: the text a human reads, the data the JSON envelope
// carries, and a note to print when there is no text to print.
type opOutput struct {
	text string
	data any
	note string
}

// bareOutput is the shape an op that says nothing about its own output emits, and the shape every
// one of them emitted before `stack logs` had more to say. Keep it byte-for-byte: it is the
// published envelope for ps, pull, deploy, backup and restore.
func bareOutput(out string, err error) (opOutput, error) {
	return opOutput{text: out, data: map[string]string{"output": out}}, err
}

// runOnVM dials a stack's VM and runs fn against it.
//
// A variable, not a plain function, because the envelope a command emits is the contract the MAUI
// app and the MCP server read, and pinning that in a test means running the command with no host to
// dial. Tests replace this; nothing else should.
var runOnVM = func(vm *fleet.VM, fn func(c sshx.Runner) (opOutput, error)) (opOutput, error) {
	c, err := sshx.Dial(vm.Target())
	if err != nil {
		return opOutput{}, err
	}
	defer c.Close()
	return fn(c)
}

// runStackOp resolves a stack, dials its VM, runs the op with a spinner, and
// prints the compose output (human) or wraps it in the result (JSON).
func runStackOp(name, action, step string, fn func(c sshx.Runner, cs compose.Stack) (string, error)) error {
	return runStackOpDescribed(name, action, step, func(c sshx.Runner, cs compose.Stack) (opOutput, error) {
		return bareOutput(fn(c, cs))
	})
}

// runStackOpDescribed is runStackOp for an op that describes its own output instead of handing back
// a bare string.
func runStackOpDescribed(name, action, step string, fn func(c sshx.Runner, cs compose.Stack) (opOutput, error)) error {
	store, err := fleet.Load()
	if err != nil {
		return err
	}
	st := store.FindStack(name)
	if st == nil {
		return fmt.Errorf("no stack named %q: register it with `baryovm stack add %s --vm ... --path ...`", name, name)
	}
	vm := store.Find(st.VM)
	if vm == nil {
		return fmt.Errorf("stack %q references unknown VM %q", name, st.VM)
	}
	if err := requireLinux(vm, action); err != nil {
		return err
	}

	var o opOutput
	err = ui.Step(step, func() error {
		var err error
		o, err = runOnVM(vm, func(c sshx.Runner) (opOutput, error) {
			return fn(c, composeStack(st))
		})
		return err
	})
	if err != nil {
		ui.Emit(ui.Result{OK: false, Action: action, Error: err.Error()})
		return err
	}
	emitOp(name, action, o)
	return nil
}

// emitOp renders one op's result: the envelope in JSON mode, the output otherwise, with the note
// standing in when there is no output to show.
func emitOp(name, action string, o opOutput) {
	if ui.JSON() {
		ui.Emit(ui.Result{OK: true, Action: action, Message: name, Data: o.data})
		return
	}
	if trimmed := strings.TrimRight(o.text, "\n"); trimmed != "" {
		fmt.Println(trimmed)
	} else if o.note != "" {
		ui.Notef("%s", o.note)
	}
	ui.Successf("%s: %s done", name, action)
}
