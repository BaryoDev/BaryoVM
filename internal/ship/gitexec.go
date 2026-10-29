// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package ship

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Git is the seam to the git binary. Run returns stdout; a non-zero exit is an error.
type Git interface {
	Run(ctx context.Context, dir string, args ...string) (string, error)
}

// ExecGit runs the real git. Arguments are passed as an argv, never through a shell.
type ExecGit struct{}

func (ExecGit) Run(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...) // #nosec G204 -- fixed binary, arguments built by this package
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return string(out), fmt.Errorf("git %s: %w", args[0], ctx.Err())
		}
		if m := strings.TrimSpace(stderr.String()); m != "" {
			return string(out), fmt.Errorf("git %s: %s", args[0], redact(m))
		}
		return string(out), fmt.Errorf("git %s: %w", args[0], err)
	}
	return string(out), nil
}
