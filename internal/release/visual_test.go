// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package release

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func visualBlock() *VerifyVisual {
	return &VerifyVisual{
		URL:                        "https://example.com/?a=1&b=2",
		Widths:                     []int{360, 1024},
		ScreenshotDir:              "/var/log/baryovm/shots",
		AssertNoHorizontalOverflow: true,
	}
}

func TestBrowserCmdIsQuotedBoundedAndNamedByWidth(t *testing.T) {
	cmd := visualBlock().BrowserCmd("abc123", 1024, false)

	for _, want := range []string{
		"'https://example.com/?a=1&b=2'",
		"--window-size=1024,900",
		"timeout -k 5 60",
		"'/var/log/baryovm/shots'",
		"--screenshot='/var/log/baryovm/shots/abc123-1024.png'",
		"--headless=new",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("command is missing %q:\n%s", want, cmd)
		}
	}
	if strings.Contains(cmd, "sudo") || strings.Contains(cmd, "--no-sandbox") {
		t.Errorf("a release without sudo must not use sudo or drop the sandbox:\n%s", cmd)
	}
}

func TestBrowserCmdUnderSudoRunsTheWholeCommandElevated(t *testing.T) {
	cmd := visualBlock().BrowserCmd("abc123", 360, true)

	if !strings.HasPrefix(cmd, `sudo -n "${SHELL:-/bin/sh}" -c '`) {
		t.Fatalf("expected the command wrapped by sudo -n, got:\n%s", cmd)
	}
	if !strings.Contains(cmd, "--no-sandbox") {
		t.Errorf("chrome as root needs --no-sandbox:\n%s", cmd)
	}
}

func TestReleaseNameCannotEscapeTheScreenshotDir(t *testing.T) {
	cmd := visualBlock().BrowserCmd("../x'; rm -rf /", 360, false)

	if strings.Contains(cmd, "rm -rf /") || strings.Contains(cmd, "../x") {
		t.Fatalf("release label leaked into the command:\n%s", cmd)
	}
}

func TestNoVerifyVisualMeansNoBrowserCommand(t *testing.T) {
	m := &Manifest{Verify: []string{"sh check.sh"}}

	p := m.Verification("http://127.0.0.1/health")

	if len(p.Commands) != 1 || p.Commands[0] != "sh check.sh" {
		t.Fatalf("a manifest without verifyVisual must verify as before, got %v", p.Commands)
	}
	for _, c := range (&Manifest{}).Verification("http://x/health").Commands {
		if strings.Contains(c, "chromium") {
			t.Fatalf("browser command without verifyVisual: %s", c)
		}
	}
}

func TestVerifyVisualAddsOneCommandPerWidthAfterTheCurlChecks(t *testing.T) {
	m := &Manifest{VerifyVisual: visualBlock(), ReleaseTag: "r1"}

	p := m.Verification("http://127.0.0.1/health")

	if len(p.Commands) != 3 {
		t.Fatalf("want the curl probe and two widths, got %d: %v", len(p.Commands), p.Commands)
	}
	if !strings.HasPrefix(p.Commands[0], "curl") || !strings.Contains(p.Commands[1], "-360.png") || !strings.Contains(p.Commands[2], "-1024.png") {
		t.Fatalf("commands out of order: %v", p.Commands)
	}
	// The retry bounds are the existing ones.
	if p.Attempts != 5 {
		t.Fatalf("attempts = %d, want the existing default of 5", p.Attempts)
	}

	only := (&Manifest{VerifyVisual: visualBlock()}).Verification("")
	if only.Source != "verifyVisual" || len(only.Commands) != 2 {
		t.Fatalf("visual alone should be its own source, got %q %v", only.Source, only.Commands)
	}
}

