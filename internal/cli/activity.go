// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/BaryoDev/BaryoVM/internal/activity"
	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"github.com/spf13/cobra"
	"golang.org/x/crypto/ssh"
)

// ranCommand is the command that actually started, with its positional args. Help, usage errors and
// unknown commands never reach PersistentPreRun, so they are never set here.
type ranCommand struct {
	cmd    *cobra.Command
	args   []string
	before *fleet.Store
}

var lastRun *ranCommand

// runRoot executes root and then appends one activity line for the command that ran. A failure to
// write the line prints one warning and never changes the returned error.
func runRoot(root *cobra.Command) error {
	lastRun = nil
	start := time.Now()
	_, err := root.ExecuteC()
	if run := lastRun; run != nil {
		if e, ok := activityEntry(run, err, time.Since(start)); ok {
			if werr := activity.Record(e); werr != nil {
				fmt.Fprintf(os.Stderr, "! could not write the activity log: %v\n", werr)
			}
		}
	}
	return err
}

func activityEntry(run *ranCommand, err error, took time.Duration) (activity.Entry, bool) {
	path := strings.TrimPrefix(run.cmd.CommandPath(), run.cmd.Root().Name()+" ")
	if !recordedAction(path) {
		return activity.Entry{}, false
	}
	e := activity.Entry{
		Version:    Version,
		Action:     path,
		OK:         err == nil,
		DurationMS: took.Milliseconds(),
	}
	if err != nil {
		e.Error = activityError(err)
	}
	name := activityName(run)
	if path == "vm exec" && name == "" {
		return activity.Entry{}, false
	}
	after, _ := fleet.Load()
	for _, s := range []*fleet.Store{run.before, after} {
		if s == nil || name == "" {
			continue
		}
		if strings.HasPrefix(path, "stack ") {
			if st := s.FindStack(name); st != nil {
				e.Stack, e.VM = name, st.VM
				break
			}
		} else if s.Find(name) != nil {
			e.VM = name
			break
		}
	}
	return e, true
}

func recordedAction(path string) bool {
	switch path {
	case "vm list", "stack list", "version", "help", "docs":
		return false
	}
	first, _, _ := strings.Cut(path, " ")
	return first != "completion" && !strings.HasPrefix(first, "__")
}

// activityError is a fixed string chosen here, never the error's own text. Error text carries the
// remote command, its stderr and user input, any of which can hold a secret.
func activityError(err error) string {
	var ee *exitError
	if errors.As(err, &ee) {
		return fmt.Sprintf("remote command failed (exit %d)", ee.code)
	}
	var se *ssh.ExitError
	if errors.As(err, &se) {
		return fmt.Sprintf("remote command failed (exit %d)", se.ExitStatus())
	}
	return "failed"
}

// activityName is the VM or stack the command was pointed at: the first positional argument of a
// command whose usage is `<name>`. Flag values are never read.
func activityName(run *ranCommand) string {
	if !strings.Contains(run.cmd.Use, "<name") {
		return ""
	}
	if run.cmd.DisableFlagParsing {
		return execName(run.args)
	}
	if len(run.args) > 0 {
		return run.args[0]
	}
	return ""
}

// execName finds the VM in the raw args of a command that parses its own flags: the first word
// before `--` that is not a flag or the value of -o.
func execName(args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			return ""
		case a == "-o" || a == "--output":
			i++
		case strings.HasPrefix(a, "-"):
		default:
			return a
		}
	}
	return ""
}
