// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package scan

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// zapImage is the official ZAP image. Pinned to the stable tag rather than
// latest so a scan is reproducible and a new ZAP release cannot change a gate's
// result without a deliberate bump here.
const zapImage = "zaproxy/zap-stable"

// DefaultTimeoutSeconds bounds the whole scan. A baseline spider-and-passive
// pass on a normal site finishes well inside this; the cap stops a hung target
// from wedging a CI job.
const DefaultTimeoutSeconds = 600

// Options controls one scan run.
type Options struct {
	// URL is the target. It must be a site you are authorised to reach; the
	// baseline scan is passive but it still fetches every page it can find.
	URL string
	// TimeoutSeconds bounds the run. Zero uses DefaultTimeoutSeconds.
	TimeoutSeconds int
}

// command builds the ZAP baseline command line. It runs the container, prints
// the JSON report to stdout (-J /dev/stdout would mix with ZAP's own logs, so
// we write to a file in the container and cat it), and always exits 0 from the
// container's view via -I so ZAP's own warn-exit does not mask our parse; the
// gate decision is ours, made from the parsed report, not ZAP's exit code.
//
// It is a package function, not a method, so a test can assert the exact
// argument vector without Docker present.
func command(o Options) []string {
	return []string{
		// zap-baseline.py refuses file reports unless /zap/wrk exists, so give
		// it a throwaway tmpfs rather than a host mount.
		"docker", "run", "--rm", "--tmpfs", "/zap/wrk:rw,mode=1777", zapImage,
		"sh", "-c",
		fmt.Sprintf(
			"zap-baseline.py -t %s -J zap.json -I >/dev/null 2>&1; cat /zap/wrk/zap.json",
			shellArg(o.URL),
		),
	}
}

// shellArg single-quotes an argument for the container's `sh -c`. The URL is
// operator-supplied, and a scan target with a shell metacharacter in it is
// almost certainly a mistake, but quoting keeps a stray character from being
// interpreted by the shell inside the container.
func shellArg(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Runner executes the scan command and returns the raw report bytes. Local
// (os/exec) and remote (SSH) both satisfy it, so Run stays transport-agnostic
// and testable with a fake.
type Runner interface {
	// Run executes argv and returns its combined stdout. A non-nil error means
	// the command could not run (Docker missing, host unreachable); a scan that
	// runs and finds problems is not an error here, it is a Report.
	Run(ctx context.Context, argv []string) ([]byte, error)
}

// LocalRunner runs the scan on the machine calling BaryoVM, for CI where the
// runner already has Docker and no SSH hop is needed.
type LocalRunner struct{}

// Run shells out to docker on the local machine.
func (LocalRunner) Run(ctx context.Context, argv []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	out, err := cmd.Output()
	if err != nil {
		return nil, wrapExec(err)
	}
	return out, nil
}

// wrapExec turns an exec error into something a reader can act on, surfacing
// docker's stderr when present.
func wrapExec(err error) error {
	var ee *exec.ExitError
	if ok := asExitError(err, &ee); ok && len(ee.Stderr) > 0 {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(ee.Stderr)))
	}
	return err
}
