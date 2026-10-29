// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/compose"
	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"github.com/BaryoDev/BaryoVM/internal/remote"
	"github.com/BaryoDev/BaryoVM/internal/sshx"
	"github.com/BaryoDev/BaryoVM/internal/ui"
	"github.com/BaryoDev/BaryoVM/internal/update"
	"github.com/spf13/cobra"
)

// errForcedVM is the refusal for any command that would send shell to a VM behind a forced command.
// Sending it anyway is the bug #98 describes: the VM runs its own script and the command looks done.
func errForcedVM(name string) error {
	return fmt.Errorf("VM %q is marked remote=forced, so its SSH key only runs `baryovm remote serve`: "+
		"only `stack update`, `stack backup`, `stack ps` and `stack logs` work on it. "+
		"Use a key without a forced command for anything else, or clear the mark with `baryovm vm remote set %s off`", name, name)
}

// probed remembers the VMs already proven to run the commands they are sent, so the probe is one
// round trip per VM per process and not one per dial.
var probed = map[string]bool{}

// newNonce is replaced by tests.
var newNonce = remote.NewNonce

// verifyRuns proves r runs what it is sent (see remote.DetectSwallowed), once per key.
func verifyRuns(key string, r sshx.Runner) error {
	if probed[key] {
		return nil
	}
	if err := remote.DetectSwallowed(r, newNonce()); err != nil {
		return err
	}
	probed[key] = true
	return nil
}

// dialChecked is sshx.Dial for every command that sends shell. It refuses a VM marked forced, and
// on any other VM proves the command is really executed, so a key pinned with command="..." fails
// loudly instead of reporting success for a script baryovm never ran.
func dialChecked(vm *fleet.VM) (*sshx.Client, error) {
	if vm.Forced() {
		return nil, errForcedVM(vm.Name)
	}
	c, err := sshx.Dial(vm.Target())
	if err != nil {
		return nil, err
	}
	t := vm.Target()
	if err := verifyRuns(fmt.Sprintf("%s:%d:%s", t.Host, t.Port, t.User), c); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("%s: %w", vm.Name, err)
	}
	return c, nil
}

// callForced sends one request over r and returns the reply. A reply that is not a v1 answer to
// exactly this request is an error, and so is a refusal from the VM.
func callForced(r sshx.Runner, req remote.Request) (remote.Response, error) {
	// A refusal exits non-zero, and Run keeps stdout when that happens, so the reply is read first
	// and the exit status second.
	out, runErr := r.Run(req.String())
	resp, err := remote.ParseResponse(out, req)
	if err != nil {
		if runErr != nil {
			return remote.Response{}, fmt.Errorf("%w (%v)", err, runErr)
		}
		return remote.Response{}, err
	}
	if !resp.OK {
		return resp, fmt.Errorf("%s", resp.Error)
	}
	return resp, nil
}

// runForced dials a forced VM and calls it. A variable so tests can run the forced paths without a
// host.
var runForced = func(vm *fleet.VM, req remote.Request) (remote.Response, error) {
	c, err := sshx.Dial(vm.Target())
	if err != nil {
		return remote.Response{}, err
	}
	defer c.Close()
	return callForced(c, req)
}

// forcedOnly is the guard every other stack command meets: a forced VM answers four requests and
// nothing else.
func forcedOnly(vm *fleet.VM, action string) error {
	if !vm.Forced() {
		return nil
	}
	return fmt.Errorf("%s: %w", action, errForcedVM(vm.Name))
}

// refuseForcedFlags stops a flag the request cannot carry from being silently ignored on a forced
// VM. It does nothing for an ordinary VM or an unknown stack, which the command reports itself.
func refuseForcedFlags(stack, action, flags string) error {
	store, err := fleet.Load()
	if err != nil {
		return err
	}
	st := store.FindStack(stack)
	if st == nil {
		return nil
	}
	vm := store.Find(st.VM)
	if vm == nil || !vm.Forced() {
		return nil
	}
	err = fmt.Errorf("on a forced VM `%s` returns the fixed tail the VM allows: %s are not available", action, flags)
	ui.Emit(ui.Result{OK: false, Action: action, Message: stack, Error: err.Error()})
	return err
}

// forcedText is the human text of a reply: the "output" every op's data carries.
func forcedText(resp remote.Response) string {
	var d struct {
		Output string `json:"output"`
		Note   string `json:"note"`
	}
	_ = json.Unmarshal(resp.Data, &d)
	return d.Output
}

// forcedOp runs one op on a forced VM and renders it like the ordinary command does.
func forcedOp(vm *fleet.VM, stack, action string, op remote.Op) error {
	var resp remote.Response
	err := ui.Step(fmt.Sprintf("asking %s to %s %s", vm.Name, op, stack), func() error {
		var e error
		resp, e = runForced(vm, remote.Request{Op: op, Stack: stack})
		return e
	})
	if err != nil {
		ui.Emit(ui.Result{OK: false, Action: action, Message: stack, Error: err.Error()})
		return err
	}
	var note string
	if op == remote.OpLogs {
		var lr compose.LogsResult
		_ = json.Unmarshal(resp.Data, &lr)
		note = lr.Note
	}
	emitOp(stack, action, opOutput{text: forcedText(resp), data: json.RawMessage(resp.Data), note: note})
	return nil
}

