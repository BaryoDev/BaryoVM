// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package backup

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/sshx"
)

// Kind is how one backup strategy dumps and restores.
type Kind string

const (
	// KindPostgresContainer runs pg_dump inside a container on the VM. It is what dbContainer,
	// dbName and dbUser always meant.
	KindPostgresContainer Kind = "postgres-container"
	// KindPostgresRemote runs pg_dump from a throwaway postgres client container on the VM against
	// a database that is not on the VM, such as a managed one (#80).
	KindPostgresRemote Kind = "postgres-remote"
	// KindCommand runs a dump line and a restore line the stack supplies. The escape hatch for
	// anything BaryoVM does not know about.
	KindCommand Kind = "command"
	// KindFiles is a tar of listed paths.
	KindFiles Kind = "files"
)

// Strategy is one thing a stack backs up, and how it is restored. A stack has a list of them.
// Restore is declared next to the dump on purpose: a backup nobody has restored is hope.
type Strategy struct {
	Kind Kind `json:"kind"`

	// Container, Database and User are for postgres-container. User defaults to postgres.
	Container string `json:"container,omitempty"`
	Database  string `json:"database,omitempty"`
	User      string `json:"user,omitempty"`

	// Host, Port and Image are for postgres-remote, together with Database and User. Port defaults
	// to 5432. Image is the client image, postgres:17 by default; pg_dump refuses a server newer
	// than itself, so pin it to the server's major version.
	Host  string `json:"host,omitempty"`
	Port  int    `json:"port,omitempty"`
	Image string `json:"image,omitempty"`

	// EnvFile (relative to the stack dir, on the VM) and PasswordVar name where postgres-remote
	// reads the password: a KEY=value file handed to the client container with --env-file, never
	// the value on a command line. PasswordVar is the key holding it, PGPASSWORD by default.
	// PassFile is the other source: an absolute path on the VM to a libpq password file, mounted
	// read-only into the client container as PGPASSFILE.
	EnvFile     string `json:"envFile,omitempty"`
	PasswordVar string `json:"passwordVar,omitempty"`
	PassFile    string `json:"passFile,omitempty"`

	// Dump and Restore are the shell lines for command. Dump writes the backup to stdout and
	// Restore reads it from stdin.
	Dump    string `json:"dump,omitempty"`
	Restore string `json:"restore,omitempty"`

	// Paths are the absolute paths a files strategy archives.
	Paths []string `json:"paths,omitempty"`
}

// KnownKinds lists the kinds this binary runs, for an error message.
func KnownKinds() []string {
	return []string{string(KindPostgresContainer), string(KindPostgresRemote), string(KindCommand), string(KindFiles)}
}

var (
	identRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)
	hostRe  = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.:-]*$`)
	imageRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_./:@-]*$`)
	varRe   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

const defaultImage = "postgres:17"

func (s Strategy) user() string {
	if s.User != "" {
		return s.User
	}
	return "postgres"
}

func (s Strategy) port() int {
	if s.Port > 0 {
		return s.Port
	}
	return 5432
}

func (s Strategy) image() string {
	if s.Image != "" {
		return s.Image
	}
	return defaultImage
}

func (s Strategy) passwordVar() string {
	if s.PasswordVar != "" {
		return s.PasswordVar
	}
	return "PGPASSWORD"
}

