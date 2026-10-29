// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package workflows

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const mplNotice = `// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
`

// TestEveryGoFileCarriesTheMPLNotice keeps the promise in CLA.md: every Go file, tests included,
// opens with the MPL-2.0 notice.
func TestEveryGoFileCarriesTheMPLNotice(t *testing.T) {
	var missing []string
	err := filepath.WalkDir("../..", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "testdata") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(p, ".go") {
			return nil
		}
		b, err := os.ReadFile(p) // #nosec G304 -- walks this repository's own files
		if err != nil {
			return err
		}
		if !strings.HasPrefix(string(b), mplNotice) {
			missing = append(missing, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) > 0 {
		t.Fatalf("missing the MPL-2.0 notice at the top: %s", strings.Join(missing, ", "))
	}
}
