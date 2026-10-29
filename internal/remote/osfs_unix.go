// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

//go:build !windows

package remote

import (
	"errors"
	"os"
	"syscall"
)

// OSFS is the real filesystem.
type OSFS struct{}

// Lstat does not follow symlinks, so a link planted at the config path is seen as a link.
func (OSFS) Lstat(name string) (FileInfo, error) {
	fi, err := os.Lstat(name)
	if err != nil {
		return FileInfo{}, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return FileInfo{}, errors.New("cannot read the owner of " + name)
	}
	return FileInfo{Mode: fi.Mode(), UID: st.Uid}, nil
}

// ReadFile reads the file.
func (OSFS) ReadFile(name string) ([]byte, error) {
	return os.ReadFile(name) // #nosec G304 -- the allowlist path from the forced command line, read after its owner and mode checks
}
