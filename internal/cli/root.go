// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package cli is BaryoVM's command surface (cobra). The .NET MAUI app and the
// MCP server call these same commands with -o json.
package cli

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"github.com/BaryoDev/BaryoVM/internal/hostkeys"
	"github.com/BaryoDev/BaryoVM/internal/sshx"
	"github.com/BaryoDev/BaryoVM/internal/ui"
	"github.com/spf13/cobra"
)

var outputFormat string
var strictHostKeys bool

// errDryRun signals that a command stopped early because --dry-run was set.
var errDryRun = errors.New("dry run")

func newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:           "baryovm",
		Short:         "Provision VMs and deploy apps to your own boxes",
		Long:          ui.AccentStyle.Render("BaryoVM") + " turns provision → install Docker → deploy into one command.\nCLI-first: the MAUI app and MCP server drive this same CLI (with -o json).",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			before, _ := fleet.Load()
			lastRun = &ranCommand{cmd: cmd, args: args, before: before}
			ui.SetJSON(outputFormat == "json")
			// Only apply the flag when it was actually given, so it overrides the environment
			// rather than a default false switching strict mode off for a CI job that set it.
			if cmd.Flags().Changed("strict-host-keys") {
				sshx.SetStrictHostKeys(strictHostKeys)
			}
			// A person can be asked about an unknown host only on a terminal and not in JSON mode.
			sshx.SetInteractive(ui.IsTTY() && !ui.JSON())
			sshx.SetOutputJSON(ui.JSON())
		},
	}
	jsonHelp(root)
	root.PersistentFlags().StringVarP(&outputFormat, "output", "o", "human", "output format: human | json")
	root.PersistentFlags().BoolVar(&strictHostKeys, "strict-host-keys", false,
		"always refuse an unknown host; =false learns its key on first use instead of asking (also BARYOVM_STRICT_HOST_KEYS=1 or 0). A project release never learns or asks, even with =false")
	root.AddCommand(newVersionCmd(), newVMCmd(), newDeployCmd(), newDoctorCmd(), newUpCmd(), newStackCmd(), newProjectCmd(), newScanCmd(), newGitCmd(), newGhCmd(), newPublishCmd(), newCloudCmd(), newGuardCmd(), newSiteCmd(), newRemoteGroupCmd(), newRepoCmd(), newStatusCmd(), newShipCmd(), newProveCmd())
	rejectUnknownSubcommands(root)
	return root
}

// rejectUnknownSubcommands makes every group (a command with subcommands and nothing to run itself)
// fail on a word it does not know. Cobra only does that for the root; below it, a group with no
// RunE treats the typo as an argument, prints help and exits 0, so a script running `vm health`
// reads success. A group given no argument still prints its help.
func rejectUnknownSubcommands(cmd *cobra.Command) {
	for _, sub := range cmd.Commands() {
		rejectUnknownSubcommands(sub)
	}
	if cmd.HasParent() && cmd.HasSubCommands() && !cmd.Runnable() {
		cmd.Args = func(c *cobra.Command, args []string) error {
			if len(args) > 0 {
				return fmt.Errorf("unknown command %q for %q", args[0], c.CommandPath())
			}
			return nil
		}
		cmd.RunE = func(c *cobra.Command, args []string) error { return c.Help() }
	}
}

// jsonHelp makes help output follow -o json. Under json the help text goes into the envelope
// (data.usage) instead of reaching stdout as bare text, so a group run with no subcommand, or any
// --help, still gives the caller one document.
func jsonHelp(root *cobra.Command) {
	plain := root.HelpFunc()
	root.SetHelpFunc(func(c *cobra.Command, args []string) {
		if !ui.JSON() {
			plain(c, args)
			return
		}
		prev := c.OutOrStdout()
		var buf bytes.Buffer
		c.SetOut(&buf)
		plain(c, args)
		c.SetOut(prev)
		ui.Emit(ui.Result{OK: true, Action: actionOf(c.CommandPath()), Data: map[string]string{"usage": buf.String()}})
	})
}

// actionOf turns a cobra command path ("baryovm stack ps") into the action name the envelopes
// use ("stack ps"). The root itself keeps the program name.
func actionOf(path string) string {
	if _, rest, ok := strings.Cut(path, " "); ok && rest != "" {
		return rest
	}
	return "baryovm"
}

// failedPath is the command path of the last run, for the envelope reportError writes when the
// handler did not write one itself. Empty until a run has started.
var failedPath string

// jsonRequested reports whether args ask for -o json (or -ojson, -o=json, --output json,
// --output=json). Cobra parses flags after the command is chosen, and a parse error stops it
// before PersistentPreRun, so the format has to be read from the raw arguments to answer a
// bad flag in JSON. Everything after a bare -- belongs to a remote command and is ignored.
func jsonRequested(args []string) bool {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--":
			return false
		case "-o", "--output":
			if i+1 < len(args) && args[i+1] == "json" {
				return true
			}
		case "-ojson", "-o=json", "--output=json":
			return true
		}
	}
	return false
}

