// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package toolchain

import "testing"

// doctor --fix promises to install what is missing, and the release sync needs
// rsync, so rsync has to be installable. ssh deliberately is not: a machine
// without ssh has a problem BaryoVM should not try to paper over. aws is in
// neither camp; nothing shells out to the AWS CLI, the provider uses the Go SDK.
func TestTheRegistryInstallsWhatDoctorChecksAndNothingElse(t *testing.T) {
	if _, ok := registry["rsync"]; !ok {
		t.Error("no rsync installer, so `doctor --fix` cannot resolve the one tool a release needs")
	}
	if _, ok := registry["docker"]; !ok {
		t.Error("no docker installer")
	}
	if _, ok := registry["ssh"]; ok {
		t.Error("an ssh installer was added; a machine without ssh is not ours to fix")
	}
	if _, ok := registry["aws"]; ok {
		t.Error("the aws installer is dead code: nothing checks or runs the AWS CLI")
	}
}

// A tool with no installer must say so rather than look like an install failure.
func TestEnsureCLISaysWhenItHasNoInstaller(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := EnsureCLI("ssh"); err == nil {
		t.Fatal("ssh was resolved on an empty PATH, so this proves nothing")
	} else if got := err.Error(); got != "ssh is not installed and BaryoVM has no installer for it" {
		t.Errorf("unexpected error for a tool with no installer: %q", got)
	}
}
