// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package recipe

import (
	"fmt"
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/sshx"
)

// DumpCmd and RestoreCmd build the remote shell for one strategy. They are pure functions returning
// a string, which is how the rest of this repository is testable without a host or a Docker daemon.
//
// Every interpolated value goes through sshx.Quote. That is the injection boundary named in
// CLAUDE.md, and it matters more here than usual: a container name, a database name and a file path
// all arrive from a recipe published by somebody else.

// Artifact is the file a strategy writes, relative to the backup directory. The timestamp is a
// shell variable the surrounding script sets, so one backup run stamps every artifact identically
// rather than each strategy calling date and drifting across a slow dump.
func (s Strategy) Artifact(index int) string {
	switch s.Kind {
	case KindPostgresContainer:
		return fmt.Sprintf("db-%d-$ts.dump", index)
	case KindMySQLContainer:
		return fmt.Sprintf("db-%d-$ts.sql", index)
	case KindMongoContainer:
		return fmt.Sprintf("db-%d-$ts.archive", index)
	case KindFiles:
		return fmt.Sprintf("files-%d-$ts.tar.gz", index)
	case KindCommand:
		return fmt.Sprintf("cmd-%d-$ts.dump", index)
	default:
		return fmt.Sprintf("backup-%d-$ts", index)
	}
}

// DumpCmd is the remote command that writes this strategy's artifact into $BK.
//
// sudo is the stack's setting, threaded through rather than decided here: a stack whose compose
// runs as root has containers only root can exec into, and a backup that silently runs as the SSH
// user fails at the first docker call.
func (s Strategy) DumpCmd(index int, sudo bool) (string, error) {
	q := sshx.Quote
	art := `"$BK/` + s.Artifact(index) + `"`
	docker := sshx.Sudo(sudo, "docker")

	switch s.Kind {
	case KindPostgresContainer:
		// -Fc is the custom format: compressed, and restorable into a database that does not exist
		// yet, which is what a restore drill needs.
		return fmt.Sprintf("%s exec %s pg_dump -U %s -Fc %s > %s",
			docker, q(s.Container), q(s.dbUser()), q(s.Database), art), nil

	case KindMySQLContainer:
		// --single-transaction so the dump is consistent without locking the tables a running site
		// is reading. It is a no-op on MyISAM, which is the case this cannot fix.
		return fmt.Sprintf("%s exec %s mysqldump --single-transaction -u %s %s > %s",
			docker, q(s.Container), q(s.dbUser()), q(s.Database), art), nil

	case KindMongoContainer:
		return fmt.Sprintf("%s exec %s mongodump --archive --gzip --db %s > %s",
			docker, q(s.Container), q(s.Database), art), nil

	case KindFiles:
		// -C / with paths stripped of their leading slash, so the archive holds relative paths and
		// tar does not refuse to restore them. Absolute paths in an archive are how a restore
		// writes somewhere nobody expected.
		var rel []string
		for _, p := range s.Paths {
			rel = append(rel, q(strings.TrimPrefix(p, "/")))
		}
		return sshx.Sudo(sudo, fmt.Sprintf("tar -czf %s -C / %s", art, strings.Join(rel, " "))), nil

	case KindCommand:
		// The recipe's own command, wrapped whole rather than prefixed. A bare sudo prefix covers
		// only up to the first &&, which is the half-applied failure SudoShell exists for.
		return fmt.Sprintf("%s > %s", sshx.SudoShell(sudo, s.Dump), art), nil

	default:
		return "", fmt.Errorf("strategy %d has kind %q, which has no dump command", index, s.Kind)
	}
}

