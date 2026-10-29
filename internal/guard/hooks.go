// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package guard

import "strings"

// HookMarker is the second line of every hook this package writes, so install-hooks can tell its
// own hook from one a person wrote.
const HookMarker = "# baryovm-guard"

// HookNames are the git hooks install-hooks writes.
var HookNames = []string{"pre-commit", "commit-msg", "pre-push"}

// HookScript returns the script for a hook. It runs the same checks as the wrappers, and fails
// closed when baryovm is not on PATH, because a hook that silently passes defeats the point.
func HookScript(name string) string {
	return "#!/bin/sh\n" + HookMarker + "\n" +
		"command -v baryovm >/dev/null 2>&1 || { echo 'baryovm-guard: baryovm is not on PATH. Install it, or remove this hook.' >&2; exit 1; }\n" +
		"exec baryovm guard check " + name + " \"$@\"\n"
}

// IsOurHook reports whether content was written by HookScript.
func IsOurHook(content string) bool {
	return strings.Contains(content, HookMarker)
}