// Validate refuses a strategy that cannot run. Every value here ends up in a remote shell line, so
// the check is also part of the injection boundary: values that the tool they are handed to would
// read as an option are refused along with the unsafe ones.
func (s Strategy) Validate() error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("backup kind %s: "+format, append([]any{s.Kind}, a...)...)
	}
	ident := func(field, v string) error {
		if !identRe.MatchString(v) {
			return bad("%s %q must be letters, digits, '.', '-' and '_', and must not start with '-'", field, v)
		}
		return nil
	}
	switch s.Kind {
	case KindPostgresContainer:
		if s.Container == "" || s.Database == "" {
			return bad("needs a container and a database")
		}
		if err := ident("container", s.Container); err != nil {
			return err
		}
		if err := ident("database", s.Database); err != nil {
			return err
		}
		if s.User != "" {
			return ident("user", s.User)
		}
	case KindPostgresRemote:
		if s.Host == "" || s.Database == "" {
			return bad("needs a host and a database")
		}
		if !hostRe.MatchString(s.Host) {
			return bad("host %q is not a plain host name or address", s.Host)
		}
		if err := ident("database", s.Database); err != nil {
			return err
		}
		if s.User != "" {
			if err := ident("user", s.User); err != nil {
				return err
			}
		}
		if s.Port < 0 || s.Port > 65535 {
			return bad("port %d is out of range", s.Port)
		}
		if s.Image != "" && !imageRe.MatchString(s.Image) {
			return bad("image %q is not a plain image reference", s.Image)
		}
		if s.EnvFile == "" && s.PassFile == "" {
			return bad("needs envFile (the stack's env file holding the password) or passFile (a libpq password file on the VM); the password is never taken from the command line")
		}
		if s.EnvFile != "" {
			if err := ValidateEnvFile(s.EnvFile); err != nil {
				return err
			}
			if !varRe.MatchString(s.passwordVar()) {
				return bad("passwordVar %q is not a variable name", s.PasswordVar)
			}
		}
		if s.PassFile != "" {
			if err := validAbs("passFile", s.PassFile); err != nil {
				return bad("%v", err)
			}
			if strings.Contains(s.PassFile, ":") {
				return bad("passFile %q must not contain ':' (it is joined into a docker -v mount)", s.PassFile)
			}
		}
	case KindCommand:
		if strings.TrimSpace(s.Dump) == "" || strings.TrimSpace(s.Restore) == "" {
			return bad("needs both dump and restore: a backup nobody can restore is hope")
		}
	case KindFiles:
		if len(s.Paths) == 0 {
			return bad("needs paths")
		}
		for _, p := range s.Paths {
			if err := validAbs("path", p); err != nil {
				return bad("%v", err)
			}
		}
	case "":
		return fmt.Errorf("a backup strategy has no kind (known kinds: %s)", strings.Join(KnownKinds(), ", "))
	default:
		return fmt.Errorf("backup kind %q is not one this baryovm runs (known kinds: %s); use kind \"command\" for anything else", s.Kind, strings.Join(KnownKinds(), ", "))
	}
	return nil
}

func validAbs(what, p string) error {
	if !strings.HasPrefix(p, "/") {
		return fmt.Errorf("%s %q must be an absolute path on the VM", what, p)
	}
	if p == "/" {
		return fmt.Errorf("%s must not be /", what)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return fmt.Errorf("%s %q must not contain ..", what, p)
		}
	}
	if strings.ContainsAny(p, "\n\r\x00") {
		return fmt.Errorf("%s %q holds a control character", what, p)
	}
	return nil
}

// ValidateStrategies checks a whole list.
func ValidateStrategies(ss []Strategy) error {
	for i, s := range ss {
		if err := s.Validate(); err != nil {
			return fmt.Errorf("backup[%d]: %w", i, err)
		}
	}
	return nil
}

// artifacts names the file each strategy writes, in the backup dir, with $ts left for the shell.
// The first postgres strategy keeps the name every backup has always had, db-$ts.dump, so listing
// and restoring an existing backup directory does not change.
func artifacts(ss []Strategy) []string {
	out := make([]string, len(ss))
	firstPG := true
	for i, s := range ss {
		switch s.Kind {
		case KindPostgresContainer, KindPostgresRemote:
			if firstPG {
				out[i] = "db-$ts.dump"
				firstPG = false
			} else {
				out[i] = fmt.Sprintf("db%d-$ts.dump", i)
			}
		case KindFiles:
			out[i] = fmt.Sprintf("files%d-$ts.tar.gz", i)
		default:
			out[i] = fmt.Sprintf("cmd%d-$ts.out", i)
		}
	}
	return out
}

