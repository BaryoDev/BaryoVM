// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package scan

import (
	"context"
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
	secs := o.TimeoutSeconds
	if secs <= 0 {
		secs = DefaultTimeoutSeconds
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(secs)*time.Second)
	defer cancel()

	raw, err := r.Run(ctx, command(o))
	if err != nil {
		return Report{}, err
	}
	return ParseReport(o.URL, raw)
}
