// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package compose

import "testing"

const psArray = `[{"Name":"app-web-1","Service":"web","State":"running","Health":"healthy","Image":"nginx:1.27","Publishers":[{"URL":"0.0.0.0","TargetPort":80,"PublishedPort":8080,"Protocol":"tcp"},{"URL":"","TargetPort":443,"PublishedPort":0,"Protocol":"tcp"}]},
{"Name":"app-db-1","Service":"db","State":"exited","Health":"","Image":"postgres:16","Publishers":null}]`

const psLines = `{"Name":"app-web-1","Service":"web","State":"running","Health":"","Image":"nginx:1.27","Publishers":[{"URL":"0.0.0.0","TargetPort":80,"PublishedPort":8080,"Protocol":"tcp"}]}
{"Name":"app-db-1","Service":"db","State":"running","Health":"healthy","Image":"postgres:16","Publishers":[]}
`

func TestParsePsArray(t *testing.T) {
	got, err := ParsePs(psArray)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Service != "web" || got[0].Health != "healthy" || got[1].State != "exited" {
		t.Fatalf("got %+v", got)
	}
	if len(got[0].Ports) != 2 || got[0].Ports[0].Published != 8080 || got[0].Ports[0].Target != 80 || got[0].Ports[0].Host != "0.0.0.0" {
		t.Errorf("ports: %+v", got[0].Ports)
	}
	if len(got[1].Ports) != 0 {
		t.Errorf("a null publishers list gave ports: %+v", got[1].Ports)
	}
}

func TestParsePsOneObjectPerLine(t *testing.T) {
	got, err := ParsePs(psLines)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1].Name != "app-db-1" || got[1].Health != "healthy" {
		t.Fatalf("got %+v", got)
	}
}

func TestParsePsEmptyIsAnEmptyList(t *testing.T) {
	got, err := ParsePs("\n")
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("got %#v, %v", got, err)
	}
}

func TestParsePsRejectsTheTable(t *testing.T) {
	if _, err := ParsePs("NAME     STATUS\napp-1    Up 2 days\n"); err == nil {
		t.Error("a text table parsed as JSON")
	}
}

func TestPsJSONCmd(t *testing.T) {
	want := "cd '/opt/app' && docker compose ps --format json"
	if got := (Stack{Dir: "/opt/app"}).PsJSONCmd(); got != want {
		t.Errorf("got %q want %q", got, want)
	}
}
