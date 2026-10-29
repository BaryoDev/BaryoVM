// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package fleet

import (
	"encoding/json"
	"strings"
	"testing"
)

// A fleet.json from before the verify spec existed loads with no spec, and a healthUrl alone is
// still all a stack needs: nothing is added to the file on save either.
func TestAStackWithOnlyAHealthURLLoadsWithNoSpec(t *testing.T) {
	old := `{"stacks":[{"name":"site","vm":"v","dir":"/srv","healthUrl":"http://127.0.0.1:8080/"}]}`
	var s Store
	if err := json.Unmarshal([]byte(old), &s); err != nil {
		t.Fatal(err)
	}
	st := s.FindStack("site")
	if st == nil || st.HealthURL != "http://127.0.0.1:8080/" || st.Verify != nil {
		t.Fatalf("stack = %+v", st)
	}
	out, _ := json.Marshal(&s)
	if strings.Contains(string(out), `"verify"`) {
		t.Errorf("saving invented a verify key: %s", out)
	}
}

// The spec is stored under "verify" on the stack, and keys this binary does not know survive a
// save at both levels: on the stack, and inside the spec.
func TestTheVerifySpecRoundTripsWithUnknownFields(t *testing.T) {
	in := `{"stacks":[{"name":"shop","vm":"v","dir":"/srv","futureStackKey":{"a":1},` +
		`"verify":{"checks":[{"path":"/","contains":["m"],"futureCheckKey":true}],"attempts":4,"futureSpecKey":"x"}}]}`
	var s Store
	if err := json.Unmarshal([]byte(in), &s); err != nil {
		t.Fatal(err)
	}
	st := s.FindStack("shop")
	if st.Verify == nil || st.Verify.Attempts != 4 || st.Verify.Checks[0].Path != "/" {
		t.Fatalf("verify = %+v", st.Verify)
	}
	out, _ := json.Marshal(&s)
	for _, want := range []string{`"futureStackKey":{"a":1}`, `"futureCheckKey":true`, `"futureSpecKey":"x"`, `"contains":["m"]`, `"attempts":4`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("lost %s:\n%s", want, out)
		}
	}
	// verify is a field of the typed struct now, so a preserved copy would be a bug.
	if strings.Count(string(out), `"verify"`) != 1 {
		t.Errorf("verify written twice:\n%s", out)
	}
}
