// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package prune frees disk on a VM by deleting only what can be rebuilt or is no longer referenced.
//
// It works in two steps. DiscoverCmd is one read-only script whose output ParseFacts turns into
// Facts. BuildPlan is a pure function from Facts to a list of items, and every never-delete rule
// lives there so each rule is tested without a host. Apply runs the plan as one script under
// flock, so two applies cannot overlap.
//
// The rules, all enforced in BuildPlan:
//   - a named volume is never planned (only 64-hex anonymous names)
//   - an image used by any container, running or stopped, is never planned
//   - bin, obj and TestResults are planned only beside a .csproj or .fsproj
//   - a worktree is planned only when clean and with no commits off the remote
package prune

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BaryoDev/BaryoVM/internal/sshx"
)

// Options controls one prune.
type Options struct {
	// FloorGB, when above zero, makes PlanFor return nothing while free space is at or above it.
	FloorGB int
	// WorkDirs are searched for build output beside a project file.
	WorkDirs []string
	// WorktreeDirs are searched for git worktrees.
	WorktreeDirs []string
	// StoppedAgeDays is how long a container must have been stopped before it is planned.
	StoppedAgeDays int
	// KeepImages is how many of the newest images per repository are kept.
	KeepImages int
	// Now is the clock, replaceable in tests.
	Now func() time.Time
}

