// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package release

import (
	"fmt"
	"net/url"
	"path"
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/sshx"
)

const (
	// maxVisualWidth and maxVisualWidths keep a typo from asking for a 100000px window or a
	// hundred browser launches inside one verify attempt.
	maxVisualWidth  = 10000
	maxVisualWidths = 20

	// visualTimeoutSeconds bounds one browser launch. A launch is seconds and a hung page is
	// forever, and the verify retry loop multiplies whatever one run costs.
	visualTimeoutSeconds = 60
)

// VerifyVisual asks the served page for something a request cannot see: whether it lays out
// without scrolling sideways, and what it looks like. A curl returns 200 for a page whose
// navigation row is wider than the viewport at exactly 1024px; only a browser finds that.
//
// There is deliberately no image comparison. A baseline diff fails on a font hint, and a check
// that cries wolf gets switched off. Overflow is objective, so it is the only assertion.
type VerifyVisual struct {
	// URL is the page to open, as seen from the VM.
	URL string `json:"url"`
	// Widths are the viewport widths in pixels to open the page at.
	Widths []int `json:"widths"`
	// ScreenshotDir is an absolute path on the VM. A screenshot per width is written there as
	// <release>-<width>.png. Empty means no screenshots.
	ScreenshotDir string `json:"screenshotDir,omitempty"`
	// AssertNoHorizontalOverflow fails the release when documentElement.scrollWidth exceeds
	// window.innerWidth at any width.
	AssertNoHorizontalOverflow bool `json:"assertNoHorizontalOverflow,omitempty"`
}

// Validate checks the block when the manifest loads, so a bad width is refused before the backup
// and the sync rather than discovered by the verify step after the files are already live.
func (v *VerifyVisual) Validate() error {
	u, err := url.Parse(strings.TrimSpace(v.URL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("verifyVisual.url must be an http or https URL, got %q", v.URL)
	}
	if len(v.Widths) == 0 {
		return fmt.Errorf("verifyVisual.widths must list at least one width")
	}
	if len(v.Widths) > maxVisualWidths {
		return fmt.Errorf("verifyVisual.widths lists %d widths, the most allowed is %d", len(v.Widths), maxVisualWidths)
	}
	for _, w := range v.Widths {
		if w <= 0 || w > maxVisualWidth {
			return fmt.Errorf("verifyVisual.widths: %d is out of range, use 1 to %d", w, maxVisualWidth)
		}
	}
	if d := v.ScreenshotDir; d != "" && !path.IsAbs(d) {
		return fmt.Errorf("verifyVisual.screenshotDir must be an absolute path on the VM, got %q", d)
	}
	if v.ScreenshotDir == "" && !v.AssertNoHorizontalOverflow {
		return fmt.Errorf("verifyVisual has nothing to do: set screenshotDir, assertNoHorizontalOverflow, or both")
	}
	return nil
}

// The content script records what the page measured on its own root element, where --dump-dom can
// read it back. A page cannot be asked for scrollWidth from outside the browser, and this avoids
// needing node or a driver on the VM: a headless Chrome or Chromium is the only requirement.
const (
	visualExtManifest = `{"manifest_version":3,"name":"bvm","version":"1","content_scripts":[{"matches":["<all_urls>"],"js":["m.js"],"run_at":"document_idle"}]}`
	visualExtScript   = `function m(){var d=document.documentElement;d.setAttribute("data-bvm-sw",d.scrollWidth+" "+window.innerWidth)}if(document.readyState==="complete"){setTimeout(m,300)}else{window.addEventListener("load",function(){setTimeout(m,300)})}`
)

// BrowserMissingMessage is what a VM without a headless browser reports.
const BrowserMissingMessage = "verifyVisual: no headless browser on this VM (looked for chromium, chromium-browser, google-chrome and google-chrome-stable): run `baryovm vm bootstrap` to install one, or install chromium yourself"

// releaseFileSafe reduces a release label to characters that are safe in a file name.
func releaseFileSafe(s string) string {
	out := []rune{}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			out = append(out, r)
		default:
			out = append(out, '_')
		}
	}
	if len(out) == 0 {
		return "release"
	}
	return string(out)
}

