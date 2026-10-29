// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package cli is BaryoVM's command surface (cobra). The .NET MAUI app and the
// MCP server call these same commands with -o json.
package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"

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
		},
	}
	jsonHelp(root)
	root.PersistentFlags().StringVarP(&outputFormat, "output", "o", "human", "output format: human | json")
	root.PersistentFlags().BoolVar(&strictHostKeys, "strict-host-keys", false,
		"refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)")
	root.AddCommand(newVersionCmd(), newVMCmd(), newDeployCmd(), newDoctorCmd(), newUpCmd(), newStackCmd(), newScanCmd(), newGitCmd(), newGhCmd(), newPublishCmd(), newGuardCmd(), newSiteCmd())
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
		switch {
		case a == "--":
			return false
		case a == "-o" || a == "--output":
			if i+1 < len(args) && args[i+1] == "json" {
				return true
			}
		case a == "-ojson" || a == "-o=json" || a == "--output=json":
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
	ui.Emit(ui.Result{OK: false, Action: action, Error: err.Error()})
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
	defer func() {
		for _, l := range learned {
			host, fp, _ := strings.Cut(l, " ")
			ui.Warnf("learned the host key for %s: %s", host, fp)
			ui.Detail("verify it", hostkeys.Keyscan(host)+" | ssh-keygen -lf -")
		}
	}()

	if code := run(newRoot(), os.Args[1:]); code != 0 {
		os.Exit(code)
	}
}

// run is Execute without the process exit: the command, then the error report, then the exit code.
// Tests drive this, so they go through the same path a user does.
func run(root *cobra.Command, args []string) int {
	if err := execute(root, args); err != nil {
		return reportError(err)
	}
	return 0
}
