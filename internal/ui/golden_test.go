package ui

import (
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Run `go test ./internal/ui -update` to rewrite the golden files, then read the diff.
var update = flag.Bool("update", false, "rewrite golden files")

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with -update to create it): %v", err)
	}
	if string(want) != got {
		t.Errorf("%s differs from %s\n--- want\n%s\n--- got\n%s", name, path, want, got)
	}
}

// capture runs fn and returns what it wrote to stdout and stderr.
func capture(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr
	ro, wo, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	re, we, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout, os.Stderr = wo, we
	defer func() { os.Stdout, os.Stderr = oldOut, oldErr }()

	fn()

	wo.Close()
	we.Close()
	o, _ := io.ReadAll(ro)
	e, _ := io.ReadAll(re)
	return string(o), string(e)
}

func jsonOn(t *testing.T) {
	t.Helper()
	SetJSON(true)
	t.Cleanup(func() { SetJSON(false) })
}

func TestEmitJSONSuccessGolden(t *testing.T) {
	jsonOn(t)
	out, errOut := capture(t, func() {
		Emit(Result{OK: true, Action: "vm ping", Message: "pong", Data: map[string]int{"rtt": 12}})
	})
	if errOut != "" {
		t.Errorf("stderr should be empty in JSON mode, got %q", errOut)
	}
	golden(t, "emit_ok", out)
}

func TestEmitJSONFailureGolden(t *testing.T) {
	jsonOn(t)
	out, errOut := capture(t, func() {
		Emit(Result{OK: false, Action: "stack restore", Error: "refused without --yes"})
	})
	if errOut != "" {
		t.Errorf("stderr should be empty in JSON mode, got %q", errOut)
	}
	golden(t, "emit_error", out)
}

// Chrome must vanish in JSON mode, or a parser reading stderr and stdout together breaks.
func TestChromeIsSilentInJSONMode(t *testing.T) {
	jsonOn(t)
	out, errOut := capture(t, func() {
		Title("t")
		Detail("k", "v")
		Successf("ok %d", 1)
		Warnf("warn")
		Notef("note")
		Errorf("bad")
	})
	if out != "" || errOut != "" {
		t.Errorf("want no output, got stdout %q stderr %q", out, errOut)
	}
}

func TestHumanEmitGoesToStderrOnly(t *testing.T) {
	SetJSON(false)
	out, errOut := capture(t, func() {
		Emit(Result{OK: true, Action: "a", Message: "all done"})
		Emit(Result{OK: false, Action: "stack update", Error: "no health url"})
	})
	if out != "" {
		t.Errorf("stdout should be empty in human mode, got %q", out)
	}
	if !strings.Contains(errOut, "all done") {
		t.Errorf("stderr missing success message: %q", errOut)
	}
	if !strings.Contains(errOut, "stack update: no health url") {
		t.Errorf("stderr missing failure line: %q", errOut)
	}
}

func TestStepRunsAndPassesTheErrorThrough(t *testing.T) {
	jsonOn(t)
	boom := errors.New("boom")
	ran := false
	var got error
	capture(t, func() {
		got = Step("work", func() error { ran = true; return boom })
	})
	if !ran {
		t.Error("fn did not run")
	}
	if got != boom {
		t.Errorf("got %v, want %v", got, boom)
	}
	capture(t, func() {
		got = Step("work", func() error { return nil })
	})
	if got != nil {
		t.Errorf("want nil, got %v", got)
	}
}