// BrowserCmd builds the command that checks one width. It is a pure function so the string can be
// tested without a host. Every interpolated value goes through sshx.Quote; widths are ints.
//
// The command prints one line saying what it found and exits non-zero on a failure, so the verify
// error names the width. It is wrapped in the release's sudo decision like the hooks are, since a
// screenshot directory under /var/log is root-owned.
func (v *VerifyVisual) BrowserCmd(release string, width int, sudo bool) string {
	var b strings.Builder
	b.WriteString(`b=""; for n in chromium chromium-browser google-chrome google-chrome-stable; do if command -v "$n" >/dev/null 2>&1; then b=$(command -v "$n"); break; fi; done; `)
	fmt.Fprintf(&b, `if [ -z "$b" ]; then echo %s; exit 127; fi; `, sshx.Quote(BrowserMissingMessage))
	// Chrome refuses to start its sandbox as root, and a sudo release runs as root.
	flags := "--headless=new --disable-gpu --hide-scrollbars"
	if sudo {
		flags += " --no-sandbox"
	}
	run := fmt.Sprintf(`timeout -k 5 %d "$b" %s --window-size=%d,900`, visualTimeoutSeconds, flags, width)

	if v.ScreenshotDir != "" {
		shot := path.Join(v.ScreenshotDir, fmt.Sprintf("%s-%d.png", releaseFileSafe(release), width))
		fmt.Fprintf(&b, `mkdir -p %s || { echo %s; exit 1; }; `, sshx.Quote(v.ScreenshotDir),
			sshx.Quote(fmt.Sprintf("verifyVisual: cannot create screenshotDir %s", v.ScreenshotDir)))
		fmt.Fprintf(&b, `%s --screenshot=%s %s >/dev/null 2>&1 || { echo %s; exit 1; }; `,
			run, sshx.Quote(shot), sshx.Quote(v.URL),
			sshx.Quote(fmt.Sprintf("verifyVisual: the browser failed or timed out after %ds taking the %dpx screenshot: check it launches on this VM", visualTimeoutSeconds, width)))
	}
	if v.AssertNoHorizontalOverflow {
		fmt.Fprintf(&b, `d=$(mktemp -d) || exit 1; printf '%%s' %s > "$d/manifest.json"; printf '%%s' %s > "$d/m.js"; `,
			sshx.Quote(visualExtManifest), sshx.Quote(visualExtScript))
		fmt.Fprintf(&b, `r=$(%s --load-extension="$d" --virtual-time-budget=10000 --dump-dom %s 2>/dev/null | grep -o 'data-bvm-sw="[0-9]* [0-9]*"' | head -n 1 | tr -dc '0-9 '); rm -rf "$d"; `,
			run, sshx.Quote(v.URL))
		fmt.Fprintf(&b, `set -- $r; if [ -z "$2" ]; then echo %s; exit 1; fi; `,
			sshx.Quote(fmt.Sprintf("verifyVisual: could not measure the page at %dpx: the browser failed, timed out, or did not load the page", width)))
		fmt.Fprintf(&b, `if [ "$1" -gt "$2" ]; then echo "verifyVisual: horizontal overflow at %dpx: document $1px wide, window $2px"; exit 1; fi; echo "verifyVisual: no overflow at %dpx"`, width, width)
	} else {
		b.WriteString(`true`)
	}
	return sshx.SudoShell(sudo, b.String())
}

// Commands returns one browser command per width, in the order the manifest lists them.
func (v *VerifyVisual) Commands(release string, sudo bool) []string {
	out := make([]string, 0, len(v.Widths))
	for _, w := range v.Widths {
		out = append(out, v.BrowserCmd(release, w, sudo))
	}
	return out
}