// ownArgs returns the part of args that belongs to baryovm. A command with DisableFlagParsing
// (vm exec, git, gh, and any other that wraps a tool) owns every word after its name, and a
// -o json there is the wrapped tool's, so only the words before it count. Those commands get JSON
// from BARYOVM_OUTPUT=json, or parse their own prefix.
func ownArgs(root *cobra.Command, args []string) []string {
	cmd, _, err := root.Find(args)
	if err != nil || cmd == nil || !cmd.DisableFlagParsing {
		return args
	}
	words := len(strings.Fields(cmd.CommandPath())) - 1
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-o" || a == "--output":
			i++
		case strings.HasPrefix(a, "-"):
		default:
			if words--; words == 0 {
				return args[:i+1]
			}
		}
	}
	return args
}

// execute runs root against args and returns the command that ran with its error. It switches
// JSON on before cobra parses anything, so a bad flag is answered in JSON too.
func execute(root *cobra.Command, args []string) error {
	if jsonRequested(ownArgs(root, args)) {
		outputFormat = "json"
		ui.SetJSON(true)
	}
	sshx.SetOutputJSON(ui.JSON())
	root.SetArgs(args)
	return runRoot(root)
}

// reportError prints a failed run in the format asked for and returns the exit code. The output
// format is set here as well as in PersistentPreRun, because cobra rejects bad arguments before
// that hook runs and the error would otherwise come out as text under -o json.
//
// This is the fallback, not an addition: stdout carries one document, so when a handler already
// wrote its own (ui.Emit ignores every call after the first) nothing more is written, whether or
// not it marked the error silent. The action is the command that failed, not the program name.
func reportError(err error) int {
	code, silent := exitCodeOf(err)
	if silent {
		return code
	}
	if outputFormat == "json" && !ui.JSON() {
		ui.SetJSON(true)
	}
	action := "baryovm" // the human banner keeps the program name; only the document names the command
	if ui.JSON() {
		action = actionOf(failedPath)
	}
	ui.Emit(ui.Result{OK: false, Action: action, Error: err.Error(), Data: errData(err)})
	return code
}

// Execute runs the root command.
func Execute() {
	// Tell the operator what host identity was just trusted. sshx does not print (engine packages
	// never do), so the hook lives here.
	//
	// Collected and printed at the end rather than as it happens. Dialling occurs inside ui.Step,
	// whose spinner rewrites the line every 80ms, so a Detail printed mid-step is drawn and then
	// erased by the next frame. Tested against a real host: the key was learned and written and the
	// operator saw nothing, which is the one moment the fingerprint is worth checking.
	var learned []string
	sshx.OnLearnHostKey = func(host, fingerprint string) {
		learned = append(learned, host+" "+fingerprint)
	}
	sshx.ConfirmHostKey = confirmHostKey
	defer func() {
		for _, l := range learned {
			host, fp, _ := strings.Cut(l, " ")
			ui.Warnf("learned the host key for %s: %s", host, fp)
			ui.Detail("check it", "against a fingerprint from the cloud console or ssh-keygen -lf /etc/ssh/ssh_host_*_key.pub on the machine")
		}
	}()

	if code := run(newRoot(), os.Args[1:]); code != 0 {
		os.Exit(code)
	}
}

// errData is the structured part of a failed result, for the errors a program can act on. An unknown
// host carries the host, the fingerprint it offered and why it was refused, so an app can show the
// fingerprint, ask the person, and call again with --host-key-fingerprint. Other errors carry none.
func errData(err error) any {
	var u *hostkeys.ErrUnknownHost
	if !errors.As(err, &u) {
		return nil
	}
	reason := string(u.Reason)
	if reason == "" {
		reason = "no-terminal"
	}
	d := map[string]any{"host": u.Host, "offered": u.Offered, "reason": reason, "store": u.Store}
	if u.Expected != "" {
		d["expected"] = u.Expected
	}
	return d
}

// hostKeyPromptMu keeps two prompts apart when a fleet command dials several VMs at once.
var hostKeyPromptMu sync.Mutex

// confirmHostKey asks the person whether to trust an unknown host's key. Only y or yes trusts; an
// empty answer, EOF or a read error does not. The spinner is paused around it, because the dial
// usually runs inside ui.Step and the spinner would erase the prompt.
func confirmHostKey(host, fingerprint string) bool {
	hostKeyPromptMu.Lock()
	defer hostKeyPromptMu.Unlock()
	var ok bool
	ui.Pause(func() {
		ui.Warnf("%s has no recorded host key", host)
		ui.Detail("offered", fingerprint)
		ui.Detail("check it", "against a fingerprint from the cloud console, the provider's serial console, or ssh-keygen -lf /etc/ssh/ssh_host_*_key.pub on the machine")
		ui.Detail("not a check", "a keyscan of this address, a man in the middle answers it too")
		ok = askYesNo(bufio.NewReader(os.Stdin), "Trust this host key? [y/N] ")
	})
	return ok
}

// run is Execute without the process exit: the command, then the error report, then the exit code.
// Tests drive this, so they go through the same path a user does.
func run(root *cobra.Command, args []string) int {
	if err := execute(root, args); err != nil {
		return reportError(err)
	}
	return 0
}
