// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package verify

import (
	"fmt"
	"time"
)

// Hard limits. Whatever a manifest, a spec or a flag asks for is clamped to these, so verify cannot
// retry without end however it was configured.
const (
	MaxAttempts    = 100
	MaxDelay       = time.Minute
	DefaultTimeout = 5 * time.Minute
	MaxTimeout     = 30 * time.Minute
)

// probeAllowance is the time one attempt may spend on its checks, on top of the delay.
const probeAllowance = 10 * time.Second

// Budget is how long to keep trying.
type Budget struct {
	Attempts int
	Delay    time.Duration
	// Timeout is the total wall-clock time across all attempts. Zero means attempts x (delay + a
	// probe) with DefaultTimeout as the floor, so a long attempts x delay is not cut short at 5
	// minutes, capped at MaxTimeout.
	Timeout time.Duration
}

// Bounded returns the budget with every value inside its limit and the timeout filled in.
func (b Budget) Bounded() Budget {
	if b.Attempts < 1 {
		b.Attempts = 1
	}
	if b.Attempts > MaxAttempts {
		b.Attempts = MaxAttempts
	}
	if b.Delay < 0 {
		b.Delay = 0
	}
	if b.Delay > MaxDelay {
		b.Delay = MaxDelay
	}
	if b.Timeout <= 0 {
		b.Timeout = time.Duration(b.Attempts) * (b.Delay + probeAllowance)
		if b.Timeout < DefaultTimeout {
			b.Timeout = DefaultTimeout
		}
	}
	if b.Timeout > MaxTimeout {
		b.Timeout = MaxTimeout
	}
	return b
}

// Runner runs a command on the VM. *sshx.Client satisfies it.
type Runner interface {
	Run(cmd string) (string, error)
}

// Pass runs every step once, in order, and stops at the first that fails. The reason names the
// step, so the report says which assertion failed rather than "unhealthy".
func Pass(r Runner, steps []Step) (bool, string) {
	for _, s := range steps {
		out, err := r.Run(s.Cmd)
		if ok, why := s.Judge(out, err); !ok {
			return false, s.Label + ": " + why
		}
	}
	return true, ""
}

// Clock is the time source, so a test can drive the retry loop without sleeping.
type Clock struct {
	Now   func() time.Time
	Sleep func(time.Duration)
}

// RealClock is the wall clock.
func RealClock() Clock { return Clock{Now: time.Now, Sleep: time.Sleep} }

// Outcome is how a retry ended.
type Outcome struct {
	OK       bool
	Attempts int
	// Reason is the last failure, empty when OK.
	Reason string
	// TimedOut is true when the time budget ended the retry before the attempts did.
	TimedOut bool
	Elapsed  time.Duration
}

// FailedError is an unhealthy verdict, as opposed to a fault in running the check.
type FailedError struct{ Reason string }

func (e *FailedError) Error() string { return "not healthy: " + e.Reason }

// TimeoutError is a retry that ran out of time. It is an error on its own so a timeout can never
// be mistaken for a pass, or for an ordinary failed attempt.
type TimeoutError struct {
	Elapsed  time.Duration
	Attempts int
	Reason   string
}

func (e *TimeoutError) Error() string {
	return fmt.Sprintf("timed out after %s and %d attempt(s), last failure: %s",
		e.Elapsed.Round(time.Second), e.Attempts, e.Reason)
}

// Err turns a failed outcome into an error, nil when it passed.
func (o Outcome) Err() error {
	switch {
	case o.OK:
		return nil
	case o.TimedOut:
		return &TimeoutError{Elapsed: o.Elapsed, Attempts: o.Attempts, Reason: o.Reason}
	}
	return fmt.Errorf("failed after %d attempt(s): %s", o.Attempts, o.Reason)
}

// Retry calls pass until it succeeds, the attempts are used or the time is. It never ends without
// a verdict: a stop for time is reported as TimedOut, and the caller must treat it as a failure.
func Retry(b Budget, clk Clock, pass func() (bool, string)) Outcome {
	b = b.Bounded()
	start := clk.Now()
	deadline := start.Add(b.Timeout)
	out := Outcome{}
	for out.Attempts < b.Attempts {
		out.Attempts++
		ok, why := pass()
		out.Elapsed = clk.Now().Sub(start)
		if ok {
			out.OK = true
			out.Reason = ""
			return out
		}
		out.Reason = why
		if out.Attempts == b.Attempts {
			break
		}
		// Stop when the next attempt could not start inside the budget, instead of sleeping
		// into a deadline that has already passed.
		if !clk.Now().Add(b.Delay).Before(deadline) {
			out.TimedOut = true
			return out
		}
		clk.Sleep(b.Delay)
	}
	out.Elapsed = clk.Now().Sub(start)
	return out
}