func (o Options) withDefaults() Options {
	if o.StoppedAgeDays <= 0 {
		o.StoppedAgeDays = 14
	}
	if o.KeepImages < 0 {
		o.KeepImages = 0
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return o
}

// Item is one thing the plan would delete.
type Item struct {
	Kind   string `json:"kind"`
	Target string `json:"target"`
	Bytes  int64  `json:"bytes"`
	Reason string `json:"reason"`
	// Cmd is the exact remote command Apply runs for this item.
	Cmd string `json:"command"`
}

// Disk is the root filesystem in bytes.
type Disk struct {
	TotalBytes int64 `json:"totalBytes"`
	UsedBytes  int64 `json:"usedBytes"`
	FreeBytes  int64 `json:"freeBytes"`
}

// Plan is what a prune would do. Nothing in it has been done.
type Plan struct {
	Disk       Disk     `json:"disk"`
	Items      []Item   `json:"items"`
	TotalBytes int64    `json:"totalBytes"`
	Notes      []string `json:"notes,omitempty"`
	// AboveFloor is true when --floor was given and free space is at or above it.
	AboveFloor bool `json:"aboveFloor,omitempty"`
}

// Failure is an item that did not delete. It does not stop the others.
type Failure struct {
	Item  Item   `json:"item"`
	Error string `json:"error"`
}

// Result is the outcome of Apply.
type Result struct {
	Before     Disk      `json:"before"`
	After      Disk      `json:"after"`
	FreedBytes int64     `json:"freedBytes"`
	Removed    []Item    `json:"removed"`
	Failed     []Failure `json:"failed,omitempty"`
	// Skipped items failed the host-side re-check before rm ran: nothing was deleted.
	Skipped []Failure `json:"skipped,omitempty"`
}

// ErrLocked means another prune holds the VM's lock.
var ErrLocked = errors.New("another vm prune is already running on this VM: wait for it to finish, then run it again")

// Facts is everything BuildPlan reads. ParseFacts fills it.
type Facts struct {
	Home       string
	HasDocker  bool
	HasGit     bool
	BuildCache int64
	Images     []Image
	Containers []Container
	// Volumes are the unused volumes docker lists, named and anonymous alike. Filtering is a rule.
	Volumes []Volume
	// Dirs are every bin, obj and TestResults found; Projects are the directories holding a project file.
	Dirs      []Dir
	Projects  []string
	Worktrees []Worktree
	Caches    []Dir
	Editors   []Editor
}

// Image is one docker image tag.
type Image struct {
	ID      string
	Created time.Time
	Bytes   int64
	Repo    string
	Tag     string
}

// Ref is repo:tag.
func (i Image) Ref() string { return i.Repo + ":" + i.Tag }

// Container is one container, running or not.
type Container struct {
	Name     string
	State    string
	Finished time.Time
	ImageID  string
	ImageRef string
}

// Volume is a docker volume.
type Volume struct {
	Name  string
	Bytes int64
}

// Dir is a directory and its size.
type Dir struct {
	Path  string
	Bytes int64
}

// Worktree is a git worktree found under a configured folder.
type Worktree struct {
	Path  string
	Main  string
	Dirty bool
	// Unpushed is the commit count not on any remote branch; -1 means it could not be read.
	Unpushed int
	Branch   string
	Bytes    int64
}

// Editor is one editor server version directory.
type Editor struct {
	Path  string
	Bytes int64
	MTime int64
}

var anonVolume = regexp.MustCompile(`^[0-9a-f]{64}$`)

// BuildPlan applies every rule to the facts. It is pure.
func BuildPlan(f Facts, o Options) Plan {
	o = o.withDefaults()
	var p Plan
	if f.HasDocker {
		p.Items = append(p.Items, dockerItems(f, o)...)
	} else {
		p.Notes = append(p.Notes, "docker not available: skipped build cache, images, containers and volumes")
	}
	if len(o.WorkDirs) == 0 {
		p.Notes = append(p.Notes, "no --work-dir given: skipped build output")
	} else {
		p.Items = append(p.Items, buildDirItems(f, o)...)
	}
	switch {
	case len(o.WorktreeDirs) == 0:
		p.Notes = append(p.Notes, "no --worktree-dir given: skipped worktrees")
	case !f.HasGit:
		p.Notes = append(p.Notes, "git not available: skipped worktrees")
	default:
		p.Items = append(p.Items, worktreeItems(f, o)...)
	}
	p.Items = append(p.Items, cacheItems(f)...)
	for _, it := range p.Items {
		p.TotalBytes += it.Bytes
	}
	return p
}

func dockerItems(f Facts, o Options) []Item {
	var items []Item
	if f.BuildCache > 0 {
		items = append(items, Item{Kind: "docker-build-cache", Target: "build cache", Bytes: f.BuildCache,
			Reason: "rebuilt on demand", Cmd: "sudo -n docker builder prune -f"})
	}

	usedIDs, usedRefs := map[string]bool{}, map[string]bool{}
	for _, c := range f.Containers {
		usedIDs[shortID(c.ImageID)] = true
		if c.ImageRef != "" {
			usedRefs[normRef(c.ImageRef)] = true
		}
	}
	used := func(im Image) bool {
		return usedIDs[shortID(im.ID)] || (im.Repo != "<none>" && usedRefs[normRef(im.Ref())])
	}

	cutoff := o.Now().Add(-time.Duration(o.StoppedAgeDays) * 24 * time.Hour)
	for _, c := range f.Containers {
		if (c.State != "exited" && c.State != "dead") || c.Finished.IsZero() || c.Finished.After(cutoff) {
			continue
		}
		name := strings.TrimPrefix(c.Name, "/")
		items = append(items, Item{Kind: "docker-container", Target: name,
			Reason: fmt.Sprintf("stopped since %s (older than %d days)", c.Finished.Format("2006-01-02"), o.StoppedAgeDays),
			Cmd:    "sudo -n docker rm " + sshx.Quote(name)})
	}

	// Dangling images, then rollback images beyond the newest N per repository.
	planned := map[string]bool{}
	byRepo := map[string][]Image{}
	for _, im := range f.Images {
		if im.Repo == "<none>" {
			if used(im) || planned[im.ID] {
				continue
			}
			planned[im.ID] = true
			items = append(items, Item{Kind: "docker-image", Target: im.ID, Bytes: im.Bytes,
				Reason: "dangling image, no tag and no container", Cmd: "sudo -n docker rmi " + sshx.Quote(im.ID)})
			continue
		}
		byRepo[im.Repo] = append(byRepo[im.Repo], im)
	}
	repos := make([]string, 0, len(byRepo))
	for r := range byRepo {
		repos = append(repos, r)
	}
	sort.Strings(repos)
	for _, repo := range repos {
		ims := byRepo[repo]
		sort.SliceStable(ims, func(a, b int) bool { return ims[a].Created.After(ims[b].Created) })
		for i, im := range ims {
			if i < o.KeepImages || used(im) {
				continue
			}
			items = append(items, Item{Kind: "docker-image", Target: im.Ref(), Bytes: im.Bytes,
				Reason: fmt.Sprintf("older than the newest %d of %s", o.KeepImages, repo),
				Cmd:    "sudo -n docker rmi " + sshx.Quote(im.Ref())})
		}
	}

	for _, v := range f.Volumes {
		// Only an anonymous volume (a 64-hex name) is ever planned. A named volume is data
		// somebody chose to keep, even when nothing uses it right now.
		if !anonVolume.MatchString(v.Name) {
			continue
		}
		items = append(items, Item{Kind: "docker-volume", Target: v.Name, Bytes: v.Bytes,
			Reason: "anonymous volume not used by any container", Cmd: "sudo -n docker volume rm " + sshx.Quote(v.Name)})
	}
	return items
}

func buildDirItems(f Facts, o Options) []Item {
	proj := map[string]bool{}
	for _, d := range f.Projects {
		// A forged PROJ line outside every work dir must not bless a bin beside it.
		if underAny(d, o.WorkDirs) {
			proj[d] = true
		}
	}
	var items []Item
	for _, d := range f.Dirs {
		base := path.Base(d.Path)
		if base != "bin" && base != "obj" && base != "TestResults" {
			continue
		}
		// The path must be clean, strictly inside a work dir (never the work dir itself), and a
		// bin/ with no project beside it is where repos keep scripts.
		if !underAny(d.Path, o.WorkDirs) || !proj[path.Dir(d.Path)] {
			continue
		}
		items = append(items, Item{Kind: "build-output", Target: d.Path, Bytes: d.Bytes,
			Reason: base + " beside a project file",
			Cmd:    deleteDirCmd(d.Path, o.WorkDirs)})
	}
	return items
}

// underAny reports whether p is a clean absolute path strictly inside one of roots. A path equal
// to a root is not under it. Newlines and '|' are refused because they are how a directory name
// forges a line of the discovery output.
func underAny(p string, roots []string) bool {
	if !safeAbs(p) || strings.Contains(p, "|") || path.Clean(p) != p {
		return false
	}
	for _, r := range roots {
		if !safeAbs(r) || strings.ContainsAny(r, "|") {
			continue
		}
		r = path.Clean(r)
		if strings.HasPrefix(p, r+"/") {
			return true
		}
	}
	return false
}

// deleteDirCmd re-checks on the host, at delete time, that p still resolves to itself, is a real
// directory (not a symlink) and is still inside a work dir. Anything else exits 3 with a message
// starting "skipped:" and Apply reports it as skipped. rm runs as the owning user first; sudo is
// used only when that leaves root-owned files behind. --one-file-system stops at a mount.
func deleteDirCmd(p string, roots []string) string {
	var pats []string
	for _, r := range roots {
		if safeAbs(r) {
			pats = append(pats, sshx.Quote(path.Clean(r)+"/")+"*")
		}
	}
	q := sshx.Quote(p)
	return "p=" + q + "; if [ \"$(realpath -e -- \"$p\" 2>/dev/null)\" = \"$p\" ] && [ -d \"$p\" ] && [ ! -L \"$p\" ] && " +
		"case \"$p\" in " + strings.Join(pats, "|") + ") true ;; *) false ;; esac; then " +
		"rm -rf --one-file-system -- \"$p\" 2>/dev/null || sudo -n rm -rf --one-file-system -- \"$p\"; " +
		"else echo 'skipped: path changed or left the work dir since the plan' >&2; exit 3; fi"
}

func worktreeItems(f Facts, o Options) []Item {
	var items []Item
	for _, w := range f.Worktrees {
		if w.Dirty || w.Unpushed != 0 || !underAny(w.Path, o.WorktreeDirs) || !safeAbs(w.Main) || strings.Contains(w.Main, "|") {
			continue
		}
		items = append(items, Item{Kind: "git-worktree", Target: w.Path, Bytes: w.Bytes,
			Reason: fmt.Sprintf("clean, branch %s has no commits off the remote", w.Branch),
			// No --force: git refuses a worktree with changes, a second guard behind the plan rule.
			Cmd: "git -C " + sshx.Quote(w.Main) + " worktree remove " + sshx.Quote(w.Path)})
	}
	return items
}

func inHomeDot(home, p string) bool {
	return home != "" && safeAbs(home) && underAny(p, []string{home}) && strings.HasPrefix(p, home+"/.")
}

// expectedCache is true only for the cache folders DiscoverCmd looks at.
func expectedCache(home, p string) bool {
	switch p {
	case home + "/.npm/_cacache", home + "/.cache/go-build", home + "/.cache/pip":
		return true
	}
	return strings.HasPrefix(p, home+"/.cache/ms-playwright")
}

func cacheItems(f Facts) []Item {
	var items []Item
	for _, c := range f.Caches {
		if !inHomeDot(f.Home, c.Path) || !expectedCache(f.Home, c.Path) {
			continue
		}
		items = append(items, Item{Kind: "user-cache", Target: c.Path, Bytes: c.Bytes,
			Reason: "cache, rebuilt on demand", Cmd: "rm -rf -- " + sshx.Quote(c.Path)})
	}
	byBase := map[string][]Editor{}
	for _, e := range f.Editors {
		byBase[path.Dir(e.Path)] = append(byBase[path.Dir(e.Path)], e)
	}
	bases := make([]string, 0, len(byBase))
	for b := range byBase {
		bases = append(bases, b)
	}
	sort.Strings(bases)
	for _, b := range bases {
		es := byBase[b]
		sort.SliceStable(es, func(i, j int) bool { return es[i].MTime > es[j].MTime })
		for _, e := range es[1:] {
			if !inHomeDot(f.Home, e.Path) || !expectedEditor(f.Home, e.Path) {
				continue
			}
			items = append(items, Item{Kind: "editor-server", Target: e.Path, Bytes: e.Bytes,
				Reason: "old editor server version, the newest is kept", Cmd: "rm -rf -- " + sshx.Quote(e.Path)})
		}
	}
	return items
}

func expectedEditor(home, p string) bool {
	d := path.Dir(p)
	return d == home+"/.vscode-server/bin" || d == home+"/.cursor-server/bin"
}

// safeAbs refuses anything an rm should never be pointed at.
func safeAbs(p string) bool {
	if !strings.HasPrefix(p, "/") || p == "/" || strings.Contains(p, "\n") {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return false
		}
	}
	return strings.Count(strings.TrimRight(p, "/"), "/") >= 2
}

