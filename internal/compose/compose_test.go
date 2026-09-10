package compose

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// The image reference has to come from `compose config`, because that is the only place it appears
// as a name. `compose ps` and `compose images` report what the container actually runs, which on a
// host that resolved the tag to a digest is a bare sha256, which is no use as a rollback target, since
// `docker tag` needs a name. Getting this wrong is quiet: no reference means no detected change,
// which reads as "already up to date" forever.
func TestParseConfigImages(t *testing.T) {
	const out = `{
      "services": {
        "app":       {"image": "ghcr.io/baryodev/barako-cms:playground"},
        "admin":     {"image": "ghcr.io/baryodev/barako-admin:playground"},
        "postgres":  {"image": "postgres:16-alpine"},
        "worker":    {"build": {"context": "."}}
      }
    }`

	refs, err := parseConfigImages(out)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := refs["app"]; got != "ghcr.io/baryodev/barako-cms:playground" {
		t.Errorf("app: got %q", got)
	}
	if got := refs["postgres"]; got != "postgres:16-alpine" {
		t.Errorf("postgres: got %q", got)
	}
	// A build-only service has nothing to pull or roll back to, so it must not appear at all.
	if _, ok := refs["worker"]; ok {
		t.Errorf("build-only service should be omitted, got %q", refs["worker"])
	}
	if len(refs) != 3 {
		t.Errorf("expected 3 services with images, got %d: %v", len(refs), refs)
	}
}

func TestParseConfigImagesRejectsGarbage(t *testing.T) {
	// A compose version that prints something unexpected must be an error, not an empty map that
	// silently means "nothing to update".
	if _, err := parseConfigImages("not json"); err == nil {
		t.Fatal("expected an error for unparseable output")
	}
}

// Output that is only newlines is nothing read. Counting it as a line would report lines:1 with no
// note, which is the ambiguity this is meant to remove wearing a different hat.
func TestDescribeLogsTreatsBlankOutputAsNothingRead(t *testing.T) {
	for _, in := range []string{"", "\n", "  \n\t\n"} {
		r := describeLogs(in)
		if r.Lines != 0 {
			t.Errorf("%q: expected 0 lines, got %d", in, r.Lines)
		}
		if r.Note == "" {
			t.Errorf("%q: expected a note saying the read found nothing", in)
		}
		if r.State != LogsUnknown {
			t.Errorf("%q: the output alone cannot explain itself, got state %q", in, r.State)
		}
		if r.Output != in {
			t.Errorf("%q: output was not preserved, got %q", in, r.Output)
		}
	}

	r := describeLogs("one\n\nthree\n")
	if r.Lines != 2 {
		t.Errorf("blank lines should not count, got %d", r.Lines)
	}
	if r.State != LogsRead {
		t.Errorf("a read with lines in it is state %q, got %q", LogsRead, r.State)
	}
	if r.Note != "" {
		t.Errorf("a non-empty read must not carry a note: %q", r.Note)
	}
}

// fakeRunner answers commands by substring, so a read can be driven with no VM. An unmatched
// command is an error rather than an empty string: an empty answer is exactly what these tests are
// about, so it must never happen by accident. Keep the patterns disjoint.
type fakeRunner struct {
	answers map[string]string
	seen    []string
}

func (f *fakeRunner) Run(cmd string) (string, error) {
	f.seen = append(f.seen, cmd)
	for match, out := range f.answers {
		if strings.Contains(cmd, match) {
			return out, nil
		}
	}
	return "", fmt.Errorf("fakeRunner: no answer for %q", cmd)
}

// The reported case and "nothing is running" both come back from compose as exit 0 with zero bytes.
// They are different problems with different fixes, so they must not serialize the same.
func TestAnEmptyReadSaysWhetherAnythingIsRunning(t *testing.T) {
	s := Stack{Dir: "/opt/app"}

	silent := &fakeRunner{answers: map[string]string{" logs": "", " ps -q": "9a1f0c2d3e4f\n"}}
	r, err := ReadLogs(silent, s, nil, 100)
	if err != nil {
		t.Fatalf("ReadLogs: %v", err)
	}
	if r.State != LogsSilent {
		t.Errorf("containers are up and silent, got state %q (note %q)", r.State, r.Note)
	}

	stopped := &fakeRunner{answers: map[string]string{" logs": "", " ps -q": ""}}
	r2, err := ReadLogs(stopped, s, nil, 100)
	if err != nil {
		t.Fatalf("ReadLogs: %v", err)
	}
	if r2.State != LogsNotRunning {
		t.Errorf("nothing is running, got state %q (note %q)", r2.State, r2.Note)
	}

	// The point of the exercise: the two answers have to differ on the wire, not just in the note a
	// human reads.
	a, _ := json.Marshal(r)
	b, _ := json.Marshal(r2)
	if string(a) == string(b) {
		t.Fatalf("a silent stack and a stopped one serialize identically: %s", a)
	}
	if r.Note == "" || r2.Note == "" || r.Note == r2.Note {
		t.Errorf("each state needs its own note, got %q and %q", r.Note, r2.Note)
	}
}

// A read with lines in it must not pay for a second round trip, and must not be reclassified by one.
func TestAReadWithLinesDoesNotAskForContainers(t *testing.T) {
	f := &fakeRunner{answers: map[string]string{" logs": "app-1  | started\n"}}

	r, err := ReadLogs(f, Stack{Dir: "/opt/app"}, nil, 100)
	if err != nil {
		t.Fatalf("ReadLogs: %v", err)
	}
	if r.State != LogsRead || r.Lines != 1 {
		t.Errorf("expected one line read, got state %q lines %d", r.State, r.Lines)
	}
	if len(f.seen) != 1 {
		t.Errorf("expected one command, got %v", f.seen)
	}
}

// The container check is best effort. If it fails the read is still an answer, just an unexplained
// one, and it must not be reported as "nothing is running".
func TestAFailedContainerCheckIsNotReportedAsNothingRunning(t *testing.T) {
	f := &fakeRunner{answers: map[string]string{" logs": ""}}

	r, err := ReadLogs(f, Stack{Dir: "/opt/app"}, nil, 100)
	if err != nil {
		t.Fatalf("a logs read that worked must not fail on the follow-up: %v", err)
	}
	if r.State != LogsUnknown {
		t.Errorf("expected state %q when the check errored, got %q", LogsUnknown, r.State)
	}
}

func TestPsQuietCmd(t *testing.T) {
	got := Stack{Dir: "/opt/app", Sudo: true}.PsQuietCmd([]string{"api"})
	for _, want := range []string{"cd '/opt/app'", "sudo -n docker compose", " ps -q", " 'api'"} {
		if !strings.Contains(got, want) {
			t.Errorf("PsQuietCmd missing %q in: %s", want, got)
		}
	}
}
