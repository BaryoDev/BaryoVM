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