// forcedUpdate runs `stack update` on a forced VM. Every flag that would change what the VM does is
// refused, because the request carries none of them: the VM's allowlist decides. --auto is accepted
// so a scheduled run can use --notify; it adds no check here, since the VM's allowlist holds the
// backup and health settings the update runs with.
func forcedUpdate(vm *fleet.VM, name string, svcs []string, dryRun, noBackup bool) (updateOutcome, error) {
	const action = "stack update"
	if len(svcs) > 0 || dryRun || noBackup {
		err := fmt.Errorf("on a forced VM `stack update` takes no flags but --auto: the VM's allowlist decides services, backups and health, and the request carries none of them")
		return updateOutcome{env: ui.Result{OK: false, Action: action, Message: name, Error: err.Error()}, have: true}, err
	}
	var resp remote.Response
	runErr := ui.Step(fmt.Sprintf("asking %s to update %s", vm.Name, name), func() error {
		var e error
		resp, e = runForced(vm, remote.Request{Op: remote.OpUpdate, Stack: name})
		return e
	})
	var res update.Result
	_ = json.Unmarshal(resp.Data, &res)
	return reportUpdate(action, name, res, runErr)
}

// --- commands -------------------------------------------------------------------------------

func newVMRemoteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remote",
		Short: "Run baryovm behind an SSH forced command (a restricted CI key)",
		Long: "A VM owner can pin a CI key in authorized_keys with command=\"...\", so the key can do one\n" +
			"thing and a leak cannot open a shell. baryovm normally sends shell, which such a key discards.\n" +
			"This mode installs `baryovm remote serve` as that forced command. It accepts only update,\n" +
			"backup, status and logs for the stacks listed in /etc/baryovm/remote.json on the VM.\n" +
			"exec and release are not available: exec is arbitrary shell, and release uploads files.",
	}
	cmd.AddCommand(newVMRemoteInstallCmd(), newVMRemoteSetCmd())
	return cmd
}

func newVMRemoteSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <vm> <forced|off>",
		Short: "Mark a VM as behind a forced command (or clear the mark)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := fleet.Load()
			if err != nil {
				return err
			}
			vm := store.Find(args[0])
			if vm == nil {
				return fmt.Errorf("no VM named %q: register it with `baryovm vm add %s --host ... --key ...`", args[0], args[0])
			}
			switch args[1] {
			case fleet.RemoteForced:
				vm.Remote = fleet.RemoteForced
			case "off":
				vm.Remote = ""
			default:
				return fmt.Errorf("mode must be %q or off, got %q", fleet.RemoteForced, args[1])
			}
			if err := store.Save(); err != nil {
				return err
			}
			ui.Emit(ui.Result{OK: true, Action: "vm remote set", Message: fmt.Sprintf("%s: remote=%q", vm.Name, vm.Remote), Data: vm})
			return nil
		},
	}
}

func newVMRemoteInstallCmd() *cobra.Command {
	var binary, pubKey string
	cmd := &cobra.Command{
		Use:   "install <vm>",
		Short: "Copy the wrapper to a VM and print the authorized_keys line to add",
		Long: "Run with an ordinary (unrestricted) key, once. Copies a linux baryovm binary to\n" +
			remote.InstallBinary + " (root-owned, so the SSH user cannot replace it), writes a template\n" +
			remote.DefaultConfigPath + " if there is none (it allows nothing until you edit it), and prints the\n" +
			"authorized_keys line. It never edits authorized_keys.\n\n" +
			"The binary is this one when it is a linux build for the VM's architecture, otherwise pass\n" +
			"--binary with a linux release binary.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRemoteInstall(args[0], binary, pubKey)
		},
	}
	cmd.Flags().StringVar(&binary, "binary", "", "linux baryovm binary to install (default: this binary, when it is linux and matches the VM)")
	cmd.Flags().StringVar(&pubKey, "public-key", "", "the CI key's public key line, to print a complete authorized_keys entry")
	return cmd
}

