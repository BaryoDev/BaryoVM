// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package recipe

import (
	"strings"
	"testing"
)

func withInputs(t *testing.T, inputs string) (*Recipe, error) {
	t.Helper()
	return Parse([]byte(`{"schema":1,"name":"x","files":[{"from":"a/","to":"/b"}],"inputs":`+inputs+`}`), "test.json")
}

// The rule the whole design rests on: a recipe is published, so a default secret is a shared one.
func TestSecretWithADefaultIsRefused(t *testing.T) {
	_, err := withInputs(t, `[{"name":"DB_PASSWORD","secret":true,"default":"hunter2"}]`)
	if err == nil {
		t.Fatal("a secret with a default must be refused, not warned about")
	}
	if !strings.Contains(err.Error(), "shared secret") {
		t.Errorf("the error should say why: %v", err)
	}
}

func TestNonSecretMayHaveADefault(t *testing.T) {
	r, err := withInputs(t, `[{"name":"SITE_URL","default":"http://localhost:8080"}]`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Inputs[0].IsRequired() {
		t.Error("an input with a default is not required")
	}
}

// Inputs land in a remote config as NAME=value, so a name with an equals sign or a newline is not a
// name, it is a write into the file being generated.
func TestNameMustBeAUsableVariableName(t *testing.T) {
	for _, bad := range []string{"DB PASSWORD", "DB=PASSWORD", "DB\nPASSWORD", "1DB", "DB-PASSWORD", ""} {
		if _, err := withInputs(t, `[{"name":`+quote(bad)+`}]`); err == nil {
			t.Errorf("name %q must be refused", bad)
		}
	}
}

func TestDuplicateInputIsRefused(t *testing.T) {
	_, err := withInputs(t, `[{"name":"A"},{"name":"A"}]`)
	if err == nil {
		t.Fatal("two inputs with one name must be refused")
	}
}

func TestSecretIsRequiredByDefault(t *testing.T) {
	r, err := withInputs(t, `[{"name":"DB_PASSWORD","secret":true}]`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !r.Inputs[0].IsRequired() {
		t.Error("a secret with no explicit answer is required: a stack that starts without its password fails later and less clearly")
	}
}

func TestExplicitlyOptionalSecretIsHonoured(t *testing.T) {
	no := false
	in := Input{Name: "OPTIONAL_TOKEN", Secret: true, Required: &no}
	if in.IsRequired() {
		t.Error("an explicit required:false must win over the secret default")
	}
}

func TestMissingReportsOnlyWhatIsNeeded(t *testing.T) {
	ins := []Input{
		{Name: "DB_PASSWORD", Secret: true},
		{Name: "SITE_URL", Default: "http://localhost"},
		{Name: "SUPPLIED"},
	}
	missing := Missing(ins, map[string]string{"SUPPLIED": "yes"})
	if len(missing) != 1 || missing[0].Name != "DB_PASSWORD" {
		t.Fatalf("want only DB_PASSWORD missing, got %v", names(missing))
	}
}

func TestExplainMissingNamesEachOneAndShowsTheShape(t *testing.T) {
	msg := ExplainMissing([]Input{
		{Name: "DB_PASSWORD", Secret: true, Description: "the application database password"},
	})
	for _, want := range []string{"DB_PASSWORD", "secret", "database password", "\"from\": \"env\""} {
		if !strings.Contains(msg, want) {
			t.Errorf("the message should contain %q:\n%s", want, msg)
		}
	}
}

func TestExplainMissingIsEmptyWhenNothingIsMissing(t *testing.T) {
	if got := ExplainMissing(nil); got != "" {
		t.Errorf("want empty, got %q", got)
	}
}

func names(ins []Input) []string {
	out := make([]string, 0, len(ins))
	for _, i := range ins {
		out = append(out, i.Name)
	}
	return out
}

// quote renders a Go string as a JSON string literal, so a test name can contain a newline.
func quote(s string) string {
	r := strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "\n", "\\n")
	return "\"" + r.Replace(s) + "\""
}