// RestoreCmd is the remote command that reads an artifact back.
//
// file is the artifact's name inside $BK, chosen by the operator from a listing rather than
// computed here, so restoring an older backup is the same command as restoring the newest.
func (s Strategy) RestoreCmd(file string, sudo bool) (string, error) {
	q := sshx.Quote
	art := `"$BK/"` + q(file)
	docker := sshx.Sudo(sudo, "docker")

	switch s.Kind {
	case KindPostgresContainer:
		// --clean --if-exists rather than dropping the database: a restore into a live stack cannot
		// drop a database something is connected to, and failing on the DROP after the dump has
		// already been read is the worst place to stop.
		return fmt.Sprintf("%s exec -i %s pg_restore -U %s -d %s --clean --if-exists < %s",
			docker, q(s.Container), q(s.dbUser()), q(s.Database), art), nil

	case KindMySQLContainer:
		return fmt.Sprintf("%s exec -i %s mysql -u %s %s < %s",
			docker, q(s.Container), q(s.dbUser()), q(s.Database), art), nil

	case KindMongoContainer:
		return fmt.Sprintf("%s exec -i %s mongorestore --archive --gzip --drop < %s",
			docker, q(s.Container), art), nil

	case KindFiles:
		return sshx.Sudo(sudo, fmt.Sprintf("tar -xzf %s -C /", art)), nil

	case KindCommand:
		return fmt.Sprintf("%s < %s", sshx.SudoShell(sudo, s.Restore), art), nil

	default:
		return "", fmt.Errorf("kind %q has no restore command", s.Kind)
	}
}

// dbUser is the strategy's user, or the kind's default.
func (s Strategy) dbUser() string {
	if strings.TrimSpace(s.User) != "" {
		return s.User
	}
	return s.Kind.DefaultUser()
}

// BackupScript is the whole remote script for every strategy on a stack: one timestamp, every
// artifact, then retention.
//
// set -e is the reason this is one script rather than one command per strategy. A stack with a
// database and an uploads directory has two artifacts that belong to the same moment, and a second
// dump running after the first failed produces a backup set that is half one point in time and half
// another, which is worse than no backup because it looks complete.
func BackupScript(strategies []Strategy, backupDir, stackName string, keep int, sudo bool) (string, error) {
	if len(strategies) == 0 {
		// A stack that declares no strategies has nothing to back up, and saying so is not the same
		// as running a script that writes nothing and reports success.
		return "", ErrNoStrategies
	}
	if keep < 1 {
		keep = 14
	}

	var b strings.Builder
	b.WriteString("set -e\n")
	if backupDir != "" {
		b.WriteString("BK=" + sshx.Quote(backupDir) + "\n")
	} else {
		// Quoted the same way backupDir is. The unquoted form was reachable: a stack named
		// `x";whoami;echo "` closed the quote and ran a command on the VM. $HOME stays outside the
		// quoting so the shell still expands it, and the rest is one quoted literal.
		b.WriteString(`BK="$HOME/"` + sshx.Quote(stackName+"-backups") + "\n")
	}
	b.WriteString("mkdir -p \"$BK\"\n")
	// One timestamp for the whole run. Each strategy calling date would drift across a slow dump
	// and produce a set nobody can match up afterwards.
	b.WriteString("ts=$(date +%Y%m%d-%H%M%S)\n")

	for i, s := range strategies {
		cmd, err := s.DumpCmd(i, sudo)
		if err != nil {
			return "", err
		}
		b.WriteString(cmd + "\n")
	}

	// Retention per artifact shape, so a stack with a database and files keeps N of each rather
	// than N in total, which would silently drop the older kind.
	seen := map[string]bool{}
	for i, s := range strategies {
		pattern := strings.Replace(s.Artifact(i), "$ts", "*", 1)
		if seen[pattern] {
			continue
		}
		seen[pattern] = true
		b.WriteString(fmt.Sprintf("ls -1t \"$BK\"/%s 2>/dev/null | tail -n +%d | xargs -r rm -f\n", pattern, keep+1))
	}

	b.WriteString(fmt.Sprintf("echo \"backup ok: %d artifact(s) at $ts in $BK\"\n", len(strategies)))
	return b.String(), nil
}

// ErrNoStrategies is returned when a backup is asked for on a stack that declares none. It is an
// error rather than a quiet success: a stack with genuinely nothing to back up says so with an
// empty list, and the caller decides whether that is fine, rather than a script reporting "backup
// ok" having written no file.
var ErrNoStrategies = fmt.Errorf("this stack declares no backup strategies, so there is nothing to back up")
