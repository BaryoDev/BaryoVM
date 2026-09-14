// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"errors"
	"testing"

	"github.com/BaryoDev/BaryoVM/internal/ui"
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

func TestParseExecArgsRequiresDash(t *testing.T) {
	_, _, _, err := parseExecArgs([]string{"web1", "df", "-h"})
	if err == nil {
		t.Fatal("expected error when remote command is not after --")
	}
}

func TestParseExecArgsAcceptsFlagsAroundName(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantName   string
		wantRemote []string
		wantJSON   bool
	}{
		{
			name:       "plain",
			args:       []string{"web1", "--", "df", "-h"},
			wantName:   "web1",
			wantRemote: []string{"df", "-h"},
		},
		{
			name:       "output before name",
			args:       []string{"-o", "json", "web1", "--", "uptime"},
			wantName:   "web1",
			wantRemote: []string{"uptime"},
			wantJSON:   true,
		},
		{
			name:       "output after name",
			args:       []string{"oracle", "-o", "json", "--", "cat", "/etc/os-release"},
			wantName:   "oracle",
			wantRemote: []string{"cat", "/etc/os-release"},
			wantJSON:   true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			outputFormat = "human"
			ui.SetJSON(false)
			name, remote, help, err := parseExecArgs(c.args)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if help {
				t.Fatal("unexpected help")
			}
			if name != c.wantName {
				t.Errorf("name: got %q want %q", name, c.wantName)
			}
			if len(remote) != len(c.wantRemote) {
				t.Fatalf("remote: got %v want %v", remote, c.wantRemote)
			}
			for i := range remote {
				if remote[i] != c.wantRemote[i] {
					t.Fatalf("remote: got %v want %v", remote, c.wantRemote)
				}
			}
			if ui.JSON() != c.wantJSON {
				t.Errorf("json mode: got %v want %v", ui.JSON(), c.wantJSON)
			}
		})
	}
}

func TestParseExecArgsHelp(t *testing.T) {
	_, _, help, err := parseExecArgs([]string{"--help"})
	if err != nil || !help {
		t.Fatalf("got help=%v err=%v", help, err)
	}
}
