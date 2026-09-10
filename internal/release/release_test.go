// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package release

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDefaultsAndValidation(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "r.json")
	os.WriteFile(p, []byte(`{"localRoot":"~/src","remoteRoot":"/srv/app","sync":["api"],
		"builds":[{"image":"app:1","dockerfile":"Dockerfile","context":"api"}]}`), 0o600)

	m, err := Load(p, false)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Exclude defaults when omitted.
	if len(m.Exclude) == 0 || m.Exclude[0] != "bin" {
		t.Errorf("expected default excludes, got %v", m.Exclude)
	}

	// Missing required fields must error.
	bad := filepath.Join(dir, "bad.json")
	os.WriteFile(bad, []byte(`{"sync":["api"]}`), 0o600)
	if _, err := Load(bad, false); err == nil {
		t.Error("expected error for missing localRoot/remoteRoot")
	}
}

func TestBuildCmd(t *testing.T) {
	m := &Manifest{RemoteRoot: "/srv/app"}
	got := m.BuildCmd(Build{
		Image: "app:1", Dockerfile: "deploy/Dockerfile.api", Context: "api",
		Args: map[string]string{"NEXT_PUBLIC_API_URL": "", "MODE": "prod"},
	})
	for _, want := range []string{
		"docker build",
		"-f '/srv/app/deploy/Dockerfile.api'",
		"-t 'app:1'",
		"'/srv/app/api'",
		"--build-arg 'MODE=prod'",
		"--build-arg 'NEXT_PUBLIC_API_URL='",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("BuildCmd missing %q in:\n%s", want, got)
		}
	}
	// Deterministic arg order (sorted): MODE before NEXT_PUBLIC…
	if strings.Index(got, "MODE=") > strings.Index(got, "NEXT_PUBLIC") {
		t.Errorf("build args not in deterministic order:\n%s", got)
	}
	if strings.Contains(got, "--no-cache") {
		t.Error("unexpected --no-cache")
	}
	if !strings.Contains(m.BuildCmd(Build{Image: "x", NoCache: true}), "--no-cache") {
		t.Error("expected --no-cache when set")
	}
}

func TestRsyncCmd(t *testing.T) {
	m := &Manifest{
		LocalRoot: "/local/src", RemoteRoot: "/srv/app",
		Exclude: []string{"bin", ".git"},
	}
	c := m.RsyncCmd("api", "opc", "1.2.3.4", 22, "/keys/id")
	args := strings.Join(c.Args, " ")
	for _, want := range []string{
		"--delete",
		"--exclude bin",
		"--exclude .git",
		"ssh -i '/keys/id' -p 22",
		"/local/src/api",
		"opc@1.2.3.4:/srv/app/",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("RsyncCmd missing %q in: %s", want, args)
		}
	}
}

// The VM's port has to reach rsync. Without it a VM registered on 2222 runs its SSH commands on
// 2222 and its file transfer on 22, so the release either fails or lands on whatever answers 22.
func TestRsyncCmdUsesTheVMPort(t *testing.T) {
	m := &Manifest{LocalRoot: "/local/src", RemoteRoot: "/srv/app"}

	args := strings.Join(m.RsyncCmd("api", "opc", "1.2.3.4", 2222, "/keys/id").Args, " ")
	if !strings.Contains(args, "-p 2222") {
		t.Fatalf("port 2222 not passed to ssh: %s", args)
	}
	if strings.Contains(args, "-p 22 ") {
		t.Fatalf("port 22 used for a VM on 2222: %s", args)
	}

	// An unset port is the same default sshx.Dial uses, not a missing -p.
	zero := strings.Join(m.RsyncCmd("api", "opc", "1.2.3.4", 0, "/keys/id").Args, " ")
	if !strings.Contains(zero, "-p 22") {
		t.Fatalf("unset port should default to 22: %s", zero)
	}
}

// rsync splits the -e string with its own tokenizer before exec'ing ssh, so a key path with a space
// in it has to be quoted or rsync reads it as two arguments.
func TestRsyncCmdQuotesAndExpandsTheKeyPath(t *testing.T) {
	m := &Manifest{LocalRoot: "/local/src", RemoteRoot: "/srv/app"}

	args := strings.Join(m.RsyncCmd("api", "opc", "h", 22, "/keys/my key").Args, " ")
	if !strings.Contains(args, `ssh -i '/keys/my key'`) {
		t.Fatalf("key path with a space is not quoted: %s", args)
	}

	// Expanded here so the path that gets quoted is the path that gets opened, rather than leaning on
	// ssh's own tilde handling for -i.
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory: %v", err)
	}
	tilde := strings.Join(m.RsyncCmd("api", "opc", "h", 22, "~/.ssh/id").Args, " ")
	if !strings.Contains(tilde, "'"+home+"/.ssh/id'") {
		t.Fatalf("~ in the key path was not expanded: %s", tilde)
	}
}

