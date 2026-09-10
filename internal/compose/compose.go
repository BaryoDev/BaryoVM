// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package compose drives `docker compose` on a VM over SSH, the day-to-day
// path for real workloads (barakoCMS, BaryoClub) that run as compose stacks
// rather than single containers.
package compose

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/sshx"
)

// Stack points at a remote compose project.
type Stack struct {
	Dir  string // project directory, e.g. /opt/barakocms
	File string // compose file name; empty uses compose's default
	// Sudo runs compose through sudo.
	//
	// Needed more often than it sounds. A project whose .env holds the database password is
	// reasonably kept root-owned and mode 600, and compose reads that file, so every command fails
	// with "permission denied" for an ordinary SSH user, even though Docker itself is reachable.
	// Loosening the file to fix the tool would be the wrong trade.
	Sudo bool
}

// base is the unelevated `cd <dir> && docker compose [-f <file>]` prefix. It is not a command on
// its own: everything goes through cmd, which appends the subcommand and elevates the result.
func (s Stack) base() string {
	cmd := "docker compose"
	if s.File != "" {
		cmd += " -f " + sshx.Quote(s.File)
	}
	return "cd " + sshx.Quote(s.Dir) + " && " + cmd
}

// cmd builds one complete compose invocation and elevates the whole of it.
//
// The elevation has to wrap the finished command, not the prefix. Wrapping the prefix leaves the
// subcommand outside the quoted shell (`sudo -n sh -c '... docker compose' ps -q 'api'`), which is
// a different command that happens to look right in a diff.
//
// And the cd belongs inside the elevation rather than in front of it: the directory it enters is
// frequently the root-owned one that made Sudo necessary, so `cd <dir> && sudo -n docker compose`
// fails at the cd, as the SSH user, before sudo is reached. Same reasoning as the release hooks.
//
// A stack that did not ask for root is byte-for-byte unchanged: SudoShell returns its argument.
func (s Stack) cmd(args string) string {
	return sshx.SudoShell(s.Sudo, s.base()+args)
}

// docker returns a plain `docker` invocation (not compose), honouring the same sudo choice.
func (s Stack) docker() string { return sshx.Sudo(s.Sudo, "docker") }

// UpOptions controls a deploy.
type UpOptions struct {
	Services      []string
	Pull          bool
	ForceRecreate bool
	NoDeps        bool
}

// Up brings the stack (or selected services) up in the background, optionally
// pulling first. Mirrors the manual `compose up -d --force-recreate` flow.
func Up(c sshx.Runner, s Stack, o UpOptions) (string, error) {
	var out strings.Builder
	if o.Pull {
		p, err := Pull(c, s, o.Services)
		out.WriteString(p)
		if err != nil {
			return out.String(), err
		}
	}
	args := " up -d"
	if o.ForceRecreate {
		args += " --force-recreate"
	}
	if o.NoDeps {
		args += " --no-deps"
	}
	args += services(o.Services)
	r, err := c.Run(s.cmd(args))
	out.WriteString(r)
	return out.String(), err
}

// Pull fetches the latest images for the stack (or selected services).
func Pull(c sshx.Runner, s Stack, svcs []string) (string, error) {
	return c.Run(s.PullCmd(svcs))
}

// PullCmd is the command Pull runs. Exposed so it can be asserted without a host.
func (s Stack) PullCmd(svcs []string) string { return s.cmd(" pull" + services(svcs)) }

// PullUpdatable is Pull for the update path, tolerating images that cannot be pulled.
//
// Real stacks mix registry images with ones built on the host. BaryoClub runs postgres from Docker
// Hub alongside a locally built api and web. A plain pull fails the whole command on the first
// local-only image, which would make those stacks permanently un-updatable. Skipping them is right:
// an image with no registry to check cannot have a newer version to find.
func PullUpdatable(c sshx.Runner, s Stack, svcs []string) (string, error) {
	return c.Run(s.PullUpdatableCmd(svcs))
}

// PullUpdatableCmd is the command PullUpdatable runs.
func (s Stack) PullUpdatableCmd(svcs []string) string {
	return s.cmd(" pull --ignore-pull-failures" + services(svcs))
}

// Ps lists the stack's containers.
func Ps(c sshx.Runner, s Stack) (string, error) {
	return c.Run(s.cmd(" ps"))
}