func runRemoteInstall(name, binary, pubKey string) error {
	const action = "vm remote install"
	fail := func(err error) error {
		ui.Emit(ui.Result{OK: false, Action: action, Message: name, Error: err.Error()})
		return err
	}
	vm, err := requireVM(name)
	if err != nil {
		return err
	}
	if err := requireLinux(&vm, action); err != nil {
		return err
	}
	if vm.Forced() {
		return fail(fmt.Errorf("%s is marked forced: install with an unrestricted key. Clear the mark first with `baryovm vm remote set %s off`", name, name))
	}

	var configState string
	err = ui.Step(fmt.Sprintf("installing the wrapper on %s", vm.Name), func() error {
		c, err := dialChecked(&vm)
		if err != nil {
			return err
		}
		defer c.Close()
		arch, err := c.Run("uname -m")
		if err != nil {
			return err
		}
		goarch := remote.UnameArch(strings.TrimSpace(arch))
		if goarch == "" {
			return fmt.Errorf("no release binary for architecture %q", strings.TrimSpace(arch))
		}
		src, err := chooseBinary(binary, goarch)
		if err != nil {
			return err
		}
		f, err := os.Open(src) // #nosec G304 -- the operator's own --binary path or this executable
		if err != nil {
			return err
		}
		defer f.Close()
		const tmpBin, tmpCfg = ".baryovm-remote.bin", ".baryovm-remote.json"
		if _, err := c.RunStdin(remote.UploadCmd(tmpBin), f); err != nil {
			return err
		}
		if _, err := c.RunStdin(remote.UploadCmd(tmpCfg), strings.NewReader(string(remote.TemplateConfig()))); err != nil {
			return err
		}
		if _, err := c.Run(remote.InstallBinaryCmd(tmpBin)); err != nil {
			return err
		}
		out, err := c.Run(remote.InstallConfigCmd(tmpCfg))
		configState = strings.TrimSpace(out)
		return err
	})
	if err != nil {
		return fail(err)
	}

	key := pubKey
	if key == "" {
		key = "<the CI key's public key>"
	}
	line := remote.AuthorizedKeysLine(key)
	next := []string{
		fmt.Sprintf("1. Edit %s on the VM: list each stack and the ops it allows (%s).", remote.DefaultConfigPath, opsText()),
		"2. Add this line to authorized_keys for the SSH user (baryovm does not edit that file):",
		"   " + line,
		fmt.Sprintf("3. On the CI machine: baryovm vm remote set %s forced", vm.Name),
	}
	if ui.JSON() {
		ui.Emit(ui.Result{OK: true, Action: action, Message: name, Data: map[string]any{
			"binary": remote.InstallBinary, "config": remote.DefaultConfigPath, "configState": configState,
			"authorizedKeysLine": line, "next": next,
		}})
		return nil
	}
	ui.Successf("%s: wrapper at %s, config %s (%s)", vm.Name, remote.InstallBinary, remote.DefaultConfigPath, configState)
	for _, l := range next {
		fmt.Println(l)
	}
	return nil
}

func opsText() string {
	s := make([]string, len(remote.Ops))
	for i, o := range remote.Ops {
		s[i] = string(o)
	}
	return strings.Join(s, ", ")
}

// chooseBinary picks the binary to copy. It never downloads: a linux build for the right
// architecture is either this process or a path the operator names.
func chooseBinary(flag, goarch string) (string, error) {
	if flag != "" {
		return flag, checkELF(flag)
	}
	if runtime.GOOS == "linux" && runtime.GOARCH == goarch {
		exe, err := os.Executable()
		if err != nil {
			return "", err
		}
		return filepath.Clean(exe), nil
	}
	return "", fmt.Errorf("this baryovm is %s/%s but the VM is linux/%s: pass --binary with a linux/%s release binary", runtime.GOOS, runtime.GOARCH, goarch, goarch)
}

// checkELF refuses a file that is not a linux executable, so a macOS binary is not installed as
// the forced command.
func checkELF(p string) error {
	f, err := os.Open(p) // #nosec G304 -- the binary chosen above, a local path the operator named
	if err != nil {
		return err
	}
	defer f.Close()
	magic := make([]byte, 4)
	if _, err := io.ReadFull(f, magic); err != nil || string(magic) != "\x7fELF" {
		return fmt.Errorf("%s is not a linux (ELF) binary", p)
	}
	return nil
}

// newRemoteServeCmd is the forced command. Hidden: nobody types it, sshd runs it.
func newRemoteGroupCmd() *cobra.Command {
	grp := &cobra.Command{Use: "remote", Short: "VM side of forced-command mode", Hidden: true}
	grp.AddCommand(newRemoteServeCmd())
	return grp
}

func newRemoteServeCmd() *cobra.Command {
	var cfgPath string
	cmd := &cobra.Command{
		Use:    "serve",
		Short:  "Answer one baryovm-remote request from SSH_ORIGINAL_COMMAND (the forced command)",
		Hidden: true,
		Args:   cobra.NoArgs,
		// Replaces the root's hook: serve touches no fleet and always answers in JSON.
		PersistentPreRun: func(cmd *cobra.Command, args []string) { ui.SetJSON(true) },
		RunE: func(cmd *cobra.Command, args []string) error {
			if cfgPath == "" {
				cfgPath = remote.DefaultConfigPath
			}
			s := remote.Server{
				Command:    os.Getenv("SSH_ORIGINAL_COMMAND"),
				ConfigPath: cfgPath,
				FS:         remote.OSFS{},
				Exec:       remote.RunnerExecutor{R: remote.LocalRunner{}},
			}
			resp := s.Serve()
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(resp)
			if !resp.OK {
				return &exitError{code: 1, msg: resp.Error, silent: true}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "allowlist file (default "+remote.DefaultConfigPath+")")
	return cmd
}
