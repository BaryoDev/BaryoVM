// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package scan

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os/exec"
	"time"
)

// asExitError unwraps err into an *exec.ExitError, reporting whether it was one.
// Kept in one place so run.go reads cleanly.
func asExitError(err error, target **exec.ExitError) bool {
	return errors.As(err, target)
}

// Run performs a scan: it runs ZAP through the Runner and parses the report.
// The context bounds the whole run; a nil ctx uses a fresh timeout from the
// options. It returns the Report and any error from running (not from what the
// scan found; findings live in the Report).
func Run(ctx context.Context, r Runner, o Options) (Report, error) {
	if o.URL == "" {
		return Report{}, errors.New("scan: no target URL")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	o.containerName = newContainerName()
	// The container enforces the timeout itself; this deadline is a backstop
	// with a little room so the in-container kill fires first.
	ctx, cancel := context.WithTimeout(ctx, time.Duration(o.timeoutSeconds()+cleanupGrace)*time.Second)
	defer cancel()

	raw, err := r.Run(ctx, command(o))
	if err != nil {
		if ctx.Err() != nil {
			removeContainer(r, o.containerName)
		}
		return Report{}, err
	}
	return ParseReport(o.URL, raw)
}

// cleanupGrace is how long past the in-container timeout the Go deadline waits.
const cleanupGrace = 15

func newContainerName() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "baryovm-scan-" + hex.EncodeToString(b)
}

// removeContainer is best effort: the container may already be gone, and the
// original error is the one worth reporting. It uses a fresh context because
// the scan's is already done.
func removeContainer(r Runner, name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, _ = r.Run(ctx, []string{"docker", "rm", "-f", name})
}
