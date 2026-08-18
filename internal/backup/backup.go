// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package backup takes and restores backups of a compose stack's Postgres
// database (and its .env/config) over SSH, so "did you back it up?" is one
// command, not a hand-rolled pg_dump every time.
package backup

import (
	"fmt"
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/sshx"
)

// Config describes what to back up for a stack.
type Config struct {
	// Sudo elevates the docker and file operations. A stack whose .env is root-owned (which is the
	// right thing for a file holding a database password) cannot be read, dumped or copied by an
	// ordinary SSH user at all.
	Sudo        bool
	Name        string // stack name (used for the default backup dir)
	Dir         string // project dir (holds the env/config files)
	EnvFile     string // config file to copy, relative to Dir (e.g. ".env"); empty skips it
	DBContainer string // postgres container name (e.g. deploy-postgres-1)
	DBName      string // database to dump
	DBUser      string // defaults to "postgres"
	BackupDir   string // remote dir to store backups; defaults to ~/<name>-backups
	Keep        int    // how many of each kind to retain; defaults to 14
}

// dockerCmd is `docker`, elevated when the stack needs it.
func (c Config) dockerCmd() string {
	if c.Sudo {
		return "sudo -n docker"
	}
	return "docker"
}

func (c Config) user() string {
	if c.DBUser != "" {
		return c.DBUser
	}
	return "postgres"
}

func (c Config) keep() int {
	if c.Keep > 0 {
		return c.Keep
	}
	return 14
}

// bkVar emits a `BK=...` shell line resolving the backup dir (double-quoted so a
// $HOME-based default expands), and returns the script prefix.
func (c Config) bkVar() string {
	if c.BackupDir != "" {
		return "BK=" + sshx.Quote(c.BackupDir) + "\n"
	}
	return fmt.Sprintf("BK=\"$HOME/%s-backups\"\n", c.Name)
}

// Backup dumps the DB (custom format) + copies the config, then prunes old ones.
func Backup(c *sshx.Client, cfg Config) (string, error) {
	q := sshx.Quote
	var b strings.Builder
	b.WriteString("set -e\n")
	b.WriteString(cfg.bkVar())
	b.WriteString("mkdir -p \"$BK\"\n")
	b.WriteString("ts=$(date +%Y%m%d-%H%M%S)\n")
	b.WriteString(fmt.Sprintf(cfg.dockerCmd()+" exec %s pg_dump -U %s -Fc %s > \"$BK/db-$ts.dump\"\n",
		q(cfg.DBContainer), q(cfg.user()), q(cfg.DBName)))
	if cfg.EnvFile != "" {
		// The config file is the reason Sudo exists: it is the one holding the database password, so
		// it is often root-owned and mode 600. Copy it the same way the rest of the stack is reached,
		// and hand ownership of the copy to the SSH user so listing and restoring do not need root
		// again. chmod stays 600; a readable backup of a secrets file is still a secrets file.
		cp, chown := "cp", ""
		if cfg.Sudo {
			cp = "sudo -n cp"
			chown = " && sudo -n chown \"$(id -u):$(id -g)\" \"$BK/env-$ts\""
		}
		b.WriteString(fmt.Sprintf("if [ -f %s/%s ]; then %s %s/%s \"$BK/env-$ts\"%s && chmod 600 \"$BK/env-$ts\"; fi\n",
			q(cfg.Dir), cfg.EnvFile, cp, q(cfg.Dir), cfg.EnvFile, chown))
	}
	// Retention: keep the newest N of each kind.
	b.WriteString(fmt.Sprintf("ls -1t \"$BK\"/db-*.dump 2>/dev/null | tail -n +%d | xargs -r rm -f\n", cfg.keep()+1))
	b.WriteString(fmt.Sprintf("ls -1t \"$BK\"/env-* 2>/dev/null | tail -n +%d | xargs -r rm -f\n", cfg.keep()+1))
	b.WriteString("sz=$(du -h \"$BK/db-$ts.dump\" | cut -f1)\n")
	b.WriteString(`echo "backup ok: db-$ts.dump ($sz) in $BK"` + "\n")
	return c.Run(b.String())
}

// List shows the available DB backups, newest first.
func List(c *sshx.Client, cfg Config) (string, error) {
	var b strings.Builder
	b.WriteString(cfg.bkVar())
	b.WriteString("ls -1t \"$BK\"/db-*.dump 2>/dev/null || echo '(no backups yet)'\n")
	return c.Run(b.String())
}

// Restore replaces the database from a backup (newest if file is empty). The
// Postgres container must be running; the caller should restart app services after.
func Restore(c *sshx.Client, cfg Config, file string) (string, error) {
	q := sshx.Quote
	var b strings.Builder
	b.WriteString("set -e\n")
	b.WriteString(cfg.bkVar())
	if file == "" {
		b.WriteString("f=$(ls -1t \"$BK\"/db-*.dump | head -1)\n")
	} else {
		b.WriteString("f=" + q(file) + "\n")
		b.WriteString("case \"$f\" in /*) ;; *) f=\"$BK/$f\" ;; esac\n")
	}
	b.WriteString(`[ -n "$f" ] && [ -f "$f" ] || { echo "backup not found: $f" >&2; exit 1; }` + "\n")
	b.WriteString(`echo "restoring $f"` + "\n")
	b.WriteString(fmt.Sprintf(cfg.dockerCmd()+" exec -i %s dropdb -U %s --force --if-exists %s\n",
		q(cfg.DBContainer), q(cfg.user()), q(cfg.DBName)))
	b.WriteString(fmt.Sprintf(cfg.dockerCmd()+" exec -i %s createdb -U %s %s\n",
		q(cfg.DBContainer), q(cfg.user()), q(cfg.DBName)))
	b.WriteString(fmt.Sprintf(cfg.dockerCmd()+" exec -i %s pg_restore -U %s -d %s < \"$f\"\n",
		q(cfg.DBContainer), q(cfg.user()), q(cfg.DBName)))
	b.WriteString(`echo "restored from $f"` + "\n")
	return c.Run(b.String())
}
