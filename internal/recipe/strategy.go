// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package recipe

import (
	"fmt"
	"strings"
)

// Strategy is one thing a stack backs up, and how it is restored.
//
// A list, not a field. A stack with a database and an uploads directory has two strategies, and the
// single DBContainer/DBName pair could only ever express one of them. An empty list is an explicit
// statement that there is nothing to back up, which is what the NoDatabase flag said with a
// negative.
//
// Restore is declared next to the dump on purpose. A backup nobody has restored is hope, and a
// strategy that says only how to dump cannot support a restore drill at all.
type Strategy struct {
	Kind Kind `json:"kind"`

	// Container is the container to run the dump in, for the container kinds.
	Container string `json:"container,omitempty"`

	// Database names the database to dump.
	Database string `json:"database,omitempty"`

	// User is the database user. Each kind has its own default.
	User string `json:"user,omitempty"`

	// Paths are the files or directories this strategy copies, for KindFiles.
	Paths []string `json:"paths,omitempty"`

	// Dump and Restore are the commands for KindCommand. Both are required for that kind: a dump
	// with no restore is the hope case above, stated in a schema.
	Dump    string `json:"dump,omitempty"`
	Restore string `json:"restore,omitempty"`
}

// Kind is how one strategy dumps and restores.
//
// The set is open by design. KindCommand exists so a shape nobody anticipated, MSSQL, SQLite, a
// Redis snapshot, an S3 sync, does not have to wait for a BaryoVM release. Without it every new
// database is a new kind and the list grows forever while somebody's deploy is blocked.
type Kind string

const (
	// KindPostgresContainer dumps with pg_dump inside a container.
	KindPostgresContainer Kind = "postgres-container"
	// KindMySQLContainer dumps with mysqldump inside a container.
	KindMySQLContainer Kind = "mysql-container"
	// KindMongoContainer dumps with mongodump inside a container.
	KindMongoContainer Kind = "mongo-container"
	// KindFiles copies paths from the VM.
	KindFiles Kind = "files"
	// KindCommand runs commands the recipe supplies. The escape hatch.
	KindCommand Kind = "command"
)

// knownKinds is every kind this binary can run. A kind outside it is refused rather than skipped,
// for the same reason an unknown schema version is: a strategy that does not run is a backup that
// does not exist, and finding that out during a restore is the worst possible moment.
var knownKinds = map[Kind]bool{
	KindPostgresContainer: true,
	KindMySQLContainer:    true,
	KindMongoContainer:    true,
	KindFiles:             true,
	KindCommand:           true,
}

// DefaultUser is the database user a kind assumes when the strategy does not name one.
func (k Kind) DefaultUser() string {
	switch k {
	case KindPostgresContainer:
		return "postgres"
	case KindMySQLContainer:
		return "root"
	case KindMongoContainer:
		return ""
	default:
		return ""
	}
}

// NeedsContainer reports whether a kind dumps inside a container.
func (k Kind) NeedsContainer() bool {
	switch k {
	case KindPostgresContainer, KindMySQLContainer, KindMongoContainer:
		return true
	default:
		return false
	}
}

func validateStrategies(ss []Strategy, path string) error {
	for i, s := range ss {
		if s.Kind == "" {
			return fmt.Errorf("recipe %s: backup %d has no kind", path, i)
		}
		if !knownKinds[s.Kind] {
			return fmt.Errorf(
				"recipe %s: backup %d has kind %q, which this baryovm does not know.\n"+
					"Known kinds: %s.\n"+
					"For anything else use kind \"command\" with its own dump and restore, rather than\n"+
					"waiting for a release.",
				path, i, s.Kind, strings.Join(KnownKinds(), ", "))
		}

		switch s.Kind {
		case KindCommand:
			if strings.TrimSpace(s.Dump) == "" {
				return fmt.Errorf("recipe %s: backup %d kind command has no dump", path, i)
			}
			if strings.TrimSpace(s.Restore) == "" {
				return fmt.Errorf(
					"recipe %s: backup %d kind command has a dump and no restore.\n"+
						"A backup nobody can restore is hope. Declare both.", path, i)
			}
		case KindFiles:
			if len(s.Paths) == 0 {
				return fmt.Errorf("recipe %s: backup %d kind files has no paths", path, i)
			}
			for _, p := range s.Paths {
				if !strings.HasPrefix(p, "/") {
					return fmt.Errorf("recipe %s: backup %d path %q must be absolute on the VM", path, i, p)
				}
			}
		default:
			if strings.TrimSpace(s.Container) == "" {
				return fmt.Errorf("recipe %s: backup %d kind %s has no container", path, i, s.Kind)
			}
			if strings.TrimSpace(s.Database) == "" {
				return fmt.Errorf("recipe %s: backup %d kind %s has no database", path, i, s.Kind)
			}
		}
	}
	return nil
}

// KnownKinds lists the kinds this binary can run, for an error message.
func KnownKinds() []string {
	return []string{
		string(KindPostgresContainer),
		string(KindMySQLContainer),
		string(KindMongoContainer),
		string(KindFiles),
		string(KindCommand),
	}
}
