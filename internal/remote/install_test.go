// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package remote

import (
	"strings"
	"testing"
)

func TestAuthorizedKeysLinePinsTheWrapper(t *testing.T) {
	l := AuthorizedKeysLine("ssh-ed25519 AAAA ci")
	for _, want := range []string{
		`command="/usr/local/bin/baryovm-remote remote serve"`,
		"no-port-forwarding", "no-pty", "no-agent-forwarding", "no-X11-forwarding",
		"ssh-ed25519 AAAA ci",
	} {
		if !strings.Contains(l, want) {
			t.Errorf("missing %q in %s", want, l)
		}
	}
	if !strings.HasPrefix(l, "command=") {
		t.Errorf("options must come first: %s", l)
	}
}

func TestTemplateConfigIsValidAndGrantsNothing(t *testing.T) {
	c, err := ParseConfig(TemplateConfig())
	if err != nil {
		t.Fatalf("the template must pass its own validation: %v", err)
	}
	s, ok := c.Stack("example")
	if !ok {
		t.Fatal("no example stack")
	}
	for _, op := range Ops {
		if s.Allows(op) {
			t.Errorf("template allows %s", op)
		}
	}
}

func TestUnameArch(t *testing.T) {
	for in, want := range map[string]string{
		"x86_64": "amd64", "amd64": "amd64", "aarch64": "arm64", "arm64": "arm64", "armv7l": "", "": "",
	} {
		if got := UnameArch(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestInstallCommandsAreRootOwnedAndQuoted(t *testing.T) {
	b := InstallBinaryCmd("/home/ci/it's.tmp")
	for _, want := range []string{"sudo -n", "install -m 0755 -o root -g root", InstallBinary} {
		if !strings.Contains(b, want) {
			t.Errorf("binary cmd missing %q: %s", want, b)
		}
	}
	if strings.Contains(b, "/home/ci/it's.tmp") {
		t.Errorf("tmp path not quoted: %s", b)
	}
	cfg := InstallConfigCmd("/home/ci/c.tmp")
	for _, want := range []string{"install -d -m 0755 -o root -g root", "test -e", "echo kept", "echo created", DefaultConfigPath} {
		if !strings.Contains(cfg, want) {
			t.Errorf("config cmd missing %q: %s", want, cfg)
		}
	}
	if !strings.Contains(UploadCmd("/tmp/x"), "cat >") {
		t.Error("upload cmd")
	}
}
