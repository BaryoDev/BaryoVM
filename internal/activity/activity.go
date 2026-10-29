// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package activity appends one JSON line per command to $BARYOVM_HOME/activity.jsonl. The line
// format is the contract: anything can read the file. It never prints; the caller decides what to
// do with a write error.
package activity

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Entry is one line of the log. The field names are stable. Nothing outside these fields is ever
// recorded: no flag values, arguments or tokens.
type Entry struct {
	TS         string `json:"ts"`
	Version    string `json:"version"`
	Action     string `json:"action"`
	VM         string `json:"vm,omitempty"`
	Stack      string `json:"stack,omitempty"`
	OK         bool   `json:"ok"`
	DurationMS int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
	Host       string `json:"host"`
}

// Enabled reports whether recording is on. BARYOVM_ACTIVITY=off turns it off.
func Enabled() bool { return os.Getenv("BARYOVM_ACTIVITY") != "off" }

// Path is the log file: BARYOVM_HOME if set, else ~/.baryovm, then activity.jsonl.
func Path() string {
	if d := os.Getenv("BARYOVM_HOME"); d != "" {
		return filepath.Join(d, "activity.jsonl")
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".baryovm", "activity.jsonl")
}

// Record appends e as one line. It fills ts (UTC) and host when they are empty. It does nothing
// when recording is off. The line goes out in a single write on an O_APPEND file, so concurrent
// processes leave whole lines.
func Record(e Entry) error {
	if !Enabled() {
		return nil
	}
	if e.TS == "" {
		e.TS = time.Now().UTC().Format(time.RFC3339)
	}
	if e.Host == "" {
		e.Host, _ = os.Hostname()
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	b = append(b, '\n')

	path := Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) // #nosec G304 -- log path comes from the fleet directory
	if err != nil {
		return err
	}
	if st, err := f.Stat(); err == nil && st.Mode().Perm()&0o077 != 0 {
		_ = f.Chmod(0o600)
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
