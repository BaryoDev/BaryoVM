// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/BaryoDev/BaryoVM/internal/activity"
	"github.com/BaryoDev/BaryoVM/internal/guard"
	"github.com/BaryoDev/BaryoVM/internal/ui"
	"github.com/spf13/cobra"
)

// guardRunner and passthrough are the two seams to the real tools. Tests replace both, so no test
// runs git or gh against a remote.
var (
	guardRunner guard.Runner = osRunner{}
	passthrough              = runReal
)

type osRunner struct{}

func (osRunner) Git(globals []string, args ...string) (string, error) {
	cmd := exec.Command("git", append(append([]string{}, globals...), args...)...) // #nosec G204 -- fixed git binary; arguments come from the command line the user typed
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return string(out), &gitFailure{msg: msg, err: err}
		}
		return string(out), err
	}
	return string(out), nil
}

// gitFailure carries git's stderr as the message and keeps the underlying error, so the exit code
// stays reachable with errors.As.
type gitFailure struct {
	msg string
	err error
}

func (e *gitFailure) Error() string { return e.msg }
func (e *gitFailure) Unwrap() error { return e.err }

func (osRunner) ReadFile(p string) ([]byte, error) { return os.ReadFile(p) } // #nosec G304 -- path is a file the user named on the gh command line

// runReal runs the real tool with this process's stdin, stdout and stderr, and returns its exit
// code, so output and exit codes pass through unchanged.
func runReal(name string, args []string) (int, error) {
	cmd := exec.Command(name, args...) // #nosec G204 -- name is "git" or "gh", set by the wrapper commands
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), nil
	}
	if err != nil {
		return 0, fmt.Errorf("cannot run %s: %v. Install it and make sure it is on PATH", name, err)
	}
	return 0, nil
}

// loadPolicy reads the allowed hosts from git config and BARYOVM_ALLOWED_HOSTS. git config exits 1
// when the key is not set, which means no policy and no host restriction. Any other failure (a
// broken config file, git missing) is returned: reading the policy wrong must not turn into
// "everything is allowed".
func loadPolicy(globals []string) (guard.Policy, error) {
	values, err := gitConfigValues(globals, guard.PolicyKey, "BARYOVM_ALLOWED_HOSTS")
	if err != nil {
		return guard.Policy{}, err
	}
	pol := guard.ParsePolicy(values)
	aliases, err := gitConfigValues(globals, guard.ShellAliasKey, "BARYOVM_ALLOWED_SHELL_ALIASES")
	if err != nil {
		return guard.Policy{}, err
	}
	for _, a := range aliases {
		pol.AllowShellAliases = append(pol.AllowShellAliases, strings.Fields(a)...)
	}
	return pol, nil
}

// loadRegistryPolicy reads the allowed registries from git config and BARYOVM_ALLOWED_REGISTRIES,
// with the same rule: unset means no restriction, an unreadable config refuses.
func loadRegistryPolicy(globals []string) (guard.Policy, error) {
	values, err := gitConfigValues(globals, guard.RegistryPolicyKey, "BARYOVM_ALLOWED_REGISTRIES")
	if err != nil {
		return guard.Policy{}, err
	}
	return guard.Policy{AllowedRegistries: guard.ParseRegistries(values)}, nil
}

func gitConfigValues(globals []string, key, envVar string) ([]string, error) {
	var values []string
	out, err := guardRunner.Git(globals, "config", "--get-all", key)
	if err == nil {
		values = append(values, strings.Split(out, "\n")...)
	} else {
		var coded interface{ ExitCode() int }
		if !errors.As(err, &coded) || coded.ExitCode() != 1 {
			return nil, fmt.Errorf("cannot read %s from git config (%v): fix the config or run inside a git repository", key, err)
		}
	}
	if env := os.Getenv(envVar); env != "" {
		values = append(values, env)
	}
	return values, nil
}

// policyRefusal is the verdict used when the policy cannot be read.
func policyRefusal(action string, err error) guard.Verdict {
	return guard.Verdict{Action: action, Reason: action + " refused: " + err.Error()}
}