func shortID(id string) string {
	id = strings.TrimPrefix(id, "sha256:")
	if len(id) > 12 {
		id = id[:12]
	}
	return id
}

func normRef(ref string) string {
	last := ref[strings.LastIndex(ref, "/")+1:]
	if !strings.Contains(last, ":") && !strings.Contains(ref, "@") {
		return ref + ":latest"
	}
	return ref
}

// DiskCmd reads the root filesystem. Read-only.
func DiskCmd() string { return `df -Pk / | awk 'NR==2{print "DF|"$2"|"$3"|"$4}'` }

func quoteAll(ds []string) string {
	q := make([]string, len(ds))
	for i, d := range ds {
		q[i] = sshx.Quote(d)
	}
	return strings.Join(q, " ")
}

// DiscoverCmd builds the read-only script that gathers facts. It deletes nothing.
func DiscoverCmd(o Options) string {
	var b strings.Builder
	b.WriteString(`set -u
echo "HOME|$HOME"
if command -v docker >/dev/null 2>&1 && sudo -n docker info >/dev/null 2>&1; then
  echo "DOCKER|1"
  sudo -n docker system df --format 'DFDOCKER|{{.Type}}|{{.Reclaimable}}'
  sudo -n docker image ls --format 'IMG|{{.ID}}|{{.CreatedAt}}|{{.Size}}|{{.Repository}}:{{.Tag}}'
  ids=$(sudo -n docker ps -aq)
  if [ -n "$ids" ]; then
    sudo -n docker inspect --format 'CTR|{{.Name}}|{{.State.Status}}|{{.State.FinishedAt}}|{{.Image}}|{{.Config.Image}}' $ids
  fi
  sudo -n docker volume ls -qf dangling=true | while read -r v; do
    mp=$(sudo -n docker volume inspect --format '{{.Mountpoint}}' "$v")
    kb=$(sudo -n du -sk "$mp" 2>/dev/null | cut -f1)
    echo "VOL|${kb:-0}|$v"
  done
else
  echo "DOCKER|0"
fi
`)
	if len(o.WorkDirs) > 0 {
		b.WriteString(`for root in ` + quoteAll(o.WorkDirs) + `; do
  [ -d "$root" ] || continue
  nl=$(printf '\nx'); nl=${nl%x}
  find "$root" \( -name node_modules -o -name .git \) -prune -o \( -type d \( -name bin -o -name obj -o -name TestResults \) -prune -print0 \) -o \( -type f \( -name '*.csproj' -o -name '*.fsproj' \) -print0 \) 2>/dev/null | while IFS= read -r -d '' p; do
    case "$p" in
      *'|'*|*"$nl"*) continue ;;
    esac
    case "$p" in
      *.csproj|*.fsproj) echo "PROJ|$(dirname "$p")" ;;
      *) kb=$(sudo -n du -sk "$p" 2>/dev/null || du -sk "$p" 2>/dev/null); echo "DIR|$(echo "$kb" | cut -f1)|$p" ;;
    esac
  done
done
`)
	}
	if len(o.WorktreeDirs) > 0 {
		b.WriteString(`if command -v git >/dev/null 2>&1; then
  echo "GIT|1"
  for root in ` + quoteAll(o.WorktreeDirs) + `; do
    [ -d "$root" ] || continue
    find "$root" -maxdepth 4 -name .git -type f 2>/dev/null | while read -r g; do
      d=$(dirname "$g")
      common=$(cd "$d" && cd "$(git rev-parse --git-common-dir 2>/dev/null)" 2>/dev/null && pwd) || continue
      [ "$(basename "$common")" = ".git" ] || continue
      main=$(dirname "$common")
      # Fail closed: a status that errors counts as dirty.
      if st=$(git -C "$d" status --porcelain 2>/dev/null); then
        if [ -z "$st" ]; then dirty=0; else dirty=1; fi
      else
        dirty=1
      fi
      br=$(git -C "$d" rev-parse --abbrev-ref HEAD 2>/dev/null)
      off=$(git -C "$d" rev-list --count HEAD --not --remotes 2>/dev/null) || off=-1
      kb=$(du -sk "$d" 2>/dev/null | cut -f1)
      echo "WT|$dirty|${off:--1}|${kb:-0}|$main|$br|$d"
    done
  done
else
  echo "GIT|0"
fi
`)
	}
	b.WriteString(`for p in "$HOME/.npm/_cacache" "$HOME/.cache/go-build" "$HOME/.cache/pip" "$HOME"/.cache/ms-playwright*; do
  [ -d "$p" ] && echo "CACHE|$(du -sk "$p" 2>/dev/null | cut -f1)|$p"
done
for base in "$HOME/.vscode-server/bin" "$HOME/.cursor-server/bin"; do
  [ -d "$base" ] || continue
  for p in "$base"/*; do
    [ -d "$p" ] && echo "EDITOR|$(stat -c %Y "$p" 2>/dev/null)|$(du -sk "$p" 2>/dev/null | cut -f1)|$p"
  done
done
exit 0
`)
	return b.String()
}