// remoteClient is the docker run prefix of the throwaway postgres client container. The password
// reaches it through --env-file or a mounted password file, never through an argument.
func (c Config) remoteClient(s Strategy, tool string) string {
	q := sshx.Quote
	var b strings.Builder
	b.WriteString(c.dockerCmd() + " run --rm -i")
	script := "exec " + tool
	if s.PassFile != "" {
		// libpq reads PGPASSFILE as whoever runs it, so the container runs as the SSH user's uid:
		// a 0600 file owned by that user is otherwise unreadable inside.
		b.WriteString(` --user "$(id -u):$(id -g)" -v ` + q(s.PassFile+":/run/pgpass:ro") + " -e PGPASSFILE=/run/pgpass")
	}
	if s.EnvFile != "" {
		b.WriteString(" --env-file " + q(c.Dir+"/"+s.EnvFile))
		if v := s.passwordVar(); v != "PGPASSWORD" {
			script = `PGPASSWORD="${` + v + `}" ` + script
		}
	}
	b.WriteString(" " + q(s.image()))
	b.WriteString(" sh -c " + q(script) + " sh")
	return b.String()
}

func remoteArgs(s Strategy) string {
	q := sshx.Quote
	return strings.Join([]string{q(s.Host), q(fmt.Sprint(s.port())), q(s.user()), q(s.Database)}, " ")
}

// dumpCmd is the remote command that writes strategy s to the path art (a shell word).
func (c Config) dumpCmd(s Strategy, art string) (string, error) {
	q := sshx.Quote
	switch s.Kind {
	case KindPostgresContainer:
		return fmt.Sprintf(c.dockerCmd()+" exec %s pg_dump -U %s -Fc %s > %s", q(s.Container), q(s.user()), q(s.Database), art), nil
	case KindPostgresRemote:
		return c.remoteClient(s, `pg_dump -h "$1" -p "$2" -U "$3" -Fc "$4"`) + " " + remoteArgs(s) + " > " + art, nil
	case KindCommand:
		return groupOrSudo(c.Sudo, s.Dump) + " > " + art, nil
	case KindFiles:
		// -C / with the leading slash stripped, so the archive holds relative paths. The redirect
		// is the SSH user's, so the archive is theirs and nothing has to chown it afterwards.
		return sshx.Sudo(c.Sudo, "tar -czf - -C / "+relPaths(s.Paths)) + " > " + art, nil
	}
	return "", fmt.Errorf("backup kind %q has no dump", s.Kind)
}

// restoreCmd is the remote command that reads art back.
func (c Config) restoreCmd(s Strategy, art string) (string, error) {
	q := sshx.Quote
	switch s.Kind {
	case KindPostgresContainer:
		return fmt.Sprintf(c.dockerCmd()+" exec -i %s dropdb -U %s --force --if-exists %s\n", q(s.Container), q(s.user()), q(s.Database)) +
			fmt.Sprintf(c.dockerCmd()+" exec -i %s createdb -U %s %s\n", q(s.Container), q(s.user()), q(s.Database)) +
			fmt.Sprintf(c.dockerCmd()+" exec -i %s pg_restore -U %s -d %s < %s", q(s.Container), q(s.user()), q(s.Database), art), nil
	case KindPostgresRemote:
		// --clean --if-exists --no-owner: a managed database cannot be dropped and recreated by
		// the client, and its roles are not the dump's roles.
		return c.remoteClient(s, `pg_restore -h "$1" -p "$2" -U "$3" -d "$4" --clean --if-exists --no-owner`) + " " + remoteArgs(s) + " < " + art, nil
	case KindCommand:
		return groupOrSudo(c.Sudo, s.Restore) + " < " + art, nil
	case KindFiles:
		return sshx.Sudo(c.Sudo, "tar -xzf - -C /") + " < " + art, nil
	}
	return "", fmt.Errorf("backup kind %q has no restore", s.Kind)
}

