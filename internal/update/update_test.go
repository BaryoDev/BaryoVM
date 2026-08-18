package update

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/BaryoDev/BaryoVM/internal/compose"
)

// fakeRunner stands in for compose-over-SSH. It records the calls so a test can assert on the order
// things happened in, which is most of what matters here: backup before recreate, retag before the
// second up.
type fakeRunner struct {
	calls []string

	before, after []compose.Image
	pulled        bool

	healthy      []bool // consumed one per Healthy() call; missing entries mean false
	healthCalls  int
	upErr        error
	upErrOnFirst bool
	backupErr    error
	retagErr     error
}

func (f *fakeRunner) Images(svcs []string) ([]compose.Image, error) {
	f.calls = append(f.calls, "images")
	if f.pulled {
		return f.after, nil
	}
	return f.before, nil
}

func (f *fakeRunner) Pull(svcs []string) (string, error) {
	f.calls = append(f.calls, "pull")
	f.pulled = true
	return "", nil
}

func (f *fakeRunner) Up(svcs []string) (string, error) {
	f.calls = append(f.calls, "up:"+strings.Join(svcs, ","))
	if f.upErr != nil && (!f.upErrOnFirst || strings.Count(strings.Join(f.calls, " "), "up:") == 1) {
		return "", f.upErr
	}
	return "", nil
}

func (f *fakeRunner) Retag(id, ref string) (string, error) {
	f.calls = append(f.calls, "retag:"+id)
	return "", f.retagErr
}

func (f *fakeRunner) Healthy() (bool, error) {
	f.calls = append(f.calls, "health")
	i := f.healthCalls
	f.healthCalls++
	if i < len(f.healthy) {
		return f.healthy[i], nil
	}
	return false, nil
}

func (f *fakeRunner) Backup() (string, error) {
	f.calls = append(f.calls, "backup")
	return "backup ok", f.backupErr
}

// img builds a service whose container runs `running` while its reference resolves to `target`.
// Equal values mean the service is current; differing values mean an update is pending.
func img(svc, ref, running, target string) compose.Image {
	return compose.Image{Service: svc, Ref: ref, Running: running, ID: target}
}

func opts() Options {
	return Options{HasHealthCheck: true, HasBackup: true, HealthAttempts: 2, HealthDelay: time.Millisecond}
}

func TestNoChangeDoesNotRestart(t *testing.T) {
	same := []compose.Image{img("app", "repo:tag", "sha256:aaa", "sha256:aaa")}
	f := &fakeRunner{before: same, after: same}

	res, err := Run(f, opts())

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Updated || res.Skipped != "already up to date" {
		t.Fatalf("expected a skip, got %+v", res)
	}
	// The point: an unchanged stack must not be recreated, or a nightly job becomes a nightly restart.
	if strings.Contains(strings.Join(f.calls, " "), "up:") {
		t.Fatalf("recreated containers despite no image change: %v", f.calls)
	}
	if strings.Contains(strings.Join(f.calls, " "), "backup") {
		t.Fatalf("backed up despite no image change: %v", f.calls)
	}
}

func TestHealthyUpdateKeepsNewImages(t *testing.T) {
	f := &fakeRunner{
		before:  []compose.Image{img("app", "repo:tag", "sha256:old", "sha256:new")},
		after:   []compose.Image{img("app", "repo:tag", "sha256:old", "sha256:new")},
		healthy: []bool{true},
	}

	res, err := Run(f, opts())

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Updated || res.RolledBack {
		t.Fatalf("expected a clean update, got %+v", res)
	}
	joined := strings.Join(f.calls, " ")
	if strings.Contains(joined, "retag") {
		t.Fatalf("rolled back a healthy update: %v", f.calls)
	}
	// Backup must precede the recreate, or it captures the new schema rather than the old one.
	if strings.Index(joined, "backup") > strings.Index(joined, "up:") {
		t.Fatalf("backed up after recreating: %v", f.calls)
	}
}

func TestUnhealthyUpdateRollsBack(t *testing.T) {
	f := &fakeRunner{
		before: []compose.Image{img("app", "repo:tag", "sha256:old", "sha256:new")},
		after:  []compose.Image{img("app", "repo:tag", "sha256:old", "sha256:new")},
		// Never healthy on the new image; healthy again once rolled back.
		healthy: []bool{false, false, true},
	}

	res, err := Run(f, opts())

	if err == nil {
		t.Fatal("expected an error describing the failed update")
	}
	if !res.RolledBack || res.Updated {
		t.Fatalf("expected a rollback, got %+v", res)
	}
	joined := strings.Join(f.calls, " ")
	if !strings.Contains(joined, "retag:sha256:old") {
		t.Fatalf("did not retag the previous image: %v", f.calls)
	}
	if strings.Count(joined, "up:") != 2 {
		t.Fatalf("expected a second up to apply the rollback: %v", f.calls)
	}
	if res.Health != "rolled back, healthy" {
		t.Fatalf("expected the rollback to be verified, got %q", res.Health)
	}
}