// ParseFacts reads DiscoverCmd's output. A line it cannot read is skipped, never guessed at, and
// an unreadable worktree stays unsafe (Unpushed -1, Dirty true).
func ParseFacts(out string) Facts {
	var f Facts
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		kind, rest, ok := strings.Cut(line, "|")
		if !ok {
			continue
		}
		switch kind {
		case "HOME":
			f.Home = rest
		case "DOCKER":
			f.HasDocker = rest == "1"
		case "GIT":
			f.HasGit = rest == "1"
		case "DFDOCKER":
			if t, r, ok := strings.Cut(rest, "|"); ok && t == "Build Cache" {
				f.BuildCache = ParseSize(r)
			}
		case "IMG":
			p := strings.SplitN(rest, "|", 4)
			if len(p) != 4 {
				continue
			}
			i := strings.LastIndex(p[3], ":")
			if i < 0 {
				continue
			}
			f.Images = append(f.Images, Image{ID: p[0], Created: parseImageTime(p[1]), Bytes: ParseSize(p[2]), Repo: p[3][:i], Tag: p[3][i+1:]})
		case "CTR":
			p := strings.SplitN(rest, "|", 5)
			if len(p) != 5 {
				continue
			}
			fin, _ := time.Parse(time.RFC3339Nano, p[2])
			if fin.Year() < 2000 {
				fin = time.Time{}
			}
			f.Containers = append(f.Containers, Container{Name: p[0], State: p[1], Finished: fin, ImageID: p[3], ImageRef: p[4]})
		case "VOL":
			if kb, name, ok := strings.Cut(rest, "|"); ok {
				f.Volumes = append(f.Volumes, Volume{Name: name, Bytes: kbToBytes(kb)})
			}
		case "PROJ":
			f.Projects = append(f.Projects, rest)
		case "DIR":
			if kb, p, ok := strings.Cut(rest, "|"); ok {
				f.Dirs = append(f.Dirs, Dir{Path: p, Bytes: kbToBytes(kb)})
			}
		case "CACHE":
			if kb, p, ok := strings.Cut(rest, "|"); ok {
				f.Caches = append(f.Caches, Dir{Path: p, Bytes: kbToBytes(kb)})
			}
		case "EDITOR":
			p := strings.SplitN(rest, "|", 3)
			if len(p) != 3 {
				continue
			}
			mt, _ := strconv.ParseInt(p[0], 10, 64)
			f.Editors = append(f.Editors, Editor{MTime: mt, Bytes: kbToBytes(p[1]), Path: p[2]})
		case "WT":
			p := strings.SplitN(rest, "|", 6)
			if len(p) != 6 {
				continue
			}
			off, err := strconv.Atoi(p[1])
			if err != nil {
				off = -1
			}
			f.Worktrees = append(f.Worktrees, Worktree{Dirty: p[0] != "0", Unpushed: off, Bytes: kbToBytes(p[2]),
				Main: p[3], Branch: p[4], Path: p[5]})
		}
	}
	return f
}