// Image describes one service's deployed-versus-declared state.
type Image struct {
	Service string // compose service name
	Ref     string // declared in the compose file, e.g. ghcr.io/baryodev/barako-cms:playground
	ID      string // what Ref resolves to on this host right now, the target
	Running string // what the container is actually running, which may lag behind Ref after a pull
}

// Stale reports that the container is running something other than what its reference now points at.
// This, rather than "did the pull change anything", is what an update should act on: it catches a tag
// moved by a pull and a tag moved by a local rebuild alike, and it stays true until the container is
// actually recreated, so an interrupted update is still visible as pending afterwards.
func (i Image) Stale() bool {
	return i.ID != "" && i.Running != "" && i.ID != i.Running
}

// Images reports, for each service that declares an image, the reference from the compose file and
// the id that reference resolves to right now.
//
// The reference has to come from the compose file, not from `ps` or `compose images`. Those report
// what the container is actually running, which on a host that resolved a tag to a digest is a bare
// sha256 with a Repository of "sha256", which is useless as a rollback target, since `docker tag` needs a
// name. The id is what makes rollback possible at all: once a pull moves the tag, the previous image
// survives on the host as an untagged id and nothing else points at it.
func Images(c sshx.Runner, s Stack, svcs []string) ([]Image, error) {
	// The Go template over `config` avoids depending on a JSON shape that differs between compose
	// versions, and skips build-only services, which have no image to pull or roll back.
	out, err := c.Run(s.ConfigCmd())
	if err != nil {
		return nil, err
	}
	refs, err := parseConfigImages(out)
	if err != nil {
		return nil, err
	}

	wanted := map[string]bool{}
	for _, s := range svcs {
		if s != "" {
			wanted[s] = true
		}
	}

	running, err := runningImages(c, s, svcs)
	if err != nil {
		return nil, err
	}

	var imgs []Image
	for _, svc := range sortedKeys(refs) {
		if len(wanted) > 0 && !wanted[svc] {
			continue
		}
		ref := refs[svc]
		img := Image{Service: svc, Ref: ref, Running: running[svc]}
		if id, err := c.Run(s.docker() + " image inspect --format '{{.Id}}' " + sshx.Quote(ref)); err == nil {
			img.ID = strings.TrimSpace(id)
		}
		// A reference with no local image cannot be compared or rolled back to; it is recorded so
		// the caller can report it rather than silently dropping the service.
		imgs = append(imgs, img)
	}
	return imgs, nil
}

// ConfigCmd asks compose for the resolved project. This, not `ps`, is where an image *reference*
// comes from. See Images.
func (s Stack) ConfigCmd() string { return s.cmd(" config --format json") }

// runningImages maps service -> the image id its container is actually running.
func runningImages(c sshx.Runner, s Stack, svcs []string) (map[string]string, error) {
	out, err := c.Run(s.cmd(` ps -a --format '{{.Service}}\t{{.Image}}'` + services(svcs)))
	if err != nil {
		return nil, err
	}
	running := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "\t", 2)
		if len(parts) != 2 {
			continue
		}
		svc, img := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		if svc == "" || img == "" {
			continue
		}
		// Depending on the compose version this is either a bare image id or a reference; resolve a
		// reference so both sides of the comparison are ids.
		if strings.HasPrefix(img, "sha256:") {
			running[svc] = img
			continue
		}
		if id, err := c.Run(s.docker() + " image inspect --format '{{.Id}}' " + sshx.Quote(img)); err == nil {
			running[svc] = strings.TrimSpace(id)
		}
	}
	return running, nil
}

