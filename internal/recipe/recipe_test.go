// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package recipe

import (
	"errors"
	"strings"
	"testing"
)

func parse(t *testing.T, body string) (*Recipe, error) {
	t.Helper()
	return Parse([]byte(body), "test.json")
}

func mustParse(t *testing.T, body string) *Recipe {
	t.Helper()
	r, err := parse(t, body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return r
}

func TestParsesAComposeRecipe(t *testing.T) {
	r := mustParse(t, `{
	  "schema": 1,
	  "name": "barako",
	  "version": "4.4.1",
	  "services": [
	    { "name": "api", "image": "ghcr.io/baryodev/barako-cms:4.4.1", "healthUrl": "http://127.0.0.1:8080/health" },
	    { "name": "db",  "image": "postgres:17" }
	  ],
	  "routes": [ { "host": "cms.example.com", "port": 8080, "tls": true } ],
	  "backup": [ { "kind": "postgres-container", "container": "db", "database": "app" } ],
	  "inputs": [ { "name": "DB_PASSWORD", "secret": true } ]
	}`)

	if got := r.ServiceNames(); len(got) != 2 || got[0] != "api" {
		t.Errorf("service names = %v", got)
	}
	if r.IsStatic() {
		t.Error("a recipe with services is not static")
	}
	if !r.HasBackup() {
		t.Error("a recipe with a strategy has a backup")
	}
}

// A static site is a recipe with files and no services, not a compose stack with pieces missing.
// This is the NoCompose flag going away.
func TestStaticSiteIsDerivedNotDeclared(t *testing.T) {
	r := mustParse(t, `{
	  "schema": 1,
	  "name": "docs",
	  "files": [ { "from": "dist/", "to": "/srv/docs" } ],
	  "routes": [ { "host": "docs.example.com", "port": 443 } ]
	}`)
	if !r.IsStatic() {
		t.Error("no services means static")
	}
	if r.HasBackup() {
		t.Error("no strategies means nothing is backed up")
	}
}

func TestUnknownSchemaVersionIsRefused(t *testing.T) {
	_, err := parse(t, `{"schema": 2, "name": "x", "files": [{"from":"a/","to":"/b"}]}`)
	if err == nil {
		t.Fatal("a newer schema must be refused, not parsed leniently")
	}
	var se *ErrSchema
	if !errors.As(err, &se) {
		t.Fatalf("want ErrSchema, got %T", err)
	}
	if !strings.Contains(err.Error(), "silently skip") {
		t.Error("the error should say why refusing beats accepting")
	}
}

func TestMissingSchemaVersionIsRefused(t *testing.T) {
	_, err := parse(t, `{"name": "x", "files": [{"from":"a/","to":"/b"}]}`)
	if err == nil {
		t.Fatal("a recipe with no schema version must be refused")
	}
	if !strings.Contains(err.Error(), "schema") {
		t.Errorf("the error should name the missing field: %v", err)
	}
}

// An unknown field is refused for the same reason an unknown version is: a field this binary does
// not act on is a deploy quietly doing less than was asked.
func TestUnknownFieldIsRefused(t *testing.T) {
	_, err := parse(t, `{"schema":1,"name":"x","files":[{"from":"a/","to":"/b"}],"surprise":true}`)
	if err == nil {
		t.Fatal("an unknown field must be refused")
	}
}

func TestRecipeWithNothingToDeployIsRefused(t *testing.T) {
	_, err := parse(t, `{"schema": 1, "name": "empty"}`)
	if err == nil {
		t.Fatal("a recipe with neither services nor files deploys nothing")
	}
}

func TestServiceNeedsImageOrBuild(t *testing.T) {
	_, err := parse(t, `{"schema":1,"name":"x","services":[{"name":"api"}]}`)
	if err == nil {
		t.Fatal("a service with neither image nor build must be refused")
	}
}

func TestServiceCannotHaveBothImageAndBuild(t *testing.T) {
	_, err := parse(t, `{"schema":1,"name":"x","services":[{"name":"api","image":"a","build":"."}]}`)
	if err == nil {
		t.Fatal("a service with both leaves which one runs undefined")
	}
}

func TestDuplicateServiceNameIsRefused(t *testing.T) {
	_, err := parse(t, `{"schema":1,"name":"x","services":[
	  {"name":"api","image":"a"},{"name":"api","image":"b"}]}`)
	if err == nil {
		t.Fatal("two services with one name must be refused")
	}
}

func TestFilesDestinationMustBeAbsolute(t *testing.T) {
	_, err := parse(t, `{"schema":1,"name":"x","files":[{"from":"dist/","to":"srv/docs"}]}`)
	if err == nil {
		t.Fatal("a relative remote path must be refused")
	}
}

func TestRouteNeedsAPort(t *testing.T) {
	_, err := parse(t, `{"schema":1,"name":"x","files":[{"from":"a/","to":"/b"}],
	  "routes":[{"host":"example.com"}]}`)
	if err == nil {
		t.Fatal("a route with no destination is a hostname nobody serves")
	}
}

// Caught by review: json.Decoder stops at the end of the first value, so a file holding two recipes
// back to back parsed as the first one and the second was never read. Two recipes concatenated by a
// bad merge would deploy the top half of the file with nothing saying so.
func TestTrailingJSONIsRefused(t *testing.T) {
	_, err := parse(t, `{"schema":1,"name":"x","files":[{"from":"a/","to":"/b"}]}{"schema":1,"name":"second"}`)
	if err == nil {
		t.Fatal("a second JSON value after the recipe must be refused")
	}
	if !strings.Contains(err.Error(), "more than one JSON value") {
		t.Errorf("the error should say what is wrong: %v", err)
	}
}

func TestTrailingWhitespaceIsFine(t *testing.T) {
	if _, err := parse(t, "{\"schema\":1,\"name\":\"x\",\"files\":[{\"from\":\"a/\",\"to\":\"/b\"}]}\n\n  \n"); err != nil {
		t.Fatalf("a trailing newline is not a second value: %v", err)
	}
}