func kbToBytes(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n * 1024
}

func parseImageTime(s string) time.Time {
	f := strings.Fields(s)
	if len(f) < 3 {
		return time.Time{}
	}
	t, _ := time.Parse("2006-01-02 15:04:05 -0700", strings.Join(f[:3], " "))
	return t
}

// ParseSize reads docker's human sizes ("16.6GB (100%)", "500MB", "12.3kB", "0B") as bytes.
func ParseSize(s string) int64 {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, ' '); i >= 0 {
		s = s[:i]
	}
	units := []struct {
		suffix string
		mult   float64
	}{{"TB", 1e12}, {"GB", 1e9}, {"MB", 1e6}, {"kB", 1e3}, {"KB", 1e3}, {"B", 1}}
	for _, u := range units {
		if strings.HasSuffix(s, u.suffix) {
			v, err := strconv.ParseFloat(strings.TrimSuffix(s, u.suffix), 64)
			if err != nil || v < 0 {
				return 0
			}
			return int64(v * u.mult)
		}
	}
	return 0
}

// ParseDisk reads DiskCmd's output.
func ParseDisk(out string) (Disk, error) {
	for _, line := range strings.Split(out, "\n") {
		p := strings.Split(strings.TrimSpace(line), "|")
		if len(p) == 4 && p[0] == "DF" {
			t, e1 := strconv.ParseInt(p[1], 10, 64)
			u, e2 := strconv.ParseInt(p[2], 10, 64)
			a, e3 := strconv.ParseInt(p[3], 10, 64)
			if e1 == nil && e2 == nil && e3 == nil {
				return Disk{TotalBytes: t * 1024, UsedBytes: u * 1024, FreeBytes: a * 1024}, nil
			}
		}
	}
	return Disk{}, errors.New("could not read free disk space from df: check the VM and run vm prune again")
}