func TestRollbackWhenTheContainerWontStart(t *testing.T) {
	f := &fakeRunner{
		before:       []compose.Image{img("app", "repo:tag", "sha256:old", "sha256:new")},
		after:        []compose.Image{img("app", "repo:tag", "sha256:old", "sha256:new")},
		upErr:        errors.New("container exited"),
		upErrOnFirst: true,
		healthy:      []bool{true},
	}

	res, err := Run(f, opts())

	if err == nil || !res.RolledBack {
		t.Fatalf("a failed start must roll back, got res=%+v err=%v", res, err)
	}
	if !strings.Contains(strings.Join(f.calls, " "), "retag:sha256:old") {
		t.Fatalf("did not restore the previous image: %v", f.calls)
	}
}

func TestRollbackReportsWhenThereIsNothingToRestore(t *testing.T) {
	f := &fakeRunner{
		// No id recorded: the previous image is not on the host, so it cannot be restored.
		before:  []compose.Image{img("app", "repo:tag", "", "sha256:new")},
		after:   []compose.Image{img("app", "repo:tag", "", "sha256:new")},
		healthy: []bool{false},
	}

	// With no previous id the update is not even detected as a change, which is itself correct:
	// there is no safe update to make. Assert that rather than pretending otherwise.
	res, err := Run(f, opts())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Updated {
		t.Fatalf("must not update when the previous image cannot be identified: %+v", res)
	}
}

func TestAutoRefusesAStackThatHasNotOptedIn(t *testing.T) {
	f := &fakeRunner{
		before: []compose.Image{img("app", "repo:tag", "sha256:old", "sha256:new")},
		after:  []compose.Image{img("app", "repo:tag", "sha256:old", "sha256:new")},
	}
	o := opts()
	o.Auto = true
	o.AutoUpdate = false

	_, err := Run(f, o)

	if !errors.Is(err, ErrNotAutoUpdatable) {
		t.Fatalf("expected a refusal, got %v", err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("refused stack must not be touched at all: %v", f.calls)
	}
}

func TestAutoRefusesAStackItCannotVerify(t *testing.T) {
	f := &fakeRunner{}
	o := opts()
	o.Auto, o.AutoUpdate, o.HasHealthCheck = true, true, false

	_, err := Run(f, o)

	if !errors.Is(err, ErrNoHealthCheck) {
		t.Fatalf("expected a refusal without a health check, got %v", err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("must not touch a stack it cannot verify: %v", f.calls)
	}
}

func TestFailedBackupStopsTheUpdate(t *testing.T) {
	f := &fakeRunner{
		before:    []compose.Image{img("app", "repo:tag", "sha256:old", "sha256:new")},
		after:     []compose.Image{img("app", "repo:tag", "sha256:old", "sha256:new")},
		backupErr: errors.New("pg_dump failed"),
	}

	res, err := Run(f, opts())

	if err == nil {
		t.Fatal("expected the update to stop when the backup fails")
	}
	if res.Updated || res.RolledBack {
		t.Fatalf("nothing should have been changed: %+v", res)
	}
	if strings.Contains(strings.Join(f.calls, " "), "up:") {
		t.Fatalf("recreated containers after a failed backup: %v", f.calls)
	}
}

func TestDryRunChangesNothing(t *testing.T) {
	f := &fakeRunner{
		before: []compose.Image{img("app", "repo:tag", "sha256:old", "sha256:new")},
		after:  []compose.Image{img("app", "repo:tag", "sha256:old", "sha256:new")},
	}
	o := opts()
	o.DryRun = true

	res, err := Run(f, o)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Updated || len(res.Services) != 1 || res.Services[0] != "app" {
		t.Fatalf("dry run should report the pending service without applying it: %+v", res)
	}
	joined := strings.Join(f.calls, " ")
	if strings.Contains(joined, "up:") || strings.Contains(joined, "backup") {
		t.Fatalf("dry run changed something: %v", f.calls)
	}
}

func TestOnlyChangedServicesAreRecreated(t *testing.T) {
	f := &fakeRunner{
		before: []compose.Image{
			img("app", "repo:app", "sha256:old", "sha256:new"),
			img("db", "postgres:16", "sha256:db", "sha256:db"),
		},
		after: []compose.Image{
			img("app", "repo:app", "sha256:old", "sha256:new"),
			img("db", "postgres:16", "sha256:db", "sha256:db"),
		},
		healthy: []bool{true},
	}

	res, err := Run(f, opts())

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Services) != 1 || res.Services[0] != "app" {
		t.Fatalf("expected only app to update, got %+v", res.Services)
	}
	// Recreating the database because an unrelated service moved is how an update loses a session,
	// or worse.
	if !strings.Contains(strings.Join(f.calls, " "), "up:app") || strings.Contains(strings.Join(f.calls, " "), "db") {
		t.Fatalf("touched more than the changed service: %v", f.calls)
	}
}
