package cli

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestGHArgvIsASliceNotAShellString(t *testing.T) {
	cases := []struct {
		name string
		got  []string
		want []string
	}{
		{"visibility", ghVisibilityArgs("acme/app"), []string{"api", "repos/acme/app", "--jq", ".visibility"}},
		{"registration", ghTokenArgs("acme/app", "registration-token"),
			[]string{"api", "-X", "POST", "repos/acme/app/actions/runners/registration-token", "--jq", ".token"}},
		{"remove", ghTokenArgs("acme/app", "remove-token"),
			[]string{"api", "-X", "POST", "repos/acme/app/actions/runners/remove-token", "--jq", ".token"}},
		{"latest", ghReleaseArgs(""), []string{"api", "repos/actions/runner/releases/latest"}},
		{"pinned", ghReleaseArgs("2.320.0"), []string{"api", "repos/actions/runner/releases/tags/v2.320.0"}},
	}
	for _, c := range cases {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s: got %q, want %q", c.name, c.got, c.want)
		}
	}
}

func TestGHErrorAdvice(t *testing.T) {
	e := ghError("fetch a registration-token", "acme/app", "gh: Must have admin rights to Repository. (HTTP 403)", errors.New("exit 1"))
	if !strings.Contains(e.Error(), "repo admin") {
		t.Errorf("a 403 should say the token needs repo admin: %v", e)
	}
	e = ghError("check the repo", "acme/app", "gh: Not Found (HTTP 404)", errors.New("exit 1"))
	if !strings.Contains(e.Error(), "gh auth status") {
		t.Errorf("a 404 should point at gh auth status: %v", e)
	}
}

func TestParseRunnerRelease(t *testing.T) {
	raw := `{"tag_name":"v2.320.0","assets":[{"name":"actions-runner-linux-x64-2.320.0.tar.gz","digest":"sha256:abc"},{"name":"other.zip"}]}`
	rel, err := parseRunnerRelease(raw)
	if err != nil {
		t.Fatal(err)
	}
	if rel.Version != "2.320.0" || len(rel.Assets) != 2 || rel.Assets[0].Digest != "sha256:abc" || rel.Assets[1].Digest != "" {
		t.Errorf("release = %+v", rel)
	}
	if _, err := parseRunnerRelease("not json"); err == nil {
		t.Error("bad json was accepted")
	}
}

func TestSplitLabels(t *testing.T) {
	got := splitLabels(" docker, gpu ,,x ")
	if !reflect.DeepEqual(got, []string{"docker", "gpu", "x"}) {
		t.Errorf("got %q", got)
	}
	if len(splitLabels("")) != 0 {
		t.Error("empty input should give no labels")
	}
}

func TestRunnerCommandIsUnderVM(t *testing.T) {
	cmd, _, err := newRoot().Find([]string{"vm", "runner", "remove"})
	if err != nil || cmd.Name() != "remove" || cmd.Parent().Name() != "runner" {
		t.Fatalf("vm runner remove not registered: %v", err)
	}
}
