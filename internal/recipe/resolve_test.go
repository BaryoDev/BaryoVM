// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package recipe

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func recipeWith(t *testing.T, inputs string) *Recipe {
	t.Helper()
	r, err := Parse([]byte(`{"schema":1,"name":"x","files":[{"from":"a/","to":"/b"}],"inputs":`+inputs+`}`), "recipe.json")
	if err != nil {
		t.Fatalf("recipe: %v", err)
	}
	return r
}

func TestResolvesFromEnv(t *testing.T) {
	t.Setenv("APP_DB_PASSWORD", "s3cret")
	r := recipeWith(t, `[{"name":"DB_PASSWORD","secret":true}]`)
	b := Bindings{"DB_PASSWORD": {From: "env", Name: "APP_DB_PASSWORD"}}

	got, err := Resolve(r, b, "project.json")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	v, ok := got.Get("DB_PASSWORD")
	if !ok || v != "s3cret" {
		t.Errorf("got %q, %v", v, ok)
	}
}

func TestResolvesFromFileAndTrimsTheNewline(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "pw")
	// What `echo secret > pw` produces. A trailing newline in a password presents as a wrong
	// password, which is an expensive way to find a whitespace bug.
	if err := os.WriteFile(p, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := recipeWith(t, `[{"name":"DB_PASSWORD","secret":true}]`)

	got, err := Resolve(r, Bindings{"DB_PASSWORD": {From: "file", Path: p}}, "project.json")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if v, _ := got.Get("DB_PASSWORD"); v != "s3cret" {
		t.Errorf("got %q, want the newline trimmed", v)
	}
}

// The rule the design rests on. A project file gets committed.
func TestLiteralValueIsRefused(t *testing.T) {
	r := recipeWith(t, `[{"name":"DB_PASSWORD","secret":true}]`)
	_, err := Resolve(r, Bindings{"DB_PASSWORD": {Value: "hunter2"}}, "project.json")
	if err == nil {
		t.Fatal("a literal in the project must be refused")
	}
	if !strings.Contains(err.Error(), "committed") {
		t.Errorf("the error should say why: %v", err)
	}
	if !strings.Contains(err.Error(), `"from": "env"`) {
		t.Error("the error should show the shape that works")
	}
}

func TestMissingRequiredInputIsRefusedBeforeReadingAnything(t *testing.T) {
	r := recipeWith(t, `[{"name":"DB_PASSWORD","secret":true},{"name":"OTHER","secret":true}]`)
	_, err := Resolve(r, Bindings{}, "project.json")
	if err == nil {
		t.Fatal("missing required inputs must be refused")
	}
	for _, want := range []string{"DB_PASSWORD", "OTHER"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error should name %s: %v", want, err)
		}
	}
}

func TestDefaultIsUsedWhenNotBound(t *testing.T) {
	r := recipeWith(t, `[{"name":"SITE_URL","default":"http://localhost:8080"}]`)
	got, err := Resolve(r, Bindings{}, "project.json")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if v, _ := got.Get("SITE_URL"); v != "http://localhost:8080" {
		t.Errorf("got %q", v)
	}
}

// A binding nothing reads is usually a rename the project has not followed, and silently ignoring
// it means the value the operator thought they supplied never arrives.
func TestBindingAnUndeclaredInputIsRefused(t *testing.T) {
	r := recipeWith(t, `[{"name":"DB_PASSWORD","secret":true}]`)
	t.Setenv("X", "y")
	_, err := Resolve(r, Bindings{
		"DB_PASSWORD":  {From: "env", Name: "X"},
		"DB_PASSWORDD": {From: "env", Name: "X"},
	}, "project.json")
	if err == nil {
		t.Fatal("a stray binding must be refused")
	}
	if !strings.Contains(err.Error(), "DB_PASSWORDD") {
		t.Errorf("the error should name the stray binding: %v", err)
	}
	if !strings.Contains(err.Error(), "rename") {
		t.Error("the error should say what this usually means")
	}
}

func TestEmptyRequiredValueIsRefused(t *testing.T) {
	t.Setenv("EMPTY_ONE", "")
	r := recipeWith(t, `[{"name":"DB_PASSWORD","secret":true}]`)
	_, err := Resolve(r, Bindings{"DB_PASSWORD": {From: "env", Name: "EMPTY_ONE"}}, "project.json")
	if err == nil {
		t.Fatal("an empty required input must be refused, not passed through")
	}
	if !strings.Contains(err.Error(), "EMPTY_ONE") {
		t.Errorf("the error should name the source: %v", err)
	}
}

func TestUnknownSourceIsRefused(t *testing.T) {
	r := recipeWith(t, `[{"name":"A"}]`)
	_, err := Resolve(r, Bindings{"A": {From: "vault", Name: "x"}}, "project.json")
	if err == nil {
		t.Fatal("an unknown source must be refused")
	}
}

func TestEnvRefNeedsAName(t *testing.T) {
	r := recipeWith(t, `[{"name":"A"}]`)
	if _, err := Resolve(r, Bindings{"A": {From: "env"}}, "project.json"); err == nil {
		t.Fatal("env with no variable name must be refused")
	}
}