// rsync is not a shell: one literal quote inside a quoted run is written by doubling it. Hand rsync
// the shell's close-escape-reopen form instead and it exits with "Missing trailing-' in remote-shell
// command" before ssh is ever reached, which is the helper's whole job getting the one input it
// exists for wrong.
func TestRsyncCmdQuotesAKeyPathTheWayRsyncParsesIt(t *testing.T) {
	m := &Manifest{LocalRoot: "/local/src", RemoteRoot: "/srv/app"}

	args := strings.Join(m.RsyncCmd("api", "opc", "h", 22, "/keys/arnel's key").Args, " ")

	if !strings.Contains(args, `ssh -i '/keys/arnel''s key'`) {
		t.Fatalf("quote not doubled for rsync's parser: %s", args)
	}
	if strings.Contains(args, `'\''`) {
		t.Fatalf("the shell's escape is a syntax error to rsync: %s", args)
	}
	// What rsync's tokenizer does with the quoted form, so the expectation above is not just a string
	// this test and the code happen to agree on.
	if got := parseRsyncShellArgs(`ssh -i '/keys/arnel''s key' -p 22`); got[2] != "/keys/arnel's key" {
		t.Fatalf("rsync would open %q", got[2])
	}
}

// parseRsyncShellArgs mirrors rsync's own tokenizer for the -e value (main.c: split on spaces, a
// quoted run ends at a single quote unless it is doubled, in which case one literal quote is kept).
func parseRsyncShellArgs(s string) []string {
	var args []string
	var cur strings.Builder
	quote := byte(0)
	started := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ' ' && quote == 0 {
			if started {
				args = append(args, cur.String())
				cur.Reset()
				started = false
			}
			continue
		}
		started = true
		if c == '\'' || c == '"' {
			if quote == 0 {
				quote = c
				continue
			}
			if c == quote {
				if i+1 < len(s) && s[i+1] == quote {
					cur.WriteByte(c)
					i++
					continue
				}
				quote = 0
				continue
			}
		}
		cur.WriteByte(c)
	}
	if started {
		args = append(args, cur.String())
	}
	return args
}

// A stack whose .env or webroot is root-owned is registered `--sudo`, and `stack release` built its
// images anyway, so on the one kind of host where the flag is needed the main command failed at the
// build step with a Docker permission error. The build and the compose up next to it now read the
// same decision.
func TestBuildCmdRunsAsRootWhenTheReleaseDoes(t *testing.T) {
	m := &Manifest{RemoteRoot: "/srv/app", Sudo: true}

	got := m.BuildCmd(Build{Image: "app:1", Dockerfile: "Dockerfile", Context: "."})

	if !strings.HasPrefix(got, "sudo -n docker build") {
		t.Fatalf("a release that needs root must build as root: %s", got)
	}
	// -n specifically: there is no terminal under -o json to answer a prompt.
	if strings.Contains(got, "sudo docker build") {
		t.Fatalf("sudo must be non-interactive: %s", got)
	}
}

func TestBuildCmdIsUntouchedWithoutSudo(t *testing.T) {
	m := &Manifest{RemoteRoot: "/srv/app"}

	got := m.BuildCmd(Build{Image: "app:1", Dockerfile: "Dockerfile", Context: "."})

	if strings.Contains(got, "sudo") {
		t.Fatalf("a plain release must not ask for root: %s", got)
	}
	if !strings.HasPrefix(got, "docker build") {
		t.Fatalf("want a plain docker build, got %s", got)
	}
}

// The stack's own registration is the other half of it: a manifest can say nothing about sudo and
// still be released for a stack that needs it.
func TestAStackRegisteredSudoReleasesAsRoot(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "r.json")
	os.WriteFile(p, []byte(`{"localRoot":"/src","remoteRoot":"/opt/app","sync":["api"],
		"postDeploy":["restorecon -R /opt/app"]}`), 0o600)

	m, err := Load(p, true)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !m.Sudo {
		t.Fatal("the stack is registered --sudo, so its release runs as root")
	}
	if got := m.BuildCmd(Build{Image: "app:1"}); !strings.HasPrefix(got, "sudo -n docker build") {
		t.Fatalf("build step ignored the stack's setting: %s", got)
	}
	if got := m.PostDeployCmds()[0]; !strings.Contains(got, "sudo -n ") {
		t.Fatalf("postDeploy ignored the stack's setting: %s", got)
	}
	if got := strings.Join(m.RsyncCmd("api", "opc", "h", 22, "/k").Args, " "); !strings.Contains(got, "--rsync-path=sudo -n rsync") {
		t.Fatalf("rsync ignored the stack's setting: %s", got)
	}
}

