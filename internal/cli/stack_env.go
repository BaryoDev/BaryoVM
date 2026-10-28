// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/compose"
	"github.com/BaryoDev/BaryoVM/internal/envfile"
	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"github.com/BaryoDev/BaryoVM/internal/sshx"
	"github.com/BaryoDev/BaryoVM/internal/ui"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func newStackEnvCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "env",
		Short: "Set, list and unset keys in a stack's .env without ssh",
		Long: strings.TrimSpace(`
Manage the env file of a registered stack on its VM. A value is never an argument: set prompts for
it with no echo, or reads one line from stdin when piped, and sends it over the ssh session's stdin.
No value appears on a command line, locally or on the VM, or in the output.

The file is the stack's --env-file (default .env) under its directory, written at mode 600, and
created with its directory if missing. Compose reads .env only when containers start, so pass
--recreate to run "compose up -d --force-recreate" after a change. The value is stored as typed,
as KEY=value on one line, and compose then reads it by its own rules for quotes and "$".
`),
	}
	cmd.AddCommand(newStackEnvSetCmd(), newStackEnvListCmd(), newStackEnvUnsetCmd())
	return cmd
}

func newStackEnvSetCmd() *cobra.Command {
	var recreate bool
	cmd := &cobra.Command{
		Use:   "set <name> <KEY>",
		Short: "Set KEY in the stack's env file (value from a no-echo prompt or stdin)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, key := args[0], args[1]
			if err := envfile.ValidKey(key); err != nil {
				return emitEnvFailure("stack env set", err)
			}
			value, err := readEnvValue(cmd, key)
			if err == nil {
				err = envfile.ValidValue(value)
			}
			if err != nil {
				return emitEnvFailure("stack env set", err)
			}
			return runEnvOp(name, "stack env set", "setting "+key, func(c sshx.Runner, st *fleet.Stack, file string) (opOutput, error) {
				r, ok := c.(envfile.Runner)
				if !ok {
					return opOutput{}, fmt.Errorf("this connection cannot send input to the VM")
				}
				if err := envfile.Set(r, file, key, value, st.Sudo); err != nil {
					return opOutput{}, err
				}
				return finishEnvWrite(c, st, name, key, recreate)
			})
		},
	}
	cmd.Flags().BoolVar(&recreate, "recreate", false, "run compose up -d --force-recreate after the write")
	return cmd
}

func newStackEnvUnsetCmd() *cobra.Command {
	var recreate bool
	cmd := &cobra.Command{
		Use:   "unset <name> <KEY>",
		Short: "Remove KEY from the stack's env file",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, key := args[0], args[1]
			if err := envfile.ValidKey(key); err != nil {
				return emitEnvFailure("stack env unset", err)
			}
			return runEnvOp(name, "stack env unset", "removing "+key, func(c sshx.Runner, st *fleet.Stack, file string) (opOutput, error) {
				if err := envfile.Unset(c, file, key, st.Sudo); err != nil {
					return opOutput{}, err
				}
				return finishEnvWrite(c, st, name, key, recreate)
			})
		},
	}
	cmd.Flags().BoolVar(&recreate, "recreate", false, "run compose up -d --force-recreate after the write")
	return cmd
}

func newStackEnvListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list <name>",
		Short: "List the keys in the stack's env file, values masked",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			return runEnvOp(name, "stack env list", "reading keys of "+name, func(c sshx.Runner, st *fleet.Stack, file string) (opOutput, error) {
				keys, err := envfile.List(c, file, st.Sudo)
				if err != nil {
					return opOutput{}, err
				}
				if keys == nil {
					keys = []string{}
				}
				var lines []string
				for _, k := range keys {
					lines = append(lines, k+"=********")
				}
				return opOutput{
					text: strings.Join(lines, "\n"),
					data: map[string]any{"stack": name, "keys": keys},
					note: "no keys set",
				}, nil
			})
		},
	}
}

// finishEnvWrite recreates the containers when asked, since compose reads .env only at start.
func finishEnvWrite(c sshx.Runner, st *fleet.Stack, name, key string, recreate bool) (opOutput, error) {
	if recreate {
		if _, err := compose.Up(c, composeStack(st), compose.UpOptions{ForceRecreate: true}); err != nil {
			return opOutput{}, fmt.Errorf("%s was written but the recreate failed: %w", key, err)
		}
	}
	return opOutput{data: map[string]any{"stack": name, "key": key, "recreated": recreate}}, nil
}

// runEnvOp resolves a stack and its env file path, then runs fn against its VM.
func runEnvOp(name, action, step string, fn func(c sshx.Runner, st *fleet.Stack, file string) (opOutput, error)) error {
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
	file := envfile.Path(st.Dir, st.EnvFile)
	var o opOutput
	err = ui.Step(step, func() error {
		var err error
		o, err = runOnVM(vm, func(c sshx.Runner) (opOutput, error) { return fn(c, st, file) })
		return err
	})
	if err != nil {
		ui.Emit(ui.Result{OK: false, Action: action, Error: err.Error()})
		return err
	}
	emitOp(name, action, o)
	return nil
}

func emitEnvFailure(action string, err error) error {
	ui.Emit(ui.Result{OK: false, Action: action, Error: err.Error()})
	return err
}

// readEnvValue takes the value from a no-echo prompt on a terminal, or one line from piped stdin.
// Never from an argument.
func readEnvValue(cmd *cobra.Command, key string) (string, error) {
	in := cmd.InOrStdin()
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprintf(os.Stderr, "Value for %s (input hidden): ", key)
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", fmt.Errorf("read value: %w", err)
		}
		return string(b), nil
	}
	b, err := io.ReadAll(in)
	if err != nil {
		return "", fmt.Errorf("read value from stdin: %w", err)
	}
	if len(b) == 0 {
		return "", fmt.Errorf("no value on stdin: pipe it in, for example `printf %%s \"$VALUE\" | baryovm stack env set <name> %s`", key)
	}
	v := strings.TrimSuffix(string(b), "\n")
	v = strings.TrimSuffix(v, "\r")
	return v, nil
}
