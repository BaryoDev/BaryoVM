// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package secretref

import "testing"

func TestInside(t *testing.T) {
	cases := map[string]bool{
		"/a/b":      true,
		"/a/b/c/d":  true,
		"/a":        false,
		"/a/bc":     false,
		"/a/b/../c": false,
		"/etc":      false,
		"/a/b/../b": true,
	}
	for target, want := range cases {
		if got := Inside("/a/b", target); got != want {
			t.Errorf("Inside(/a/b, %s) = %v, want %v", target, got, want)
		}
	}
}