// finishGuard turns a verdict into the command result. A refusal emits ui.Result with OK false and
// exits 1 without running the tool. An allowed command runs the real tool and returns its exit
// code, with nothing added to its output.
//
// The wrappers parse no flags of their own (git's -o and gh's -F would collide with ours), so
// JSON output comes from BARYOVM_OUTPUT=json instead of -o json.
func finishGuard(v guard.Verdict, tool string, args []string) error {
	if os.Getenv("BARYOVM_OUTPUT") == "json" {
		ui.SetJSON(true)
	}
	if v.Args != nil {
		args = v.Args // --confirm removed, --yes added after a confirmed delete
	}
	if !v.Allowed {
		auditGuard(v, false, "refused by "+ruleOrKeys(v), 0)
		ui.Emit(ui.Result{OK: false, Action: v.Action, Error: v.Reason, Data: v.Findings})
		return &exitError{code: 1, msg: v.Reason, silent: true}
	}
	start := time.Now()
	code, err := passthrough(tool, args)
	if err != nil {
		auditGuard(v, false, "could not run "+tool, time.Since(start))
		ui.Emit(ui.Result{OK: false, Action: v.Action, Error: err.Error()})
		return &exitError{code: 127, msg: err.Error(), silent: true}
	}
	if code != 0 {
		auditGuard(v, false, fmt.Sprintf("%s exited with status %d", tool, code), time.Since(start))
		return &exitError{code: code, msg: fmt.Sprintf("%s exited with status %d", tool, code), silent: true}
	}
	auditGuard(v, true, "", time.Since(start))
	return nil
}

func ruleOrKeys(v guard.Verdict) string {
	if v.Rule != "" {
		return v.Rule
	}
	return "key scan or host check"
}

// auditGuard records a guarded command in the activity log: the action, its target and the
// result. The error is a fixed string and the target went through guard.SafeTarget, so no secret
// value reaches the file.
func auditGuard(v guard.Verdict, ok bool, errText string, took time.Duration) {
	if !v.Audit {
		return
	}
	e := activity.Entry{Version: Version, Action: v.Action, Target: v.Target, OK: ok, DurationMS: took.Milliseconds(), Error: errText}
	if werr := activity.Record(e); werr != nil {
		fmt.Fprintf(os.Stderr, "! could not write the activity log: %v\n", werr)
	}
}

// guardEnv builds what the rules need from the terminal. A prompt is offered only on a terminal
// and never under -o json; anywhere else a destructive command needs --confirm.
var guardEnv = func(confirm string) guard.Env {
	return guard.Env{
		Interactive: ui.IsTTY() && !ui.JSON(),
		Confirm:     confirm,
		Prompt: func(q string) (string, error) {
			fmt.Fprint(os.Stderr, q)
			line, err := bufio.NewReader(os.Stdin).ReadString('\n')
			return strings.TrimSpace(line), err
		},
		Show: func(lines []string) {
			for _, l := range lines {
				fmt.Fprintln(os.Stderr, l)
			}
		},
		RunGate: func(script string) (int, error) {
			cmd := exec.Command("sh", "-c", script) // #nosec G204 -- the gate script is named in the repository's own git config
			cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stderr, os.Stderr
			err := cmd.Run()
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				return ee.ExitCode(), nil
			}
			return 0, err
		},
	}
}

// loadRules reads the rule settings from git config (one call) and BARYOVM_RULES_OFF. git config
// exits 1 when no key is set, which means every rule on with its defaults; any other failure is
// returned so an unreadable config never turns into "no rules".
func loadRules(globals []string) (guard.Rules, error) {
	var lines []string
	out, err := guardRunner.Git(globals, "config", "--get-regexp", `^baryovm\.`)
	if err == nil {
		lines = strings.Split(out, "\n")
	} else {
		var coded interface{ ExitCode() int }
		if !errors.As(err, &coded) || coded.ExitCode() != 1 {
			return guard.Rules{}, fmt.Errorf("cannot read the baryovm rules from git config (%v): fix the config or run inside a git repository", err)
		}
	}
	return guard.ParseRules(lines, os.Getenv("BARYOVM_RULES_OFF")), nil
}

func newGuardCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "guard",
		Short: "Key and remote checks that run before git and gh",
	}
	cmd.AddCommand(newGuardInstallHooksCmd(), newGuardCheckCmd())
	return cmd
}

func newGuardInstallHooksCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "install-hooks",
		Short: "Write pre-commit and pre-push hooks that run the same checks",
		Long: "Writes git pre-commit and pre-push hooks in this repository that call\n" +
			"`baryovm guard check`, so a commit or push made without `baryovm git` is\n" +
			"checked too. A hook that is not BaryoVM's is left alone unless --force, which\n" +
			"moves it to <name>.baryovm-old first. The hooks fail when baryovm is not on PATH.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			written, err := installHooks(force)
			if err != nil {
				ui.Emit(ui.Result{OK: false, Action: "guard install-hooks", Error: err.Error()})
				return &exitError{code: 1, msg: err.Error(), silent: true}
			}
			ui.Emit(ui.Result{OK: true, Action: "guard install-hooks", Message: "installed " + strings.Join(written, ", "), Data: written})
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "replace a hook that is not BaryoVM's, keeping it as <name>.baryovm-old")
	return cmd
}

func installHooks(force bool) ([]string, error) {
	out, err := guardRunner.Git(nil, "rev-parse", "--git-path", "hooks")
	if err != nil {
		return nil, fmt.Errorf("not inside a git repository: run this from the repository you want to guard")
	}
	dir := strings.TrimSpace(out)
	if !filepath.IsAbs(dir) {
		wd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		dir = filepath.Join(wd, dir)
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	// Check every hook before writing any, so a refusal leaves the repository as it was.
	for _, name := range guard.HookNames {
		path := filepath.Join(dir, name)
		if b, err := os.ReadFile(path); err == nil && !guard.IsOurHook(string(b)) && !force { // #nosec G304 -- hook path is the repository hooks dir plus a fixed name
			return nil, fmt.Errorf("%s already has a %s hook that is not BaryoVM's: rerun with --force to keep it as %s.baryovm-old", dir, name, name)
		}
	}
	var written []string
	for _, name := range guard.HookNames {
		path := filepath.Join(dir, name)
		if b, err := os.ReadFile(path); err == nil && !guard.IsOurHook(string(b)) { // #nosec G304 -- hook path is the repository hooks dir plus a fixed name
			if err := os.Rename(path, path+".baryovm-old"); err != nil {
				return nil, err
			}
		}
		if err := os.WriteFile(path, []byte(guard.HookScript(name)), 0o755); err != nil { // #nosec G306 -- git hooks must be executable
			return nil, err
		}
		if err := os.Chmod(path, 0o755); err != nil { // #nosec G302 -- git hooks must be executable; the umask may have stripped the bit
			return nil, err
		}
		written = append(written, name)
	}
	return written, nil
}

// newGuardCheckCmd is what the installed hooks call. It reads git's own hook arguments.
func newGuardCheckCmd() *cobra.Command {
	check := &cobra.Command{
		Use:   "check",
		Short: "Run the hook checks (called by the hooks install-hooks writes)",
	}
	check.AddCommand(&cobra.Command{
		Use:   "pre-commit",
		Short: "Scan the staged diff for keys",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if v := guard.CheckPreCommit(guardRunner); !v.Allowed {
				return hookResult(v)
			}
			ru, rerr := loadRules(nil)
			if rerr != nil {
				return hookResult(policyRefusal("pre-commit", rerr))
			}
			return hookResult(guard.CheckPreCommitFiles(guardRunner, ru))
		},
	}, &cobra.Command{
		Use:   "pre-push <remote> <url>",
		Short: "Scan the commits being pushed and check the remote host (reads git's ref lines on stdin)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			in, err := io.ReadAll(cmd.InOrStdin())
			if err != nil {
				return err
			}
			pol, perr := loadPolicy(nil)
			if perr != nil {
				return hookResult(policyRefusal("push", perr))
			}
			return hookResult(guard.CheckPrePush(guardRunner, args[0], args[1], string(in), pol))
		},
	}, &cobra.Command{
		Use:   "commit-msg <file>",
		Short: "Scan the commit message file for keys",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if v := guard.CheckCommitMsg(guardRunner, args[0]); !v.Allowed {
				return hookResult(v)
			}
			ru, rerr := loadRules(nil)
			if rerr != nil {
				return hookResult(policyRefusal("commit-msg", rerr))
			}
			return hookResult(guard.CheckCommitMsgText(guardRunner, args[0], ru))
		},
	})
	return check
}

// hookResult reports a hook verdict. Allowed emits nothing, so a clean commit stays quiet.
func hookResult(v guard.Verdict) error {
	if v.Allowed {
		return nil
	}
	ui.Emit(ui.Result{OK: false, Action: "guard check " + v.Action, Error: v.Reason, Data: v.Findings})
	return &exitError{code: 1, msg: v.Reason, silent: true}
}