// A secret must be debuggable without being readable.
func TestRedactedHidesSecretsAndKeepsTheRest(t *testing.T) {
	t.Setenv("PW", "s3cret")
	t.Setenv("URL", "https://example.com")
	r := recipeWith(t, `[{"name":"DB_PASSWORD","secret":true},{"name":"SITE_URL"}]`)
	got, err := Resolve(r, Bindings{
		"DB_PASSWORD": {From: "env", Name: "PW"},
		"SITE_URL":    {From: "env", Name: "URL"},
	}, "project.json")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	red := got.Redacted()
	if strings.Contains(red["DB_PASSWORD"], "s3cret") {
		t.Error("a redacted secret must not contain the value")
	}
	if !strings.Contains(red["DB_PASSWORD"], "6 chars") {
		t.Errorf("the length is the useful part: %q", red["DB_PASSWORD"])
	}
	if red["SITE_URL"] != "https://example.com" {
		t.Errorf("a non-secret should read normally, got %q", red["SITE_URL"])
	}
	if !got.IsSecret("DB_PASSWORD") || got.IsSecret("SITE_URL") {
		t.Error("IsSecret should follow the recipe's declaration")
	}
}

func TestMissingFileIsNamed(t *testing.T) {
	r := recipeWith(t, `[{"name":"A","secret":true}]`)
	_, err := Resolve(r, Bindings{"A": {From: "file", Path: "/nonexistent/secret"}}, "project.json")
	if err == nil {
		t.Fatal("a missing file must be an error")
	}
	if !strings.Contains(err.Error(), "/nonexistent/secret") {
		t.Errorf("the error should name the path: %v", err)
	}
}

// The defect a critic found on the first review of this file: an optional input with no default and
// no binding used to vanish from the resolved set entirely. Resolve returned success and the name
// was simply absent, so a caller writing the remote config had no way to tell "declared optional and
// nobody supplied it" from "never asked for". Success over something that did not happen.
func TestOptionalUnboundInputIsPresentAndMarkedUnset(t *testing.T) {
	no := false
	r := &Recipe{
		Schema: SchemaVersion, Name: "x",
		Files:  []Files{{From: "a/", To: "/b"}},
		Inputs: []Input{{Name: "OPTIONAL_TOKEN", Required: &no}},
	}

	got, err := Resolve(r, Bindings{}, "project.json")
	if err != nil {
		t.Fatalf("an optional input nobody supplied is not an error: %v", err)
	}

	v, ok := got.Get("OPTIONAL_TOKEN")
	if !ok {
		t.Fatal("a declared input must appear in the resolved set even when nothing supplied it")
	}
	if v != "" {
		t.Errorf("want empty, got %q", v)
	}
	if !got.IsUnset("OPTIONAL_TOKEN") {
		t.Error("an input nothing supplied must be distinguishable from one set to empty")
	}
	if names := got.Unset(); len(names) != 1 || names[0] != "OPTIONAL_TOKEN" {
		t.Errorf("Unset() = %v", names)
	}
}

// The other half of the same distinction: a value somebody deliberately set to empty is not unset.
func TestDeliberatelyEmptyOptionalIsNotUnset(t *testing.T) {
	t.Setenv("DELIBERATELY_EMPTY", "")
	no := false
	r := &Recipe{
		Schema: SchemaVersion, Name: "x",
		Files:  []Files{{From: "a/", To: "/b"}},
		Inputs: []Input{{Name: "OPTIONAL_TOKEN", Required: &no}},
	}

	got, err := Resolve(r, Bindings{"OPTIONAL_TOKEN": {From: "env", Name: "DELIBERATELY_EMPTY"}}, "project.json")
	if err != nil {
		t.Fatalf("an optional input may resolve to empty: %v", err)
	}
	if got.IsUnset("OPTIONAL_TOKEN") {
		t.Error("a bound input is not unset, whatever its value")
	}
}

func TestNilBindingsBehavesLikeEmpty(t *testing.T) {
	r := recipeWith(t, `[{"name":"SITE_URL","default":"http://localhost"}]`)
	got, err := Resolve(r, nil, "project.json")
	if err != nil {
		t.Fatalf("a nil bindings map is an empty one: %v", err)
	}
	if v, _ := got.Get("SITE_URL"); v != "http://localhost" {
		t.Errorf("got %q", v)
	}
}

// A binding wins over a default rather than the two being combined somehow.
func TestBindingWinsOverDefault(t *testing.T) {
	t.Setenv("FROM_ENV", "bound")
	r := recipeWith(t, `[{"name":"SITE_URL","default":"http://localhost"}]`)
	got, err := Resolve(r, Bindings{"SITE_URL": {From: "env", Name: "FROM_ENV"}}, "project.json")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if v, _ := got.Get("SITE_URL"); v != "bound" {
		t.Errorf("the binding should win, got %q", v)
	}
	if got.IsUnset("SITE_URL") {
		t.Error("a bound input is not unset")
	}
}

func TestRedactedOnARecipeWithNoInputs(t *testing.T) {
	r := &Recipe{Schema: SchemaVersion, Name: "x", Files: []Files{{From: "a/", To: "/b"}}}
	got, err := Resolve(r, Bindings{}, "project.json")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(got.Redacted()) != 0 || len(got.Names()) != 0 || len(got.Unset()) != 0 {
		t.Error("a recipe with no inputs resolves to nothing, and says so consistently")
	}
}