// PlanFor reads the disk, applies the floor, gathers facts and builds the plan. It deletes nothing.
func PlanFor(r sshx.Runner, o Options) (Plan, error) {
	o = o.withDefaults()
	out, err := r.Run(DiskCmd())
	if err != nil {
		return Plan{}, err
	}
	disk, err := ParseDisk(out)
	if err != nil {
		return Plan{}, err
	}
	if o.FloorGB > 0 && disk.FreeBytes >= int64(o.FloorGB)*(1<<30) {
		return Plan{Disk: disk, AboveFloor: true,
			Notes: []string{fmt.Sprintf("free space is above the %d GB floor: nothing to do", o.FloorGB)}}, nil
	}
	out, err = r.Run(DiscoverCmd(o))
	if err != nil {
		return Plan{}, err
	}
	p := BuildPlan(ParseFacts(out), o)
	p.Disk = disk
	return p, nil
}

// ApplyCmd wraps the plan in one script under flock. Exit 75 means the lock was busy.
func ApplyCmd(items []Item) string {
	var b strings.Builder
	for i, it := range items {
		fmt.Fprintf(&b, "if err=$(%s 2>&1); then echo 'OK|%d'; else echo \"FAIL|%d|$(echo \"$err\" | head -n1)\"; fi\n", it.Cmd, i, i)
	}
	b.WriteString("echo DONE\n")
	return `flock -n -E 75 "$HOME/.baryovm-prune.lock" sh -c ` + sshx.Quote(b.String()) +
		`; rc=$?; if [ $rc -eq 75 ]; then echo LOCKED; elif [ $rc -eq 127 ]; then echo NOFLOCK; fi; exit 0`
}

