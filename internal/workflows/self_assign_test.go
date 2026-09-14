// Package workflows tests the pure parts of the repository's GitHub Actions workflows.
package workflows

import (
	"os"
	"regexp"
	"testing"
)

// askingPattern reads the `asking` regex out of the hint job, so the test runs the pattern the
// workflow actually ships rather than a copy that can drift from it.
//
// It is compiled with Go's regexp rather than JavaScript's. The pattern only uses syntax both
// engines read the same way; anything Go cannot compile, such as a lookbehind, fails here loudly
// instead of being tested under different rules.
func askingPattern(t *testing.T) *regexp.Regexp {
	t.Helper()
	raw, err := os.ReadFile("../../.github/workflows/self-assign.yml")
	if err != nil {
		t.Fatalf("read workflow: %v", err)
	}
	m := regexp.MustCompile(`const asking = /(.+)/i;`).FindSubmatch(raw)
	if m == nil {
		t.Fatal("no `const asking = /.../i;` line in self-assign.yml")
	}
	re, err := regexp.Compile("(?i)" + string(m[1]))
	if err != nil {
		t.Fatalf("asking pattern does not compile: %v", err)
	}
	return re
}

// The phrasings from #72, which the hint missed on a real claim, plus the forms it already caught.
func TestTheHintRecognisesAClaimInPlainEnglish(t *testing.T) {
	re := askingPattern(t)
	claims := []string{
		"Taking this \u2014 will add a single-VM run/exec command on top of the existing sshx plumbing.",
		"Taking this",
		"taking it",
		"Picking this up",
		"Grabbing this one",
		"Claiming this",
		"On it",
		"on it, PR by Friday",
		"Working on this",
		"Working on this, PR tomorrow",
		"working on it",
		"I would like to work on this",
		"can I take this",
		"please assign me",
		"I'll take this",
	}
	if len(claims) == 0 {
		t.Fatal("no claims to test")
	}
	for _, c := range claims {
		if !re.MatchString(c) {
			t.Errorf("missed a claim: %q", c)
		}
	}
}

func TestTheHintIgnoresCommentsThatAreNotClaims(t *testing.T) {
	re := askingPattern(t)
	others := []string{
		"Thanks for filing this",
		"The docs keep claiming that sudo is optional",
		"I commented on it in #23",
		"This is working now",
		"Reproduced on 0.3.0",
	}
	if len(others) == 0 {
		t.Fatal("no comments to test")
	}
	for _, c := range others {
		if re.MatchString(c) {
			t.Errorf("hinted on a comment that claims nothing: %q", c)
		}
	}
}