// The whole point of postDeploy is restorecon, nginx -t and systemctl reload, and all three need
// root. They ran as the SSH user, after the rsync had already landed, so the release failed with the
// files in place and nothing to say whether the site was old, new or half of each.
func TestPostDeployRunsAsRootWhenTheReleaseDoes(t *testing.T) {
	m := &Manifest{RemoteRoot: "/var/www/site", Sudo: true,
		PostDeploy: []string{"restorecon -R /var/www/site", "systemctl reload nginx"}}

	got := m.PostDeployCmds()

	want := []string{
		`sudo -n "${SHELL:-/bin/sh}" -c 'cd '\''/var/www/site'\'' && restorecon -R /var/www/site'`,
		`sudo -n "${SHELL:-/bin/sh}" -c 'cd '\''/var/www/site'\'' && systemctl reload nginx'`,
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

func TestPreDeployRunsAsRootWhenTheReleaseDoes(t *testing.T) {
	m := &Manifest{RemoteRoot: "/opt/app", Sudo: true, PreDeploy: []string{"grep -q MODE=prod .env"}}

	got := m.PreDeployCmds()

	want := `sudo -n "${SHELL:-/bin/sh}" -c 'cd '\''/opt/app'\'' && grep -q MODE=prod .env'`
	if len(got) != 1 || got[0] != want {
		t.Fatalf("want %q, got %v", want, got)
	}
}

// One shell, not a prefix. `sudo -n nginx -t && systemctl reload nginx` would run the reload as the
// SSH user, which fails on its own and leaves exactly the half-applied state the hook exists to
// avoid.
func TestACompoundHookRunsEntirelyAsRoot(t *testing.T) {
	m := &Manifest{RemoteRoot: "/var/www/site", Sudo: true,
		PostDeploy: []string{"nginx -t && systemctl reload nginx"}}

	got := m.PostDeployCmds()[0]

	want := `sudo -n "${SHELL:-/bin/sh}" -c 'cd '\''/var/www/site'\'' && nginx -t && systemctl reload nginx'`
	if got != want {
		t.Fatalf("want %q, got %q", want, got)
	}
}

// Manifests written while this was broken put sudo in each command by hand, and those strings are
// wrapped like every other one. Sudo inside sudo authorises and execs, so it costs nothing; leaving
// such a string alone costs the bug, because "sudo -n nginx -t && systemctl reload nginx" starts
// with sudo and is still half unprivileged, and a hook written with the prefix by hand is the one
// most likely to be compound.
func TestAHookThatAlreadySaysSudoIsStillWrappedWhole(t *testing.T) {
	m := &Manifest{RemoteRoot: "/var/www/site", Sudo: true,
		PostDeploy: []string{"sudo -n nginx -t && systemctl reload nginx"}}

	got := m.PostDeployCmds()[0]

	want := `sudo -n "${SHELL:-/bin/sh}" -c 'cd '\''/var/www/site'\'' && sudo -n nginx -t && systemctl reload nginx'`
	if got != want {
		t.Fatalf("want %q, got %q", want, got)
	}
}

// The cd belongs inside the root shell. `cd <root> && sudo -n "${SHELL:-/bin/sh}" -c '<cmd>'` runs the cd as the SSH
// user, so a root-owned mode 700 remoteRoot, the posture this flag exists for, fails at the cd with
// Permission denied and the elevated hook never runs, with the sync already landed.
func TestAnElevatedHookEntersRemoteRootAsRoot(t *testing.T) {
	m := &Manifest{RemoteRoot: "/var/www/site", Sudo: true, PostDeploy: []string{"nginx -t"}}

	got := m.PostDeployCmds()[0]

	if strings.HasPrefix(got, "cd ") {
		t.Fatalf("the cd runs as the SSH user, which is the half that cannot enter the root: %s", got)
	}
	want := `sudo -n "${SHELL:-/bin/sh}" -c 'cd '\''/var/www/site'\'' && nginx -t'`
	if got != want {
		t.Fatalf("want %q, got %q", want, got)
	}
}

// The wrapper shape decides a hook's environment as much as its privilege: under `sudo -n "${SHELL:-/bin/sh}" -c`,
// sudo's env_reset and secure_path apply, so $PATH is root's secure_path and $HOME is /root, and a
// hook calling a per-user tool exits 127 where it used to work. That is written down on
// Manifest.Sudo, and pinned here so the shape cannot change back without a test saying so.
func TestAnElevatedHookRunsUnderOneRootShell(t *testing.T) {
	m := &Manifest{RemoteRoot: "/opt/app", Sudo: true,
		PostDeploy: []string{"npm run build && ./deploy.sh"}}

	got := m.PostDeployCmds()[0]

	if !strings.HasPrefix(got, "sudo -n \"${SHELL:-/bin/sh}\" -c '") || !strings.HasSuffix(got, "'") {
		t.Fatalf("a hook must arrive as one quoted argument to one root shell: %s", got)
	}
	// One shell, not one per && , which is the failure this wrapper exists to prevent.
	if n := strings.Count(got, " -c '"); n != 1 {
		t.Fatalf("one shell for the whole hook, got %d in %s", n, got)
	}
	// The login shell, not dash. `sudo -n sh -c` is dash on Debian and Ubuntu, so a hook using
	// [[ ]], source or pipefail would exit 127 after this change and not before it.
	if strings.Contains(got, "sudo -n sh -c") {
		t.Fatalf("an elevated hook must keep the shell an unelevated one would have got: %s", got)
	}
}

// A hook carrying a quote must still arrive as one argument to sh -c.
func TestAHookCarryingAQuoteIsQuotedForTheRootShell(t *testing.T) {
	m := &Manifest{RemoteRoot: "/opt/app", Sudo: true,
		PostDeploy: []string{`sh -c 'echo hi'`}}

	got := m.PostDeployCmds()[0]

	want := `sudo -n "${SHELL:-/bin/sh}" -c 'cd '\''/opt/app'\'' && sh -c '\''echo hi'\'''`
	if got != want {
		t.Fatalf("want %q, got %q", want, got)
	}
}

// Verify probes the running site, so it stays as the SSH user. Pinned because it is a decision, not
// an oversight: the next reader should see that it was chosen.
func TestVerifyIsNotRunAsRoot(t *testing.T) {
	m := &Manifest{RemoteRoot: "/opt/app", Sudo: true, Verify: []string{"sh scripts/check.sh"}}

	got := m.Verification("").Commands

	want := "cd '/opt/app' && sh scripts/check.sh"
	if len(got) != 1 || got[0] != want {
		t.Fatalf("want %q, got %v", want, got)
	}
}

// The failure mode of a fix like this is running things as root that nobody asked to. A manifest
// with no sudo, released for a stack with no --sudo, must produce what it produced before.
func TestAReleaseWithoutSudoIsUnchangedEverywhere(t *testing.T) {
	m := &Manifest{LocalRoot: "/src", RemoteRoot: "/opt/app", Sync: []string{"api"},
		PreDeploy: []string{"test -f .env"}, PostDeploy: []string{"nginx -t", "systemctl reload nginx"},
		Verify: []string{"sh scripts/check.sh"}}

	var cmds []string
	cmds = append(cmds, m.BuildCmd(Build{Image: "app:1", Dockerfile: "Dockerfile", Context: "api"}))
	cmds = append(cmds, m.PreDeployCmds()...)
	cmds = append(cmds, m.PostDeployCmds()...)
	cmds = append(cmds, m.Verification("http://127.0.0.1:5005/health").Commands...)
	cmds = append(cmds, strings.Join(m.RsyncCmd("api", "opc", "h", 22, "/k").Args, " "))

	if len(cmds) != 6 {
		t.Fatalf("expected every step to produce a command, got %v", cmds)
	}
	for _, c := range cmds {
		if strings.Contains(c, "sudo") {
			t.Fatalf("nothing asked for root, but a step asks: %s", c)
		}
		if strings.Contains(c, "sh -c") {
			t.Fatalf("a plain release must not be rewrapped in a shell: %s", c)
		}
	}
	if cmds[1] != "cd '/opt/app' && test -f .env" {
		t.Fatalf("preDeploy changed shape: %q", cmds[1])
	}
	if cmds[2] != "cd '/opt/app' && nginx -t" {
		t.Fatalf("postDeploy changed shape: %q", cmds[2])
	}
}

// The fold is one way, and that is a promise made in three doc comments and the flag help: a stack
// registered --sudo elevates its release even if the manifest says otherwise. The test that covered
// this used a manifest with no sudo key at all, which does not pin the claim as written.
func TestAManifestCannotTurnOffAStackRegisteredSudo(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "release.json")
	if err := os.WriteFile(path, []byte(`{"localRoot":".","remoteRoot":"/opt/app","sudo":false}`), 0o600); err != nil {
		t.Fatal(err)
	}

	m, err := Load(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Sudo {
		t.Fatal(`a stack registered --sudo releases as root, even when its manifest says "sudo": false`)
	}

	// And the other direction still holds: the manifest can ask for root on its own.
	if err := os.WriteFile(path, []byte(`{"localRoot":".","remoteRoot":"/opt/app","sudo":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err = Load(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Sudo {
		t.Fatal("a manifest asking for root gets it without the stack being registered --sudo")
	}
}
