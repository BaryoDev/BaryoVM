// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package site

import (
	"fmt"
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/sshx"
)

// Finish is what follows the file transfer of `site deploy`: relabel the webroot where SELinux is
// on, test the nginx config, reload. It returns the steps it completed. The sync itself is not
// here, it is the release code's rsync, run by the cli.
func Finish(r sshx.Runner, root string, step StepFunc) ([]string, error) {
	if err := ValidateRoot(root); err != nil {
		return nil, err
	}
	done := []string{}
	run := func(cmd string) (string, error) {
		out, err := r.Run(cmd)
		if err != nil {
			return out, fmt.Errorf("%w: %s", err, tail(out))
		}
		return out, nil
	}
	do := func(name string, fn func() error) error {
		if err := step(name, fn); err != nil {
			return &deployError{Step: name, Err: err}
		}
		done = append(done, name)
		return nil
	}

	selinux := false
	if err := do(StepInspect, func() error {
		out, e := run(ProbeSELinuxCmd())
		selinux = strings.TrimSpace(out) == "selinux"
		return e
	}); err != nil {
		return done, err
	}
	if selinux {
		if err := do(StepRestorecon, func() error { _, e := run(RestoreconCmd(root)); return e }); err != nil {
			return done, err
		}
	}
	if err := do("nginx -t", func() error { _, e := run(TestCmd()); return e }); err != nil {
		return done, err
	}
	if err := do(StepReload, func() error { _, e := run(ReloadCmd()); return e }); err != nil {
		return done, err
	}
	return done, nil
}

type deployError struct {
	Step string
	Err  error
}

func (e *deployError) Error() string {
	return "site deploy failed at step \"" + e.Step + "\": " + e.Err.Error()
}
func (e *deployError) Unwrap() error { return e.Err }
