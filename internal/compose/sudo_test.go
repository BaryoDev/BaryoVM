package compose

import (
	"strings"
	"testing"
)

// A stack whose .env is root-owned (the right posture for a file holding a database password) cannot
// be driven by an ordinary SSH user: compose reads that file, so every command fails with "permission
// denied" even though Docker itself is reachable. Sudo is how such a stack stays managed without
// loosening the file. These pin that every command path honours the flag; one that forgot would fail
// only against a real host, which is exactly where it is most expensive to find out.
func TestSudoAppliesToComposeCommands(t *testing.T) {
	plain := Stack{Dir: "/opt/app"}
	elevated := Stack{Dir: "/opt/app", Sudo: true}

	// Assert on a whole command rather than on base(), which is only the prefix. An earlier
	// version of this elevated the prefix and left the subcommand outside the quoted shell,
	// producing `sudo -n sh -c '... docker compose' ps -q 'api'`, and a test reading base()
	// could not see it.
	if got := plain.PsQuietCmd(nil); strings.Contains(got, "sudo") {
		t.Errorf("a stack without Sudo must not elevate: %q", got)
	}
	got := elevated.PsQuietCmd(nil)
	if !strings.Contains(got, "docker compose ps -q") {
		t.Errorf("the subcommand must be inside the elevated command, got %q", got)
	}
	// -n so a host that would prompt fails loudly rather than hanging a non-interactive session.
	if !strings.HasPrefix(got, "sudo -n ") {
		t.Errorf("sudo must be non-interactive and wrap the whole command: %q", got)
	}
	// The cd is inside the elevation: the directory is usually the root-owned one that made
	// Sudo necessary, so an unelevated cd fails before sudo is reached.
	if strings.HasPrefix(got, "cd ") {
		t.Errorf("the cd must run as root too: %q", got)
	}
}

func TestSudoAppliesToPlainDockerCommands(t *testing.T) {
	// Rollback retags images with `docker tag`, and detection inspects them. Those are not compose
	// calls, so they need elevating separately. Miss one and a rollback fails at the worst moment.
	if got := (Stack{Dir: "/opt/app"}).docker(); got != "docker" {
		t.Errorf("unelevated: got %q", got)
	}
	if got := (Stack{Dir: "/opt/app", Sudo: true}).docker(); got != "sudo -n docker" {
		t.Errorf("elevated: got %q", got)
	}
}

func TestSudoStillHonoursAnExplicitComposeFile(t *testing.T) {
	s := Stack{Dir: "/opt/app", File: "docker-compose.prod.yml", Sudo: true}

	got := s.PsQuietCmd(nil)
	if !strings.Contains(got, "docker compose -f ") {
		t.Errorf("the file flag must follow compose, got %q", got)
	}
	if strings.Index(got, "sudo") > strings.Index(got, "docker compose") {
		t.Errorf("sudo must precede the command it elevates: %q", got)
	}
}

func TestDirIsQuotedSoAPathWithSpacesSurvives(t *testing.T) {
	s := Stack{Dir: "/opt/my app", Sudo: true}

	if !strings.Contains(s.base(), "'/opt/my app'") {
		t.Errorf("project dir must be quoted: %q", s.base())
	}
}
