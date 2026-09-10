package cli

import (
	"errors"
	"testing"
	"time"

	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"github.com/BaryoDev/BaryoVM/internal/update"
)

// The refusal has to happen before the SSH dial, like the autoUpdate and healthUrl ones. A cron
// entry pointed at a misconfigured stack should report the misconfiguration, not whatever the
// network said, and it can say so without connecting. The VM here has an unreadable key path, so a
// run that dials first fails with a key error instead of the reason the operator needs.
func TestAutoRefusesAStackWithNoDatabaseBeforeDialling(t *testing.T) {
	t.Setenv("BARYOVM_HOME", t.TempDir())
	s := &fleet.Store{}
	s.Upsert(fleet.VM{Name: "oracle", Host: "127.0.0.1", Port: 1, User: "deploy", KeyPath: "/nonexistent/key"})
	s.UpsertStack(fleet.Stack{
		Name: "barako", VM: "oracle", Dir: "/opt/barakocms",
		AutoUpdate: true, HealthURL: "http://127.0.0.1:8091/health",
	})
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	err := runStackUpdate("barako", nil, true, false, false, 1, time.Millisecond)

	if !errors.Is(err, update.ErrNoBackup) {
		t.Fatalf("want the no-backup refusal before any connection, got %v", err)
	}
}

// And the opt-out gets past that refusal, so a stack with no database is not blocked by the check
// before it ever reaches the update itself.
func TestNoDatabaseGetsPastTheCliRefusal(t *testing.T) {
	t.Setenv("BARYOVM_HOME", t.TempDir())
	s := &fleet.Store{}
	s.Upsert(fleet.VM{Name: "oracle", Host: "127.0.0.1", Port: 1, User: "deploy", KeyPath: "/nonexistent/key"})
	s.UpsertStack(fleet.Stack{
		Name: "site", VM: "oracle", Dir: "/var/www/site",
		AutoUpdate: true, HealthURL: "http://127.0.0.1:8080/", NoDatabase: true,
	})
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	err := runStackUpdate("site", nil, true, false, false, 1, time.Millisecond)

	if errors.Is(err, update.ErrNoBackup) {
		t.Fatalf("a stack that records it has no database must not be refused: %v", err)
	}
}
