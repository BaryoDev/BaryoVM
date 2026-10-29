// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package repomove

import (
	"strings"
	"testing"
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
	for _, c := range []string{GetURLCmd(true, dir), StatusCmd(true, dir), SetURLCmd(true, dir, "https://h/o/r'; id #"), ReferencesCmd(true, dir), CredHelperCmd(true, dir), FindCmd(true, []string{dir})} {
		if !strings.HasPrefix(c, "sudo -n ") {
			t.Errorf("not elevated: %s", c)
		}
		if strings.Contains(c, "it's here") {
			t.Errorf("unquoted value in %s", c)
		}
	}
	if strings.HasPrefix(GetURLCmd(false, dir), "sudo") {
		t.Error("sudo without the stack setting")
	}
	if strings.Contains(CredApproveCmd(false, "/d"), "password") {
		t.Error("credential approve carries a value on its command line")
	}
}
