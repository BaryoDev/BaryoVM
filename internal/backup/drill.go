// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package backup

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/sshx"
)

// DrillNote is what a drill says before it runs. It changes nothing live, so it needs no --yes.
const DrillNote = "restore drill: restores into a throwaway postgres container and removes it afterwards. " +
	"The live database and its container are not touched, so no --yes is needed."

// DrillResult is what a finished drill reports.
type DrillResult struct {
	Drill     bool   `json:"drill"`
	File      string `json:"file"`
	Tables    int    `json:"tables"`
	Container string `json:"container"`
	// Removed is true once the throwaway container is confirmed gone.
	Removed bool `json:"removed"`
	// Touched is always false: the drill never writes to the live database or container.
	Touched bool `json:"liveTouched"`
}

// DrillContainerName is the throwaway container's name for one drill. stamp makes it unique.
func DrillContainerName(stack, stamp string) string {
	clean := func(s string) string {
		return strings.Map(func(r rune) rune {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
				return r
			}
			return '-'
		}, s)
	}
	return "baryovm-drill-" + clean(stack) + "-" + clean(stamp)
}

// DrillCleanupCmd force-removes the throwaway container and its anonymous volume. It succeeds when
// the container is already gone.
func (c Config) DrillCleanupCmd(drill string) string {
	return c.dockerCmd() + " rm -f -v " + sshx.Quote(drill) + " >/dev/null 2>&1 || true"
}

// DrillScript builds the script a drill runs, without running it. file is a backup name or path,
// newest when empty. drill is the throwaway container's name.
//
// The live container is named once, by a read-only `docker inspect` that learns its image. Every
// write goes to the throwaway container, which has no network and no mounts. The cleanup is an EXIT
// trap, so it runs on every failure path after the trap is set.
func DrillScript(cfg Config, file, drill string) string {
	q := sshx.Quote
	docker := cfg.dockerCmd()
	var b strings.Builder
	b.WriteString("set -e\n")
	b.WriteString(cfg.bkVar())
	b.WriteString("D=" + q(drill) + "\n")
	b.WriteString("trap '" + docker + " rm -f -v \"$D\" >/dev/null 2>&1 || true; echo \"drill: removed $D\"' EXIT\n")
	b.WriteString("trap 'exit 1' HUP INT TERM\n")
	if file == "" {
		b.WriteString("f=$(ls -1t \"$BK\"/db-*.dump | head -1)\n")
	} else {
		b.WriteString("f=" + q(file) + "\n")
		b.WriteString("case \"$f\" in /*) ;; *) f=\"$BK/$f\" ;; esac\n")
	}
	b.WriteString(`[ -n "$f" ] && [ -f "$f" ] || { echo "backup not found: $f" >&2; exit 1; }` + "\n")
	b.WriteString(`echo "drill: file=$f"` + "\n")
	fmt.Fprintf(&b, "img=$(%s inspect -f '{{.Config.Image}}' %s)\n", docker, q(cfg.DBContainer))
	b.WriteString(`[ -n "$img" ] || { echo "cannot read the image of the database container" >&2; exit 1; }` + "\n")
	fmt.Fprintf(&b, "%s run -d --name \"$D\" --network none -e POSTGRES_HOST_AUTH_METHOD=trust -e POSTGRES_USER=%s \"$img\" >/dev/null\n",
		docker, q(cfg.user()))
	fmt.Fprintf(&b, "ready=0; for i in $(seq 1 60); do if %s exec \"$D\" pg_isready -h 127.0.0.1 -U %s -q; then ready=1; break; fi; sleep 1; done\n",
		docker, q(cfg.user()))
	b.WriteString(`[ "$ready" = 1 ] || { echo "throwaway postgres did not become ready" >&2; exit 1; }` + "\n")
	fmt.Fprintf(&b, "%s exec -i \"$D\" createdb -U %s %s\n", docker, q(cfg.user()), q(cfg.DBName))
	fmt.Fprintf(&b, "%s exec -i \"$D\" pg_restore --no-owner --no-privileges --exit-on-error -U %s -d %s < \"$f\"\n",
		docker, q(cfg.user()), q(cfg.DBName))
	fmt.Fprintf(&b, "tables=$(%s exec -i \"$D\" psql -U %s -d %s -tA -c %s)\n", docker, q(cfg.user()), q(cfg.DBName),
		q("select count(*) from information_schema.tables where table_schema not in ('pg_catalog','information_schema')"))
	b.WriteString(`[ "$tables" -gt 0 ] || { echo "restored database has no tables" >&2; exit 1; }` + "\n")
	b.WriteString(`echo "drill: tables=$tables"` + "\n")
	b.WriteString(`echo "drill ok"` + "\n")
	return b.String()
}

// DrillGoneCmd prints "gone" when the throwaway container no longer exists and "present" when it
// does. It asks docker rather than trusting the removal, which can fail quietly (sudo denied).
func (c Config) DrillGoneCmd(drill string) string {
	return "if " + c.dockerCmd() + " inspect " + sshx.Quote(drill) + " >/dev/null 2>&1; then echo present; else echo gone; fi"
}

// Drill runs the drill and, whatever happened, removes the container again from the client side so
// a dropped connection cannot leave it behind, then checks it is gone. Removed comes from that
// check, not from the removal's exit status. The result is filled in as far as it got.
func Drill(c sshx.Runner, cfg Config, file, drill string) (DrillResult, error) {
	r := DrillResult{Drill: true, Container: drill}
	out, err := c.Run(DrillScript(cfg, file, drill))
	r = ParseDrill(out, r)
	_, _ = c.Run(cfg.DrillCleanupCmd(drill))
	state, serr := c.Run(cfg.DrillGoneCmd(drill))
	r.Removed = serr == nil && strings.TrimSpace(state) == "gone"
	if !r.Removed {
		left := fmt.Errorf("throwaway container %s is still on the VM: remove it by hand with `%s`", drill, cfg.dockerCmd()+" rm -f -v "+drill)
		if err != nil {
			return r, fmt.Errorf("%w; %v", err, left)
		}
		return r, left
	}
	return r, err
}

// ParseDrill reads the `drill: key=value` lines out of a drill's output into r.
func ParseDrill(out string, r DrillResult) DrillResult {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "drill: file="):
			r.File = strings.TrimPrefix(line, "drill: file=")
		case strings.HasPrefix(line, "drill: tables="):
			r.Tables, _ = strconv.Atoi(strings.TrimPrefix(line, "drill: tables="))
		}
	}
	return r
}
