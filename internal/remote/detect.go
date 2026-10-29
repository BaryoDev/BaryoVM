// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package remote

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/sshx"
)

// NewNonce returns a random token that no forced command can predict.
func NewNonce() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand does not fail on supported platforms. A fixed fallback would defeat the
		// check, so stop rather than weaken it.
		panic("remote: no randomness: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// DetectSwallowed proves the VM runs the command it is sent. It asks the VM to echo nonce, and
// refuses unless that exact token comes back.
//
// This is the failure #98 was filed for: a key pinned with command="..." discards whatever the
// client sends and runs its own script, so every BaryoVM command would "succeed" with BaryoVM not
// involved. A random token cannot be produced by a script that never saw it, so a match means the
// command really ran. The probe is a hex string, which is the same word in sh, cmd and PowerShell,
// so it needs no quoting on any host.
func DetectSwallowed(r sshx.Runner, nonce string) error {
	out, err := r.Run("echo " + nonce)
	if err == nil && strings.TrimSpace(out) == nonce {
		return nil
	}
	if strings.Contains(out, Protocol) {
		return fmt.Errorf("this VM's SSH key is pinned to `baryovm remote serve`, which answers only update, backup, status and logs: " +
			"mark the VM with `baryovm vm remote set <vm> forced` and use those commands")
	}
	why := "the VM ran something other than the command sent"
	if err != nil {
		why = "the probe command failed: " + err.Error()
	}
	return fmt.Errorf("%s. This usually means the key is pinned in authorized_keys with command=\"...\", so the VM discards "+
		"every command baryovm sends and runs its own script. Nothing baryovm sends is being executed. "+
		"Use a key without a forced command, or install the baryovm wrapper (`baryovm vm remote install <vm>`) and mark the VM `baryovm vm remote set <vm> forced`", why)
}
