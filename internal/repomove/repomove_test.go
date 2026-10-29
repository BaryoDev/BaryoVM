// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package repomove

import (
	"strings"
	"testing"

	"github.com/BaryoDev/BaryoVM/internal/sshx"
)

func TestParseKindsAndSameRepository(t *testing.T) {
	cases := []struct {
		url  string
		kind Kind
		path string
	}{
		{"https://github.com/acme/app", GitHub, "acme/app"},
		{"https://github.com/acme/app.git", GitHub, "acme/app"},
		{"git@github.com:acme/app.git", GitHub, "acme/app"},
		{"ssh://git@github.com/acme/app", GitHub, "acme/app"},
		{"https://acme@dev.azure.com/acme/proj/_git/app", Azure, "acme/proj/app"},
		{"git@ssh.dev.azure.com:v3/acme/proj/app", Azure, "acme/proj/app"},
		{"https://gitlab.com/g/s/app", GitLab, "g/s/app"},
		{"git@git.example.com:team/app.git", Other, "team/app"},
	}
	for _, c := range cases {
		r, err := Parse(c.url)
		if err != nil || r.Kind != c.kind || r.Path != c.path {
			t.Errorf("%s: %+v %v", c.url, r, err)
		}
	}
	a, _ := Parse("https://github.com/Acme/App.git")
	b, _ := Parse("git@github.com:acme/app")
	if !Same(a, b) {
		t.Error("the same repository over https and ssh did not compare equal")
	}
	az1, _ := Parse("https://acme@dev.azure.com/acme/proj/_git/app")
	az2, _ := Parse("git@ssh.dev.azure.com:v3/acme/proj/app")
	if !Same(az1, az2) {
		t.Error("the two Azure URL forms did not compare equal")
	}
}

func TestParseRefusesCredentialsAndOptionLookalikes(t *testing.T) {
	for _, bad := range []string{
		"https://ghp_abcdef@github.com/o/r",
		"https://user:secret@git.example.com/o/r",
		"https://someone@dev.azure.com/acme/proj/_git/app", // a name that is not the organisation may be a PAT
		"--upload-pack=touch x",
		"-oProxyCommand=x",
		"",
		"not a url",
	} {
		_, err := Parse(bad)
		if err == nil {
			t.Errorf("accepted %q", bad)
			continue
		}
		if strings.Contains(err.Error(), "ghp_abcdef") || strings.Contains(err.Error(), "secret") {
			t.Errorf("error echoes the credential: %v", err)
		}
	}
}

func TestDiffNamesWhatIsMissingChangedAndExtra(t *testing.T) {
	old := Refs{"refs/heads/main": "1", "refs/tags/v1": "2", "refs/heads/dev": "3"}
	now := Refs{"refs/heads/main": "9", "refs/heads/dev": "3", "refs/heads/new": "4"}
	d := Diff(old, now)
	if len(d.Missing) != 1 || d.Missing[0] != "refs/tags/v1" || len(d.Changed) != 1 || len(d.Extra) != 1 {
		t.Fatalf("%+v", d)
	}
	if !Diff(old, old).Empty() {
		t.Error("equal lists differ")
	}
}

func TestParseRefsSkipsNoise(t *testing.T) {
	sha := strings.Repeat("a", 40)
	got := ParseRefs("warning: redirecting\n" + sha + "\trefs/heads/main\n" + sha + "\trefs/tags/v1^{}\nfatal-ish line\n")
	if len(got) != 2 || got["refs/heads/main"] != sha {
		t.Errorf("%v", got)
	}
}

func TestPushNeverForcesOrDeletes(t *testing.T) {
	for _, a := range PushArgv("/tmp/x", "https://h/o/r") {
		if a == "--mirror" || a == "--force" || a == "-f" || strings.HasPrefix(a, "+") || a == "--delete" {
			t.Errorf("push argv carries %q", a)
		}
	}
}

