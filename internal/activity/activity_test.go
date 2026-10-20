// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package activity

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func lines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) == 0 {
		return nil
	}
	if !bytes.HasSuffix(b, []byte("\n")) {
		t.Fatalf("log does not end in a newline: %q", b)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

func TestRecordWritesOneLineWithTheStableFields(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BARYOVM_HOME", home)
	t.Setenv("BARYOVM_ACTIVITY", "")

	err := Record(Entry{Version: "1.2.3", Action: "stack release", VM: "oracle", Stack: "site", OK: false, DurationMS: 42, Error: "boom"})
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(home, "activity.jsonl")
	got := lines(t, path)
	if len(got) != 1 {
		t.Fatalf("want one line, got %d: %v", len(got), got)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(got[0]), &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"ts", "version", "action", "vm", "stack", "ok", "duration_ms", "error", "host"} {
		if _, ok := m[k]; !ok {
			t.Errorf("missing field %q in %s", k, got[0])
		}
	}
	ts, err := time.Parse(time.RFC3339, m["ts"].(string))
	if err != nil || ts.Location() != time.UTC {
		t.Errorf("ts must be UTC RFC 3339, got %v (%v)", m["ts"], err)
	}
	if m["ok"] != false || m["duration_ms"] != float64(42) || m["action"] != "stack release" {
		t.Errorf("unexpected values: %s", got[0])
	}
	if m["host"] == "" {
		t.Error("host is empty")
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", st.Mode().Perm())
	}
}

func TestRecordOmitsVMStackAndErrorWhenEmpty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BARYOVM_HOME", home)
	if err := Record(Entry{Version: "v", Action: "doctor", OK: true}); err != nil {
		t.Fatal(err)
	}
	line := lines(t, filepath.Join(home, "activity.jsonl"))[0]
	for _, k := range []string{"\"vm\"", "\"stack\"", "\"error\""} {
		if strings.Contains(line, k) {
			t.Errorf("%s should be omitted: %s", k, line)
		}
	}
}

func TestRecordAppendsAndNeverRewrites(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BARYOVM_HOME", home)
	path := filepath.Join(home, "activity.jsonl")
	if err := os.WriteFile(path, []byte("{\"old\":true}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Record(Entry{Action: "doctor", OK: true}); err != nil {
		t.Fatal(err)
	}
	got := lines(t, path)
	if len(got) != 2 || got[0] != "{\"old\":true}" {
		t.Fatalf("existing line was not kept: %v", got)
	}
}

func TestOffWritesNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BARYOVM_HOME", home)
	t.Setenv("BARYOVM_ACTIVITY", "off")
	if err := Record(Entry{Action: "doctor", OK: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "activity.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("log must not exist when off, stat err = %v", err)
	}
}

func TestRecordReportsAnUnwritableLog(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BARYOVM_HOME", home)
	if err := os.Mkdir(filepath.Join(home, "activity.jsonl"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Record(Entry{Action: "doctor", OK: true}); err == nil {
		t.Fatal("want an error when the log cannot be opened")
	}
}

func TestConcurrentAppendsLeaveWholeLines(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BARYOVM_HOME", home)
	const n = 50
	pad := strings.Repeat("x", 3000)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := Record(Entry{Action: "stack release", Error: pad}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	got := lines(t, filepath.Join(home, "activity.jsonl"))
	if len(got) != n {
		t.Fatalf("want %d lines, got %d", n, len(got))
	}
	for _, l := range got {
		var e Entry
		if err := json.Unmarshal([]byte(l), &e); err != nil {
			t.Fatalf("torn line: %v", err)
		}
	}
}