// groupOrSudo runs a stack-supplied line whole: through sshx.SudoShell when the stack uses sudo (a bare
// prefix covers only up to the first &&), and in a brace group otherwise so a redirect applies to
// the entire line and not its last command.
func groupOrSudo(sudo bool, line string) string {
	if sudo {
		return sshx.SudoShell(true, line)
	}
	return "{ " + line + "\n}"
}

func relPaths(paths []string) string {
	rel := make([]string, len(paths))
	for i, p := range paths {
		rel[i] = sshx.Quote(strings.TrimPrefix(p, "/"))
	}
	return strings.Join(rel, " ")
}

// ErrNothingToBackUp is returned when a backup or restore is asked for on a stack that declares an
// empty strategy list.
var ErrNothingToBackUp = fmt.Errorf("this stack declares no backup strategies (backup is an empty list), so there is nothing to back up or restore")

// multiBackupScript is the script for a stack with a strategy list. One script, set -e: every
// strategy runs under one timestamp and the first failure stops it, so a set is never half one
// point in time and half another.
func multiBackupScript(cfg Config) (string, error) {
	if err := ValidateStrategies(cfg.Strategies); err != nil {
		return "", err
	}
	if err := ValidateEnvFile(cfg.EnvFile); err != nil {
		return "", err
	}
	q := sshx.Quote
	names := artifacts(cfg.Strategies)
	var b strings.Builder
	b.WriteString("set -e\numask 077\n")
	b.WriteString(cfg.bkVar())
	b.WriteString("mkdir -p \"$BK\" && chmod 700 \"$BK\"\n")
	b.WriteString("ts=$(date +%Y%m%d-%H%M%S)\n")
	var ran []int
	for i, s := range cfg.Strategies {
		if cfg.Skip[i] {
			continue
		}
		cmd, err := cfg.dumpCmd(s, `"$BK/`+names[i]+`.partial"`)
		if err != nil {
			return "", err
		}
		// The artifact takes its final name only once its command has succeeded, on its own line
		// so set -e stops a failed dump before the rename.
		b.WriteString(cmd + "\n")
		fmt.Fprintf(&b, "mv \"$BK/%s.partial\" \"$BK/%s\"\n", names[i], names[i])
		ran = append(ran, i)
	}
	if len(ran) == 0 {
		return "", ErrNothingToBackUp
	}
	if cfg.EnvFile != "" {
		cp, chown := "cp", ""
		if cfg.Sudo {
			cp = sshx.Sudo(cfg.Sudo, "cp")
			chown = " && " + sshx.Sudo(cfg.Sudo, `chown "$(id -u):$(id -g)" "$BK/env-$ts"`)
		}
		envPath := q(cfg.Dir + "/" + cfg.EnvFile)
		fmt.Fprintf(&b, "if [ -f %s ]; then %s %s \"$BK/env-$ts\"%s && chmod 600 \"$BK/env-$ts\"; fi\n", envPath, cp, envPath, chown)
	}
	seen := map[string]bool{}
	for _, i := range ran {
		glob := strings.Replace(names[i], "$ts", "*", 1)
		if seen[glob] {
			continue
		}
		seen[glob] = true
		fmt.Fprintf(&b, "ls -1t \"$BK\"/%s 2>/dev/null | tail -n +%d | xargs -r rm -f\n", glob, cfg.keep()+1)
	}
	fmt.Fprintf(&b, "ls -1t \"$BK\"/env-* 2>/dev/null | tail -n +%d | xargs -r rm -f\n", cfg.keep()+1)
	for _, i := range ran[1:] {
		fmt.Fprintf(&b, "echo \"backed up %s: %s ($(du -h \"$BK/%s\" | cut -f1))\"\n", cfg.Strategies[i].Kind, names[i], names[i])
	}
	first := names[ran[0]]
	fmt.Fprintf(&b, "sz=$(du -h \"$BK/%s\" | cut -f1)\n", first)
	fmt.Fprintf(&b, "echo \"backup ok: %s ($sz) in $BK\"\n", first)
	return b.String(), nil
}