func TestRemoteCommandsQuoteEveryValue(t *testing.T) {
	dir := "/opt/it's here; rm -rf x"
	for _, c := range []string{GetURLCmd(true, "app", dir), StatusCmd(true, "app", dir), SetURLCmd(true, "app", dir, "https://h/o/r'; id #"), ReferencesCmd(true, dir), OwnerCmd(true, dir), FindCmd(true, []string{dir})} {
		if !strings.HasPrefix(c, "sudo -n ") {
			t.Errorf("not run with sudo: %s", c)
		}
		if strings.Contains(c, "it's here") {
			t.Errorf("unquoted value in %s", c)
		}
	}
	if strings.HasPrefix(GetURLCmd(false, "app", dir), "sudo") {
		t.Error("sudo without the stack setting")
	}
	ap, err := CredApproveCmd(false, "app", "/d")
	if err != nil || strings.Contains(ap, "password") {
		t.Errorf("credential approve carries a value on its command line: %v", err)
	}
}

func TestGitRunsAsTheOwnerWithCodeRunningConfigBlanked(t *testing.T) {
	helper, _ := CredHelperCmd(true, "deploy", "/opt/app")
	approve, _ := CredApproveCmd(true, "deploy", "/opt/app")
	for _, c := range []string{GetURLCmd(true, "deploy", "/opt/app"), StatusCmd(true, "deploy", "/opt/app"), SetURLCmd(true, "deploy", "/opt/app", "u"), helper, approve} {
		for _, must := range []string{"sudo -n -u 'deploy' ", "GIT_CONFIG_NOSYSTEM=1", "core.fsmonitor=", "core.hooksPath=/dev/null"} {
			if !strings.Contains(c, must) {
				t.Errorf("%q missing from %s", must, c)
			}
		}
		if strings.Contains(c, "safe.directory") {
			t.Errorf("safe.directory override in %s", c)
		}
	}
}

func TestCredentialHelperValueQuotesAHostilePath(t *testing.T) {
	if _, err := CredHelperCmd(true, "app", "/opt/a b;touch x"); err == nil {
		t.Error("a hostile directory was accepted for the credential helper")
	}
	if _, err := CredApproveCmd(true, "app", "/opt/$(id)"); err == nil {
		t.Error("a hostile directory was accepted for credential approve")
	}
	c, err := CredHelperCmd(false, "app", "/opt/app")
	if err != nil {
		t.Fatal(err)
	}
	// the helper value is quoted for git's shell, then again for the remote shell
	if !strings.Contains(c, sshx.Quote("store --file="+sshx.Quote("/opt/app/.git/baryovm-credentials"))) {
		t.Errorf("helper value not quoted twice: %s", c)
	}
	ap, _ := CredApproveCmd(false, "app", "/opt/app")
	if !strings.Contains(ap, "-L ") || !strings.Contains(ap, "umask 077") {
		t.Errorf("approve does not refuse a symlink under umask 077: %s", ap)
	}
}

func TestParseFindsSplitsOnNULAndDropsTrickyNames(t *testing.T) {
	out := "/opt/app/.git\x00/opt/evil\n/etc/.git\x00/opt/../etc/x/.git\x00/opt/a/../b/.git\x00/opt/ok two/.git\x00/opt/bell\x07/.git\x00/opt/app/.git\x00"
	got := ParseFinds(out)
	want := []string{"/opt/app", "/opt/ok two"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("got %q want %q", got, want)
	}
	if !strings.Contains(FindCmd(false, []string{"/opt"}), "-print0") {
		t.Error("find does not use -print0")
	}
}

func TestParseOwnerRefusesMixedOrMissingOwners(t *testing.T) {
	if o, err := ParseOwner("deploy\ndeploy\n"); err != nil || o != "deploy" {
		t.Errorf("%q %v", o, err)
	}
	for _, bad := range []string{"", "deploy\nroot\n", "deploy\n", "a b\na b\n", "-x\n-x\n"} {
		if _, err := ParseOwner(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
