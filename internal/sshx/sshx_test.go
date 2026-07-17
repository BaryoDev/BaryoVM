// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package sshx

import "testing"

func TestQuote(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "''"},
		{"simple", "'simple'"},
		{"with space", "'with space'"},
		{"deploy-postgres-1", "'deploy-postgres-1'"},
		// Shell metacharacters must stay literal inside single quotes (injection safety).
		{"$HOME; rm -rf /", "'$HOME; rm -rf /'"},
		{"a`b`c", "'a`b`c'"},
		{"a&&b||c", "'a&&b||c'"},
		// A single quote is the one char that must be escaped.
		{"a'b", `'a'\''b'`},
		{"'", `''\'''`},
	}
	for _, c := range cases {
		if got := Quote(c.in); got != c.want {
			t.Errorf("Quote(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
