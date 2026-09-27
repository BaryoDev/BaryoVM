// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"context"
	"fmt"

	"github.com/BaryoDev/BaryoVM/internal/scan"
	"github.com/BaryoDev/BaryoVM/internal/ui"
	"github.com/spf13/cobra"
)

// newScanCmd runs an OWASP ZAP baseline (passive) scan against a URL and
// reports the findings. It is meant to gate a release: pipe it before promoting
// to production, or run it in CI where the runner already has Docker.
//
// The baseline scan sends no attack payloads. It spiders the target and reports
// what passive analysis sees. Scan only sites you are authorised to reach.
func newScanCmd() *cobra.Command {
	var (
		url     string
		failOn  string
		timeout int
	)
	cmd := &cobra.Command{
		Use:   "scan --url <target>",
		Short: "Run an OWASP ZAP baseline (passive) scan against a URL",
		Long: "Runs the OWASP ZAP baseline scan (passive: no attack payloads) against a\n" +
			"deployed site and reports findings worst-first. With --fail-on, it exits\n" +
			"non-zero when a finding is at or above that severity, so it can gate a\n" +
			"release in a pipeline. Needs Docker on the machine that runs it.\n\n" +
			"Scan only targets you are authorised to reach.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if url == "" {
				return fmt.Errorf("--url is required")
			}
			threshold, err := scan.ParseSeverity(failOn)
			if err != nil {
				return err
			}

			var rep scan.Report
			runErr := ui.Step(fmt.Sprintf("scanning %s", url), func() error {
				var e error
				rep, e = scan.Run(context.Background(), scan.LocalRunner{}, scan.Options{
					URL:            url,
					TimeoutSeconds: timeout,
				})
				return e
			})
			if runErr != nil {
				ui.Emit(ui.Result{OK: false, Action: "scan", Error: runErr.Error()})
				return runErr
			}

			renderScanFindings(rep)

			failed := rep.FailsAt(threshold)
			msg := fmt.Sprintf("%d findings (%s)", len(rep.Findings), countsSummary(rep))
			ui.Emit(ui.Result{
				OK:      !failed,
				Action:  "scan",
				Message: msg,
				Data:    rep,
				Error:   gateError(failed, failOn),
			})
			if failed {
				// A gate failure is an expected outcome, not a crash. Return an
				// error so cobra sets a non-zero exit; the root command sets
				// SilenceErrors, so it is not reprinted after the envelope.
				return fmt.Errorf("scan gate: %s", msg)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&url, "url", "", "target URL to scan (required)")
	cmd.Flags().StringVar(&failOn, "fail-on", "high",
		"exit non-zero when a finding is at or above this severity: high | medium | low | informational")
	cmd.Flags().IntVar(&timeout, "timeout", scan.DefaultTimeoutSeconds, "scan timeout in seconds")
	return cmd
}

// countsSummary renders the severity tally like "1 high, 2 medium".
func countsSummary(r scan.Report) string {
	order := []string{"high", "medium", "low", "informational"}
	var parts []string
	for _, s := range order {
		if n := r.Counts[s]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, s))
		}
	}
	if len(parts) == 0 {
		return "clean"
	}
	return joinComma(parts)
}

func gateError(failed bool, failOn string) string {
	if failed {
		return fmt.Sprintf("findings at or above %s", failOn)
	}
	return ""
}

// renderScanFindings prints the human table. In JSON mode ui.Step and this are
// silent, and the machine reads Data off the envelope instead.
func renderScanFindings(r scan.Report) {
	if ui.JSON() {
		return
	}
	if len(r.Findings) == 0 {
		ui.Successf("no findings")
		return
	}
	for _, f := range r.Findings {
		label := fmt.Sprintf("[%s] %s", f.Severity, f.Name)
		detail := fmt.Sprintf("%d instance(s)", f.Count)
		if f.CWE != "" {
			detail += " · " + f.CWE
		}
		ui.Notef("%s  %s", label, detail)
	}
}

func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}