// multiRestoreScript restores a whole set. file names any artifact of the set (empty means the
// newest); the timestamp in its name picks the rest. Every artifact is checked before the first
// restore starts, so a missing file stops it before anything is replaced.
func multiRestoreScript(cfg Config, file string) (string, error) {
	if err := ValidateStrategies(cfg.Strategies); err != nil {
		return "", err
	}
	names := artifacts(cfg.Strategies)
	var b strings.Builder
	b.WriteString("set -e\n")
	b.WriteString(cfg.bkVar())
	if file == "" {
		// The newest set with every artifact present. A set whose backup failed part way is
		// missing one, and a .partial file never matches the glob, so it is not picked.
		b.WriteString("f=\n")
		b.WriteString("for c in \"$BK\"/" + strings.Replace(names[0], "$ts", "*", 1) + "; do\n")
		b.WriteString("  [ -f \"$c\" ] || continue\n")
		b.WriteString(`  ts=$(basename "$c" | grep -oE '[0-9]{8}-[0-9]{6}' | head -1)` + "\n")
		b.WriteString("  ok=1\n")
		for i := range cfg.Strategies {
			fmt.Fprintf(&b, "  [ -f \"$BK/%s\" ] || ok=0\n", names[i])
		}
		b.WriteString("  if [ \"$ok\" = 1 ]; then f=$c; fi\n")
		b.WriteString("done\n")
	} else {
		b.WriteString("f=" + sshx.Quote(file) + "\n")
		b.WriteString("case \"$f\" in /*) ;; *) f=\"$BK/$f\" ;; esac\n")
	}
	b.WriteString(`[ -n "$f" ] && [ -f "$f" ] || { echo "backup not found: $f" >&2; exit 1; }` + "\n")
	b.WriteString(`ts=$(basename "$f" | grep -oE '[0-9]{8}-[0-9]{6}' | head -1)` + "\n")
	b.WriteString(`[ -n "$ts" ] || { echo "no timestamp in $f" >&2; exit 1; }` + "\n")
	for i := range cfg.Strategies {
		fmt.Fprintf(&b, "[ -f \"$BK/%s\" ] || { echo \"backup not found: $BK/%s\" >&2; exit 1; }\n", names[i], names[i])
	}
	b.WriteString(`echo "restoring set $ts"` + "\n")
	for i, s := range cfg.Strategies {
		cmd, err := cfg.restoreCmd(s, `"$BK/`+names[i]+`"`)
		if err != nil {
			return "", err
		}
		b.WriteString(cmd + "\n")
	}
	b.WriteString(`echo "restored from set $ts"` + "\n")
	return b.String(), nil
}

// multiListGlobs is the ls arguments that cover every artifact the strategies write.
func multiListGlobs(cfg Config) string {
	seen := map[string]bool{}
	var globs []string
	for _, n := range artifacts(cfg.Strategies) {
		g := strings.Replace(n, "$ts", "*", 1)
		if !seen[g] {
			seen[g] = true
			globs = append(globs, "\"$BK\"/"+g)
		}
	}
	return strings.Join(globs, " ")
}

// SkipMissingContainers marks the postgres-container strategies whose container is not on the host
// yet, as on a first release, so the pre-release backup leaves them out. It reports whether that
// leaves nothing to run, in which case the whole backup is skipped. A probe that errors skips
// nothing: it falls through to the backup, which then fails loudly if the container is really gone.
func SkipMissingContainers(c sshx.Runner, cfg *Config) bool {
	if len(cfg.Strategies) == 0 {
		ok, err := ContainerExists(c, *cfg)
		return err == nil && !ok
	}
	cfg.Skip = map[int]bool{}
	for i, s := range cfg.Strategies {
		if s.Kind != KindPostgresContainer {
			continue
		}
		probe := *cfg
		probe.DBContainer = s.Container
		if ok, err := ContainerExists(c, probe); err == nil && !ok {
			cfg.Skip[i] = true
		}
	}
	return len(cfg.Skip) == len(cfg.Strategies)
}
