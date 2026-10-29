// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package remote

import (
	"errors"
	"strings"
	"testing"
)

func TestParseAcceptsEveryAllowedOp(t *testing.T) {
	for _, op := range Ops {
		line := "baryovm-remote v1 " + string(op) + " my-app-2"
		got, err := Parse(line)
		if err != nil {
			t.Fatalf("%q: %v", line, err)
		}
		if got.Op != op || got.Stack != "my-app-2" {
			t.Errorf("%q parsed as %+v", line, got)
		}
		if got.String() != line {
			t.Errorf("String() = %q, want %q", got.String(), line)
		}
	}
}

func TestParseRefusesInjectionAndMalformedRequests(t *testing.T) {
	long := strings.Repeat("a", 64)
	cases := map[string]string{
		"empty":                "",
		"a shell":              "bash",
		"a plain command":      "cat /etc/shadow",
		"semicolon in name":    "baryovm-remote v1 update app;id",
		"command substitution": "baryovm-remote v1 update $(id)",
		"backticks":            "baryovm-remote v1 update `id`",
		"pipe":                 "baryovm-remote v1 update app|sh",
		"ampersand":            "baryovm-remote v1 update app&&id",
		"newline in name":      "baryovm-remote v1 update app\nid",
		"trailing newline":     "baryovm-remote v1 update app\n",
		"carriage return":      "baryovm-remote v1 update app\r",
		"tab separator":        "baryovm-remote\tv1\tupdate\tapp",
		"double space":         "baryovm-remote  v1 update app",
		"leading space":        " baryovm-remote v1 update app",
		"trailing space":       "baryovm-remote v1 update app ",
		"extra word":           "baryovm-remote v1 update app extra",
		"two stacks":           "baryovm-remote v1 update app other",
		"missing stack":        "baryovm-remote v1 update",
		"missing everything":   "baryovm-remote",
		"wrong word":           "baryovm-remot v1 update app",
		"wrong case word":      "Baryovm-Remote v1 update app",
		"wrong version":        "baryovm-remote v2 update app",
		"no version":           "baryovm-remote update app x",
		"unknown op":           "baryovm-remote v1 restart app",
		"exec op":              "baryovm-remote v1 exec app",
		"release op":           "baryovm-remote v1 release app",
		"op in caps":           "baryovm-remote v1 UPDATE app",
		"uppercase name":       "baryovm-remote v1 update App",
		"leading dash name":    "baryovm-remote v1 update -app",
		"underscore name":      "baryovm-remote v1 update my_app",
		"dot dot name":         "baryovm-remote v1 update ..",
		"slash name":           "baryovm-remote v1 update ../etc",
		"quote name":           "baryovm-remote v1 update 'app'",
		"dollar name":          "baryovm-remote v1 update $HOME",
		"name too long":        "baryovm-remote v1 update " + long,
		"non ascii":            "baryovm-remote v1 update appé",
		"nul byte":             "baryovm-remote v1 update app\x00",
		"del byte":             "baryovm-remote v1 update app\x7f",
		"huge":                 "baryovm-remote v1 update " + strings.Repeat("a", 500),
	}
	for name, line := range cases {
		if got, err := Parse(line); err == nil {
			t.Errorf("%s: %q was accepted as %+v", name, line, got)
		}
	}
}

func TestParseNameLengthBoundary(t *testing.T) {
	ok := "baryovm-remote v1 logs " + strings.Repeat("a", 63)
	if _, err := Parse(ok); err != nil {
		t.Errorf("63 characters must be accepted: %v", err)
	}
	tooLong := "baryovm-remote v1 logs " + strings.Repeat("a", 64)
	if _, err := Parse(tooLong); err == nil {
		t.Error("64 characters must be refused")
	}
}

func TestParseSaysWhyExecAndReleaseAreMissing(t *testing.T) {
	for _, op := range []string{"exec", "release"} {
		_, err := Parse("baryovm-remote v1 " + op + " app")
		if err == nil || !strings.Contains(err.Error(), "not available over a forced command") {
			t.Errorf("%s: error should say it is not available: %v", op, err)
		}
	}
}

func TestParseNoCommandIsAShellAttempt(t *testing.T) {
	if _, err := Parse(""); !errors.Is(err, ErrNoRequest) {
		t.Errorf("an empty command is an interactive login and must be ErrNoRequest, got %v", err)
	}
}

func TestParseErrorsDoNotEchoClientText(t *testing.T) {
	_, err := Parse("baryovm-remote v1 update $(touch /tmp/pwned)")
	if err == nil {
		t.Fatal("accepted")
	}
	if strings.Contains(err.Error(), "touch") {
		t.Errorf("the refusal repeats the client's text: %v", err)
	}
}
