package compose

import (
	"strings"
	"testing"
)

// A stack whose .env is root-owned (the right posture for a file holding a database password) cannot
// be driven by an ordinary SSH user: compose reads that file, so every command fails with "permission
// denied" even though Docker itself is reachable. Sudo is how such a stack stays managed without
// loosening the file. These pin that every command path honours the flag — one that forgot would fail
// only against a real host, which is exactly where it is most expensive to find out.
func TestSudoAppliesToComposeCommands(t *testing.T) {
	plain := Stack{Dir: "/opt/app"}
	elevated := Stack{Dir: "/opt/app", Sudo: true}

	if strings.Contains(plain.base(), "sudo") {
		t.Errorf("a stack without Sudo must not elevate: %q", plain.base())
	}
	if !strings.Contains(elevated.base(), "sudo -n docker compose") {
		t.Errorf("expected an elevated compose invocation, got %q", elevated.base())
	}
	// -n so a host that would prompt fails loudly rather than hanging a non-interactive session.
	if !strings.Contains(elevated.base(), "sudo -n") {
		t.Errorf("sudo must be non-interactive: %q", elevated.base())
	}
}

func TestSudoAppliesToPlainDockerCommands(t *testing.T) {
	// Rollback retags images with `docker tag`, and detection inspects them. Those are not compose
	// calls, so they need elevating separately — miss one and a rollback fails at the worst moment.
	if got := (Stack{Dir: "/opt/app"}).docker(); got != "docker" {
		t.Errorf("unelevated: got %q", got)
	}
	if got := (Stack{Dir: "/opt/app", Sudo: true}).docker(); got != "sudo -n docker" {
		t.Errorf("elevated: got %q", got)
	}
}

func TestSudoStillHonoursAnExplicitComposeFile(t *testing.T) {
	s := Stack{Dir: "/opt/app", File: "docker-compose.prod.yml", Sudo: true}

	base := s.base()
	if !strings.Contains(base, "sudo -n docker compose -f") {
		t.Errorf("expected sudo before compose and the file flag after it, got %q", base)
	}
	if strings.Index(base, "sudo") > strings.Index(base, "docker compose") {
		t.Errorf("sudo must precede the command it elevates: %q", base)
	}
}

func TestDirIsQuotedSoAPathWithSpacesSurvives(t *testing.T) {
	s := Stack{Dir: "/opt/my app", Sudo: true}

	if !strings.Contains(s.base(), "'/opt/my app'") {
		t.Errorf("project dir must be quoted: %q", s.base())
	}
}