func TestVerifyVisualValidation(t *testing.T) {
	good := visualBlock()
	if err := good.Validate(); err != nil {
		t.Fatalf("a valid block was refused: %v", err)
	}
	cases := map[string]func(v *VerifyVisual){
		"zero width":          func(v *VerifyVisual) { v.Widths = []int{0} },
		"negative width":      func(v *VerifyVisual) { v.Widths = []int{-5} },
		"absurd width":        func(v *VerifyVisual) { v.Widths = []int{999999} },
		"no widths":           func(v *VerifyVisual) { v.Widths = nil },
		"too many widths":     func(v *VerifyVisual) { v.Widths = make([]int, 21) },
		"relative shotdir":    func(v *VerifyVisual) { v.ScreenshotDir = "shots" },
		"not a web url":       func(v *VerifyVisual) { v.URL = "file:///etc/passwd" },
		"nothing to do":       func(v *VerifyVisual) { v.ScreenshotDir = ""; v.AssertNoHorizontalOverflow = false },
		"empty url":           func(v *VerifyVisual) { v.URL = "" },
		"url with no host":    func(v *VerifyVisual) { v.URL = "https://" },
		"tilde in screenshot": func(v *VerifyVisual) { v.ScreenshotDir = "~/shots" },
	}
	for name, mutate := range cases {
		v := visualBlock()
		for i := range v.Widths {
			v.Widths[i] = 360
		}
		mutate(v)
		if err := v.Validate(); err == nil {
			t.Errorf("%s: expected a validation error", name)
		}
	}
}

func TestLoadRefusesABadVerifyVisual(t *testing.T) {
	p := filepath.Join(t.TempDir(), "m.json")
	body := `{"localRoot":"/a","remoteRoot":"/b","verifyVisual":{"url":"https://x.test/","widths":[0],"assertNoHorizontalOverflow":true}}`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p, false); err == nil || !strings.Contains(err.Error(), "verifyVisual.widths") {
		t.Fatalf("expected a widths error, got %v", err)
	}
}

// runVisual runs the generated command in a local sh with a fake browser on PATH, which is the
// only way to test the part that parses the measurement without a real Chrome.
func runVisual(t *testing.T, withBrowser bool, sw string, width int) (string, error) {
	t.Helper()
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// macOS has no timeout(1); the command is `timeout -k 5 60 <cmd>`.
	write("timeout", "#!/bin/sh\nshift 3\nexec \"$@\"\n")
	if withBrowser {
		write("chromium", `#!/bin/sh
w=""; shot=""; dump=0
for a in "$@"; do
  case "$a" in
    --window-size=*) w="${a#--window-size=}"; w="${w%%,*}";;
    --screenshot=*) shot="${a#--screenshot=}";;
    --dump-dom) dump=1;;
  esac
done
[ -n "$shot" ] && : > "$shot"
[ "$dump" = 1 ] && echo "<html data-bvm-sw=\"$FAKE_SW $w\"></html>"
exit 0
`)
	}
	shots := filepath.Join(dir, "shots")
	v := visualBlock()
	v.ScreenshotDir = shots
	cmd := exec.Command("sh", "-c", v.BrowserCmd("rel1", width, false))
	cmd.Env = []string{"PATH=" + dir + ":/usr/bin:/bin", "FAKE_SW=" + sw}
	out, err := cmd.CombinedOutput()
	if withBrowser {
		if _, statErr := os.Stat(filepath.Join(shots, "rel1-"+itoa(width)+".png")); statErr != nil {
			t.Errorf("screenshot not written: %v", statErr)
		}
	}
	return string(out), err
}

func itoa(n int) string {
	s := ""
	for ; n > 0; n /= 10 {
		s = string(rune('0'+n%10)) + s
	}
	return s
}

func TestAnOverflowingPageFailsAndNamesTheWidth(t *testing.T) {
	out, err := runVisual(t, true, "1059", 1024)

	if err == nil {
		t.Fatalf("overflow must fail the check, output: %s", out)
	}
	if !strings.Contains(out, "horizontal overflow at 1024px") || !strings.Contains(out, "document 1059px") {
		t.Fatalf("message must name the width and sizes, got: %s", out)
	}
}

func TestAPageThatFitsPasses(t *testing.T) {
	out, err := runVisual(t, true, "1024", 1024)
	if err != nil {
		t.Fatalf("a page that fits failed: %v: %s", err, out)
	}
	if !strings.Contains(out, "no overflow at 1024px") {
		t.Fatalf("unexpected output: %s", out)
	}
}

func TestAMissingBrowserPointsAtBootstrap(t *testing.T) {
	for _, n := range []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable"} {
		if _, err := exec.LookPath(n); err == nil {
			t.Skipf("%s is installed here, cannot test the missing case", n)
		}
	}
	out, err := runVisual(t, false, "1", 360)

	if err == nil {
		t.Fatalf("a missing browser must fail, output: %s", out)
	}
	if !strings.Contains(out, "install chromium on the VM") || strings.Contains(out, "not found") {
		t.Fatalf("expected the bootstrap message, got: %s", out)
	}
}
