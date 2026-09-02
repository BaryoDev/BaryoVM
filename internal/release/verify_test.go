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

// The README has claimed for a long time that the compose dir is never in the sync list, so
// --delete cannot wipe a .env. Until now that was a convention rather than a check.
func TestASyncEntryCoveringTheComposeDirIsRefused(t *testing.T) {
	m := &Manifest{RemoteRoot: "/opt/app", Sync: []string{"deploy"}}

	err := m.CheckComposeDir("/opt/app/deploy")

	if err == nil {
		t.Fatal("expected a refusal: rsync --delete would reach the .env")
	}
	if !strings.Contains(err.Error(), ".env") {
		t.Fatalf("the error should say what is at risk, got %q", err)
	}
}

// A trailing slash syncs CONTENTS to remoteRoot itself, so it covers everything under it including
// a compose dir one level down. This is the case the trailing-slash rule makes easy to miss.
func TestATrailingSlashSyncCoveringTheComposeDirIsRefused(t *testing.T) {
	m := &Manifest{RemoteRoot: "/var/www/site", Sync: []string{"out/"}}

	if err := m.CheckComposeDir("/var/www/site/deploy"); err == nil {
		t.Fatal("expected a refusal: out/ lands on remoteRoot, which contains the compose dir")
	}
}

// The positive control. Without it a checker that refuses everything would pass the two above and
// make every release impossible.
func TestAnOrdinarySyncIsAllowed(t *testing.T) {
	m := &Manifest{RemoteRoot: "/opt/app", Sync: []string{"api", "web"}}

	if err := m.CheckComposeDir("/opt/app/deploy"); err != nil {
		t.Fatalf("syncing siblings of the compose dir must be allowed, got %v", err)
	}
}

// A path that merely shares a prefix is not inside it. Refusing this would be a false positive that
// blocks a legitimate layout.
func TestASimilarlyNamedSiblingIsNotTreatedAsTheComposeDir(t *testing.T) {
	m := &Manifest{RemoteRoot: "/opt/app", Sync: []string{"deploy-assets"}}

	if err := m.CheckComposeDir("/opt/app/deploy"); err != nil {
		t.Fatalf("deploy-assets does not contain deploy, got %v", err)
	}
}

func TestNoComposeDirSkipsTheCheck(t *testing.T) {
	m := &Manifest{RemoteRoot: "/var/www/site", Sync: []string{"out/"}}

	if err := m.CheckComposeDir(""); err != nil {
		t.Fatalf("a stack with no compose dir has nothing to protect, got %v", err)
	}
}

func TestSyncDestFollowsRsyncsTrailingSlashRule(t *testing.T) {
	m := &Manifest{RemoteRoot: "/opt/app"}

	if got := m.SyncDest("api"); got != "/opt/app/api" {
		t.Fatalf(`"api" should land at /opt/app/api, got %q`, got)
	}
	if got := m.SyncDest("out/"); got != "/opt/app" {
		t.Fatalf(`"out/" copies contents to the root, got %q`, got)
	}
}

// A static site's stack directory is its webroot, so remoteRoot and the stack dir are the same path
// by design. Refusing that broke every noCompose stack in 0.2.0: barakocms-site, baryo-web and
// rnxjs-samples all failed to release with a message about a compose directory none of them have.
func TestAStaticSiteIsNotBlockedByTheComposeGuard(t *testing.T) {
	m := &Manifest{RemoteRoot: "/var/www/site", Sync: []string{"out/"}, NoCompose: true}

	if err := m.CheckComposeDir("/var/www/site"); err != nil {
		t.Fatalf("a noCompose stack has no .env to protect, got %v", err)
	}
}

// The positive control for the exemption. A compose stack with the same shape must still be caught,
// or the fix would have removed the protection rather than narrowed it.
func TestAComposeStackWithTheSameShapeIsStillBlocked(t *testing.T) {
	m := &Manifest{RemoteRoot: "/opt/app", Sync: []string{"deploy/"}}

	if err := m.CheckComposeDir("/opt/app"); err == nil {
		t.Fatal("a compose stack syncing onto its own dir must still be refused")
	}
}