// parseConfigImages pulls service -> image out of `docker compose config --format json`.
func parseConfigImages(jsonOut string) (map[string]string, error) {
	var cfg struct {
		Services map[string]struct {
			Image string `json:"image"`
		} `json:"services"`
	}
	if err := json.Unmarshal([]byte(jsonOut), &cfg); err != nil {
		return nil, fmt.Errorf("reading compose config: %w", err)
	}
	refs := map[string]string{}
	for name, svc := range cfg.Services {
		if svc.Image != "" {
			refs[name] = svc.Image
		}
	}
	return refs, nil
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Retag points a reference back at a specific image id, so `up -d` recreates from it. This is how an
// update is undone: the tag has already moved to the new image, and the old one survives only as an
// id until the next prune.
func Retag(c sshx.Runner, s Stack, id, ref string) (string, error) {
	return c.Run(s.docker() + " tag " + sshx.Quote(id) + " " + sshx.Quote(ref))
}

// Logs returns recent logs for the stack (or selected services).
func Logs(c sshx.Runner, s Stack, svcs []string, tail int) (string, error) {
	args := " logs --no-color"
	if tail > 0 {
		args += " --tail " + strconv.Itoa(tail)
	}
	return c.Run(s.cmd(args + services(svcs)))
}

// LogsResult is a logs read, described well enough that an empty one cannot be mistaken for a
// failed one, or for a stack that is not running.
//
// An empty log is a real answer: a container whose app logs to a file inside it writes nothing to
// stdout, which is the default for most .NET templates. Reporting that as {"output": ""} made it
// byte-for-byte identical to looking at the wrong container, the wrong host, or never reaching
// Docker at all, and the reader has no way to tell which they got.
type LogsResult struct {
	Output string `json:"output"`
	Lines  int    `json:"lines"`
	State  string `json:"state"`
	Note   string `json:"note,omitempty"`
}

// The states a read can be in. They exist because `docker compose logs` answers exit 0 with zero
// bytes for two different situations, and a machine consumer has to act on them differently: a
// silent container wants its logging config looked at, a stack with no containers wants deploying.
const (
	LogsRead       = "read"        // lines came back
	LogsSilent     = "silent"      // no lines, but the stack has containers
	LogsNotRunning = "not-running" // no lines, because the stack has no containers
	LogsUnknown    = "unknown"     // no lines, and asking for the containers failed
)

// Notes for the states a human needs explaining. A read with lines in it explains itself.
const (
	SilentLogsNote = "the stack's containers are running and wrote nothing to stdout or stderr: " +
		"an app that logs to a file inside the container shows nothing here"
	// Deliberately "nothing running" rather than "no containers": compose ps -q
	// lists running containers, so a container that exited, or one stopped service
	// in an otherwise running stack, reaches this note too. Saying the stack has no
	// containers would be wrong in both those cases.
	NotRunningLogsNote = "nothing is running for this stack, so there is nothing to log: " +
		"start it with `baryovm stack deploy`, or check `baryovm stack ps`"
	UnknownLogsNote = "no logs came back and the container check did not answer either: " +
		"check `baryovm stack ps`"
)

// PsQuietCmd lists the ids of the stack's containers, one per line, and prints nothing at all when
// the project has none. It is how a silent container is told from an absent one.
func (s Stack) PsQuietCmd(svcs []string) string { return s.cmd(" ps -q" + services(svcs)) }

// ReadLogs fetches the stack's recent logs and says which of the three zero-exit outcomes it got.
//
// A read with lines in it costs one round trip, as before. A read with none costs a second, for
// `compose ps -q`, because that is the only thing that separates "running and silent" from "not
// running", and the two must not serialize the same.
func ReadLogs(c sshx.Runner, s Stack, svcs []string, tail int) (LogsResult, error) {
	out, err := Logs(c, s, svcs, tail)
	if err != nil {
		return LogsResult{}, err
	}
	r := describeLogs(out)
	if r.Lines > 0 {
		return r, nil
	}
	ids, err := c.Run(s.PsQuietCmd(svcs))
	if err != nil {
		return r, nil
	}
	return r.withContainers(ids), nil
}

// describeLogs counts what compose returned.
//
// Blank lines do not count. Output that is only newlines is nothing read, and treating it as a
// line would put the ambiguity straight back. A read with no lines is left LogsUnknown, because
// the output on its own cannot say why there were none: that is withContainers' job.
func describeLogs(out string) LogsResult {
	r := LogsResult{Output: out, State: LogsRead}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) != "" {
			r.Lines++
		}
	}
	if r.Lines == 0 {
		r.State, r.Note = LogsUnknown, UnknownLogsNote
	}
	return r
}

// withContainers resolves a zero-line read with the answer to PsQuietCmd: ids mean the containers
// are running and silent, no ids mean there is nothing running to log.
func (r LogsResult) withContainers(ids string) LogsResult {
	if r.Lines > 0 {
		return r
	}
	if strings.TrimSpace(ids) == "" {
		r.State, r.Note = LogsNotRunning, NotRunningLogsNote
		return r
	}
	r.State, r.Note = LogsSilent, SilentLogsNote
	return r
}

func services(svcs []string) string {
	var b strings.Builder
	for _, s := range svcs {
		if s == "" {
			continue
		}
		b.WriteString(" " + sshx.Quote(s))
	}
	return b.String()
}
