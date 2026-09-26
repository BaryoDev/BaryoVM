// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package main

import (
	"flag"
	"log"

	"github.com/BaryoDev/BaryoVM/internal/cli"
)

func main() {
	check := flag.Bool("check", false, "fail if generated command docs differ from the checked-in pages")
	flag.Parse()
	if err := cli.GenerateCommandDocs("docs/commands", *check); err != nil {
		log.Fatal(err)
	}
}
