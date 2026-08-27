package release

import (
	"strings"
	"testing"
	"time"
)

func TestVerificationPrefersManifestCommandsOverHealthUrl(t *testing.T) {
	m := &Manifest{Verify: []string{"sh scripts/check.sh"}}

	p := m.Verification("http://127.0.0.1:5005/health")

	if len(p.Commands) != 1 || p.Commands[0] != "sh scripts/check.sh" {
		t.Fatalf("expected the manifest's own command, got %v", p.Commands)
	}
	// A repository that already has a check script knows more about what healthy means than a
	// single URL does, so the healthUrl must not be appended alongside it.
	for _, c := range p.Commands {
		if strings.Contains(c, "curl") {
			t.Fatalf("healthUrl probe should not run when verify is set: %v", p.Commands)
		}
	}
}

func TestVerificationFallsBackToTheHealthUrl(t *testing.T) {
	m := &Manifest{}

	p := m.Verification("http://127.0.0.1:5005/health")

	if len(p.Commands) != 1 {
		t.Fatalf("expected one probe, got %v", p.Commands)
	}
	got := p.Commands[0]
	for _, want := range []string{"curl", "--fail", "'http://127.0.0.1:5005/health'"} {
		if !strings.Contains(got, want) {
			t.Fatalf("probe %q is missing %q", got, want)
		}
	}
}

// --fail is what makes the probe mean anything. Without it curl exits zero on a 500, so a stack
// serving errors would verify as healthy, which is the failure this whole feature exists to catch.
func TestTheProbeFailsOnAnErrorStatus(t *testing.T) {
	p := (&Manifest{}).Verification("http://x/health")

	if !strings.Contains(p.Commands[0], "--fail") {
		t.Fatalf("probe must use --fail, got %q", p.Commands[0])
	}
}

func TestAHealthUrlCarryingShellSyntaxIsQuoted(t *testing.T) {
	p := (&Manifest{}).Verification("http://x/health?a=1&rm=-rf")

	got := p.Commands[0]
	if !strings.Contains(got, "'http://x/health?a=1&rm=-rf'") {
		t.Fatalf("url must be single-quoted, got %q", got)
	}
	// The & must not be able to background the command.
	if strings.Contains(got, "& ") {
		t.Fatalf("unquoted & reached the command line: %q", got)
	}
}

// Nothing configured means nothing to run, and the caller has to be able to tell that apart from a
// plan that passed. An empty command list is how a release knows to say it verified nothing.
func TestNoVerifyAndNoHealthUrlProducesNoCommands(t *testing.T) {
	p := (&Manifest{}).Verification("")

	if len(p.Commands) != 0 {
		t.Fatalf("expected no commands, got %v", p.Commands)
	}
}

func TestVerificationHasWorkableDefaults(t *testing.T) {
	p := (&Manifest{}).Verification("http://x/health")

	if p.Attempts < 2 {
		t.Fatalf("a single attempt cannot tolerate a restarting service, got %d", p.Attempts)
	}
	if p.Delay <= 0 {
		t.Fatalf("delay must be positive, got %v", p.Delay)
	}
}

func TestVerificationHonoursConfiguredAttemptsAndDelay(t *testing.T) {
	m := &Manifest{VerifyAttempts: 9, VerifyDelaySeconds: 7}

	p := m.Verification("http://x/health")

	if p.Attempts != 9 {
		t.Fatalf("attempts: want 9, got %d", p.Attempts)
	}
	if p.Delay != 7*time.Second {
		t.Fatalf("delay: want 7s, got %v", p.Delay)
	}
}
