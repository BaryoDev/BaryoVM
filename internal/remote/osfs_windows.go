// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

//go:build windows

package remote

import "errors"

// OSFS has no ownership model on Windows, and the wrapper only runs on Linux VMs, so it refuses.
type OSFS struct{}

// Lstat always fails.
func (OSFS) Lstat(string) (FileInfo, error) {
	return FileInfo{}, errors.New("baryovm remote serve runs on Linux VMs only")
}

// ReadFile always fails.
func (OSFS) ReadFile(string) ([]byte, error) {
	return nil, errors.New("baryovm remote serve runs on Linux VMs only")
}