// Verify and postDeploy commands are shell strings written against the repository, the way every
// other path in the manifest is. They ran from the SSH login shell's home instead, so
// "sh scripts/check-live-demo.sh" exited 127 and the release reported "released but failed
// verification, the stack may need attention" for a stack that was perfectly healthy.
func TestVerifyCommandsRunFromTheRemoteRoot(t *testing.T) {
	m := &Manifest{RemoteRoot: "/home/opc/app", Verify: []string{"sh scripts/check.sh"}}

	p := m.Verification("")

	want := "cd '/home/opc/app' && sh scripts/check.sh"
	if len(p.Commands) != 1 || p.Commands[0] != want {
		t.Fatalf("want %q, got %v", want, p.Commands)
	}
}

func TestPostDeployCommandsRunFromTheRemoteRoot(t *testing.T) {
	m := &Manifest{RemoteRoot: "/home/opc/app", PostDeploy: []string{"nginx -t", "systemctl reload nginx"}}

	got := m.PostDeployCmds()

	want := []string{
		"cd '/home/opc/app' && nginx -t",
		"cd '/home/opc/app' && systemctl reload nginx",
	}
	if len(got) != len(want) {
		t.Fatalf("want %d commands, got %v", len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("command %d: want %q, got %q", i, want[i], got[i])
		}
	}
}

// A remoteRoot carrying shell syntax must not escape into the command line, for the same reason the
// healthUrl is quoted.
func TestARemoteRootCarryingShellSyntaxIsQuoted(t *testing.T) {
	m := &Manifest{RemoteRoot: "/tmp/a b; rm -rf x", Verify: []string{"true"}}

	got := m.Verification("").Commands[0]

	if !strings.HasPrefix(got, "cd '/tmp/a b; rm -rf x' && ") {
		t.Fatalf("remoteRoot must be single-quoted, got %q", got)
	}
	// The ; must not be able to end the cd and start a second command.
	if strings.Contains(got, "; rm -rf x' &&") == false {
		t.Fatalf("the semicolon escaped its quotes: %q", got)
	}
}

// remoteRoot is written with or without a trailing slash depending on who wrote the manifest, and
// SyncDest already trims it. The cd has to agree, or the same manifest means two directories.
func TestATrailingSlashOnRemoteRootIsTrimmed(t *testing.T) {
	m := &Manifest{RemoteRoot: "/home/opc/app/", Verify: []string{"true"}}

	want := "cd '/home/opc/app' && true"
	if got := m.Verification("").Commands[0]; got != want {
		t.Fatalf("want %q, got %q", want, got)
	}
}

// The control. A manifest with no remoteRoot has nowhere to cd to, and prefixing a cd to an empty
// path would break every command rather than fix its working directory.
func TestCommandsAreLeftAloneWithoutARemoteRoot(t *testing.T) {
	m := &Manifest{Verify: []string{"sh scripts/check.sh"}}

	if got := m.Verification("").Commands[0]; got != "sh scripts/check.sh" {
		t.Fatalf("want the command unchanged, got %q", got)
	}
	if got := (&Manifest{PostDeploy: []string{"nginx -t"}}).PostDeployCmds(); got[0] != "nginx -t" {
		t.Fatalf("want the command unchanged, got %q", got[0])
	}
}

// "/" is a legal absolute path, and trimming its trailing slash leaves nothing. Treated as an empty
// remoteRoot the command is left unwrapped, so it runs in the login shell's home: the exact bug this
// change exists to fix, reappearing for the one root that is all slash.
func TestASlashRemoteRootIsPreserved(t *testing.T) {
	m := &Manifest{RemoteRoot: "/", Verify: []string{"true"}}

	want := "cd '/' && true"
	if got := m.Verification("").Commands[0]; got != want {
		t.Fatalf("want %q, got %q", want, got)
	}
}
