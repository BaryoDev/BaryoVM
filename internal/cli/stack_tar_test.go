package cli

import (
	"encoding/json"
	"io"
	"os/exec"
	"strings"
	"testing"

	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"github.com/BaryoDev/BaryoVM/internal/ui"
)

// tarSession is a fakeSession that also takes a stream on stdin, and records that it did.
type tarSession struct {
	fakeSession
	streamed *int
}

func (s tarSession) RunStdin(cmd string, in io.Reader) (string, error) {
	s.seen = append(s.seen, "stdin: "+cmd)
	n, _ := io.Copy(io.Discard, in)
	*s.streamed += int(n)
	if strings.Contains(cmd, "find ") {
		return "", nil
	}
	return "", nil
}

func withLookPath(t *testing.T, fn func(string) (string, error)) {
	t.Helper()
	prev := lookPath
	lookPath = fn
	prevKH := knownHostsFileFor
	knownHostsFileFor = func(*fleet.VM) (string, func(), error) { return "/tmp/kh", func() {}, nil }
	t.Cleanup(func() { lookPath, knownHostsFileFor = prev, prevKH })
}

func noRsync(string) (string, error)  { return "", exec.ErrNotFound }
func hasRsync(string) (string, error) { return "/usr/bin/rsync", nil }

func releaseResult(t *testing.T, f *fakeRunner, streamed *int) (ui.Result, error) {
	t.Helper()
	prevDial, prevLocal := dialRelease, runLocal
	dialRelease = func(*fleet.VM) (remoteSession, error) {
		return tarSession{fakeSession{f}, streamed}, nil
	}
	runLocal = func(c *exec.Cmd) (string, error) {
		f.seen = append(f.seen, "local: "+strings.Join(c.Args, " "))
		return "", nil
	}
	t.Cleanup(func() { dialRelease, runLocal = prevDial, prevLocal })

	stdout, _, err := runCmdE(t, newStackReleaseCmd(), f, true, "demo")
	var r ui.Result
	if e := json.Unmarshal([]byte(stdout), &r); e != nil {
		t.Fatalf("no JSON envelope: %v (%q)", e, stdout)
	}
	return r, err
}

func transportOf(t *testing.T, r ui.Result) string {
	t.Helper()
	d, ok := r.Data.(map[string]any)
	if !ok {
		t.Fatalf("release result carries no data: %+v", r)
	}
	s, _ := d["transport"].(string)
	return s
}

func TestReleaseUsesTarWhenRsyncIsMissing(t *testing.T) {
	releaseFixture(t)
	withLookPath(t, noRsync)
	answers := releaseAnswers(ownedDemo)
	answers["find ."] = ""
	f := &fakeRunner{answers: answers}
	var streamed int

	r, err := releaseResult(t, f, &streamed)
	if err != nil {
		t.Fatalf("release failed without rsync: %v\n%s", err, strings.Join(f.seen, "\n"))
	}
	if got := transportOf(t, r); got != "tar" {
		t.Errorf("transport = %q, want tar", got)
	}
	for _, c := range f.seen {
		if strings.HasPrefix(c, "local: ") {
			t.Errorf("rsync ran although it is missing: %s", c)
		}
	}
	if streamed == 0 {
		t.Error("nothing was streamed to the host")
	}
}

func TestReleaseKeepsRsyncWhenPresent(t *testing.T) {
	releaseFixture(t)
	withLookPath(t, hasRsync)
	f := &fakeRunner{answers: releaseAnswers(ownedDemo)}
	var streamed int

	r, err := releaseResult(t, f, &streamed)
	if err != nil {
		t.Fatal(err)
	}
	if got := transportOf(t, r); got != "rsync" {
		t.Errorf("transport = %q, want rsync", got)
	}
	if streamed != 0 {
		t.Error("tar was used although rsync exists")
	}
}

func TestDoctorNamesTheReleaseTransport(t *testing.T) {
	for _, c := range []struct {
		look func(string) (string, error)
		want string
	}{{hasRsync, "rsync"}, {noRsync, "tar"}} {
		withLookPath(t, c.look)
		t.Setenv("HOME", t.TempDir())
		var found *doctorCheck
		for _, r := range doctorChecks(false, "linux") {
			r := r
			if r.Name == "release transport" {
				found = &r
			}
		}
		if found == nil {
			t.Fatal("doctor has no release transport line")
		}
		if !found.Present || !strings.HasPrefix(found.Detail, c.want) {
			t.Errorf("rsync lookup = %v: detail %q, want it to start with %q", c.want, found.Detail, c.want)
		}
	}
}

// A machine with no rsync can still release, so doctor must not call it broken for that.
func TestDoctorDoesNotFailAMachineForLackingRsync(t *testing.T) {
	withLookPath(t, noRsync)
	t.Setenv("HOME", t.TempDir())
	for _, r := range doctorChecks(false, "linux") {
		if r.Name == "rsync" && !r.Optional {
			t.Error("rsync is still required although release falls back to tar")
		}
	}
}