// Apply runs the plan and reports the freed space. A second apply while one holds the lock returns
// ErrLocked and changes nothing.
func Apply(r sshx.Runner, p Plan) (Result, error) {
	res := Result{Before: p.Disk, After: p.Disk}
	if len(p.Items) == 0 {
		return res, nil
	}
	out, err := r.Run(ApplyCmd(p.Items))
	if err != nil {
		return res, err
	}
	done := false
	ok := map[int]bool{}
	fail := map[int]string{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "LOCKED":
			return res, ErrLocked
		case line == "NOFLOCK":
			return res, errors.New("flock is not installed on this VM: install util-linux, prune will not run without a lock")
		case line == "DONE":
			done = true
		case strings.HasPrefix(line, "OK|"):
			if i, err := strconv.Atoi(line[3:]); err == nil {
				ok[i] = true
			}
		case strings.HasPrefix(line, "FAIL|"):
			parts := strings.SplitN(line, "|", 3)
			if len(parts) == 3 {
				if i, err := strconv.Atoi(parts[1]); err == nil {
					fail[i] = parts[2]
				}
			}
		}
	}
	if !done {
		return res, errors.New("the prune script did not finish: check the VM and run vm prune again")
	}
	for i, it := range p.Items {
		if ok[i] {
			res.Removed = append(res.Removed, it)
			continue
		}
		msg, has := fail[i]
		if !has {
			msg = "no result reported"
		}
		if strings.HasPrefix(msg, "skipped:") {
			res.Skipped = append(res.Skipped, Failure{Item: it, Error: msg})
			continue
		}
		res.Failed = append(res.Failed, Failure{Item: it, Error: msg})
	}
	dfOut, err := r.Run(DiskCmd())
	if err != nil {
		return res, err
	}
	if res.After, err = ParseDisk(dfOut); err != nil {
		return res, err
	}
	res.FreedBytes = res.After.FreeBytes - res.Before.FreeBytes
	if res.FreedBytes < 0 {
		res.FreedBytes = 0
	}
	return res, nil
}
