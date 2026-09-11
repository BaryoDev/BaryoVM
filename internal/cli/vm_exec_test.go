// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"errors"
	"testing"
)

func TestBuildExecRemoteQuotesEachArg(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want string
	}{
		{
			name: "simple argv",
			in:   []string{"systemctl", "status", "nginx"},
			want: "'systemctl' 'status' 'nginx'",
		},
		{
			name: "one arg with spaces stays one word",
			in:   []string{"df -h /var/www"},
			want: "'df -h /var/www'",
		},
		{
			name: "metacharacters stay literal",
			in:   []string{"echo", "$HOME; rm -rf /"},
			want: "'echo' '$HOME; rm -rf /'",
		},
		{
			name: "single quote inside an arg",
			in:   []string{"echo", "a'b"},
			want: `'echo' 'a'\''b'`,
		},
	}
	for _, c := range cases {
		got, err := buildExecRemote(c.in)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", c.name, err)
		}
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestBuildExecRemoteRequiresCommand(t *testing.T) {
	if _, err := buildExecRemote(nil); err == nil {
		t.Fatal("expected an error for an empty argv")
	}
}

func TestExitCodeOfPropagatesRemoteStatus(t *testing.T) {
	err := &exitError{code: 7, msg: "remote exit status 7"}
	code, silent := exitCodeOf(err)
	if code != 7 {
		t.Errorf("code: got %d, want 7", code)
	}
	if silent {
		t.Error("expected silent=false for a human-mode failure")
	}

	err = &exitError{code: 3, msg: "boom", silent: true}
	code, silent = exitCodeOf(err)
	if code != 3 || !silent {
		t.Errorf("got code=%d silent=%v", code, silent)
	}

	code, silent = exitCodeOf(errors.New("dial failed"))
	if code != 1 || silent {
		t.Errorf("plain error: got code=%d silent=%v", code, silent)
	}
}
