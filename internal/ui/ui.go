// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package ui renders BaryoVM's colorful terminal output and loading spinners.
// It mirrors the baryo-cli look: a lipgloss adaptive palette (same 256-color
// codes) plus the braille dot spinner from bubbles. When JSON output is on,
// all animation and color is suppressed so MAUI/agents get clean structured data.
package ui

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
)

// adaptive picks the right shade for light vs dark terminals — same codes as baryo-cli.
func adaptive(light, dark string) lipgloss.AdaptiveColor {
	return lipgloss.AdaptiveColor{Light: light, Dark: dark}
}

var (
	AccentStyle  = lipgloss.NewStyle().Bold(true).Foreground(adaptive("33", "75"))
	SuccessStyle = lipgloss.NewStyle().Foreground(adaptive("28", "108"))
	ErrorStyle   = lipgloss.NewStyle().Bold(true).Foreground(adaptive("160", "167"))
	WarnStyle    = lipgloss.NewStyle().Foreground(adaptive("172", "179"))
	PurpleStyle  = lipgloss.NewStyle().Foreground(adaptive("55", "183"))
	DimStyle     = lipgloss.NewStyle().Foreground(adaptive("242", "243"))
	ShellStyle   = lipgloss.NewStyle().Bold(true).Foreground(adaptive("208", "214"))
)

var jsonMode bool

// SetJSON switches to machine-readable output (no color, no spinners).
func SetJSON(on bool) { jsonMode = on }

// JSON reports whether machine-readable mode is active.
func JSON() bool { return jsonMode }

func isTTY() bool { return term.IsTerminal(int(os.Stderr.Fd())) }

// Title prints a bold accent banner for a command.
func Title(s string) {
	if jsonMode {
		return
	}
	fmt.Fprintln(os.Stderr, AccentStyle.Render("▸ "+s))
}

// Detail prints a dim key/value line, indented under a title.
func Detail(label, value string) {
	if jsonMode {
		return
	}
	fmt.Fprintf(os.Stderr, "  %s %s\n", DimStyle.Render(label+":"), value)
}

// Successf prints a green check line.
func Successf(format string, a ...any) {
	if jsonMode {
		return
	}
	fmt.Fprintf(os.Stderr, "%s %s\n", SuccessStyle.Render("✓"), fmt.Sprintf(format, a...))
}

// Warnf prints an orange warning line.
func Warnf(format string, a ...any) {
	if jsonMode {
		return
	}
	fmt.Fprintf(os.Stderr, "%s %s\n", WarnStyle.Render("!"), fmt.Sprintf(format, a...))
}

// Errorf prints a red error line.
func Errorf(format string, a ...any) {
	if jsonMode {
		return
	}
	fmt.Fprintf(os.Stderr, "%s %s\n", ErrorStyle.Render("✗"), fmt.Sprintf(format, a...))
}

// Result is the envelope every command emits in JSON mode so MAUI and agents
// can parse outcomes instead of scraping human text.
type Result struct {
	OK      bool   `json:"ok"`
	Action  string `json:"action"`
	Message string `json:"message,omitempty"`
	Data    any    `json:"data,omitempty"`
	Error   string `json:"error,omitempty"`
}

// Emit writes the final result: JSON to stdout in machine mode, or a styled
// banner to stderr in human mode (the step spinners already told the story).
func Emit(r Result) {
	if jsonMode {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(r)
		return
	}
	if r.OK {
		Successf("%s", r.Message)
	} else {
		Errorf("%s: %s", r.Action, r.Error)
	}
}

var dotFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// Step runs fn while showing a dot spinner (baryo-cli's spinner.Dot), then
// replaces the line with a ✓ or ✗. In JSON mode fn runs silently.
func Step(msg string, fn func() error) error {
	if jsonMode || !isTTY() {
		return fn()
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(80 * time.Millisecond)
		defer t.Stop()
		i := 0
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				frame := AccentStyle.Render(dotFrames[i%len(dotFrames)])
				fmt.Fprintf(os.Stderr, "\r%s %s", frame, DimStyle.Render(msg))
				i++
			}
		}
	}()
	err := fn()
	close(stop)
	<-done
	fmt.Fprint(os.Stderr, "\r\033[K") // clear the spinner line
	if err != nil {
		Errorf("%s — %s", msg, err.Error())
		return err
	}
	Successf("%s", msg)
	return nil
}
