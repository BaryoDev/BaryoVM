// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package release

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/sshx"
)

// Transport is how a static release's files reach the host.
type Transport string

const (
	// TransportRsync runs the operator's local rsync. It is the default when it exists, because
	// its delta transfer is worth keeping.
	TransportRsync Transport = "rsync"
	// TransportTar writes a tar archive with Go and streams it into `tar -x` over the SSH
	// session. It needs no rsync on either end.
	TransportTar Transport = "tar"
)

// PickTransport chooses rsync when lookPath finds it and tar otherwise. lookPath is passed in so
// the choice is testable without touching PATH.
func PickTransport(lookPath func(string) (string, error)) Transport {
	if _, err := lookPath("rsync"); err == nil {
		return TransportRsync
	}
	return TransportTar
}

// insideRoot reports whether dest is root or a path below it. Compared on a "/" boundary, so
// /opt/app2 is not inside /opt/app.
func insideRoot(dest, root string) bool {
	if dest == "" || dest == "." || !path.IsAbs(dest) {
		return false
	}
	return dest == root || strings.HasPrefix(dest, root+"/")
}

// tarDest validates one sync entry and returns where it lands on the host.
func (m *Manifest) tarDest(sub string) (root, dest string, err error) {
	cleanSub := path.Clean(filepath.ToSlash(sub))
	if path.IsAbs(cleanSub) || cleanSub == ".." || strings.HasPrefix(cleanSub, "../") {
		return "", "", fmt.Errorf("sync entry %q points outside localRoot", sub)
	}
	root = path.Clean(m.RemoteRoot)
	if !path.IsAbs(root) || root == "/" {
		return "", "", fmt.Errorf("remoteRoot %q must be an absolute directory other than /", m.RemoteRoot)
	}
	dest = path.Clean(m.SyncDest(sub))
	if !insideRoot(dest, root) {
		return "", "", fmt.Errorf("sync entry %q lands on %q, outside remoteRoot %s", sub, dest, root)
	}
	return root, dest, nil
}

// TarSync copies one sync entry to the host as a tar stream, then removes what is on the host
// and no longer local, giving the result rsync -az --delete gives.
//
// Order matters: the archive is extracted first and the host is pruned only after the extract
// exited cleanly, so a transfer that dies midway never leaves a half-pruned directory. The one
// thing removed before the extract is a host path whose type differs from the local one (a
// directory where a file now is), because tar cannot extract over it.
//
// Pruning is confined to the entry's destination directory, and it keeps what rsync --delete
// keeps: excluded paths, and any directory that holds one.
//
// Known window: between listing the host and removing a path, a user on the host who can write
// inside the release dir could swap a parent directory for a symlink. Each removal re-resolves
// the path's parent on the host right before rm and skips it unless it is still inside the
// release dir, which narrows that window to the instant between the check and the rm. It cannot
// be closed with POSIX tools. Do not release into a directory other users can write to.
func (m *Manifest) TarSync(c sshx.StdinRunner, sub string) error {
	root, dest, err := m.tarDest(sub)
	if err != nil {
		return err
	}
	cleanSub := path.Clean(filepath.ToSlash(sub))
	prefix := ""
	if !strings.HasSuffix(sub, "/") && cleanSub != "." {
		prefix = path.Base(cleanSub)
	}
	mt, err := newMatcher(m.Exclude, prefix)
	if err != nil {
		return err
	}
	src := filepath.Join(expand(m.LocalRoot), filepath.FromSlash(cleanSub))
	lfi, err := os.Lstat(src)
	if err != nil {
		return err
	}
	fi, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		if lfi.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("sync entry %q is a symlink to a file, which the tar transport does not send; sync the file itself", sub)
		}
		// A single file lands at remoteRoot/<name>: extract into remoteRoot and prune nothing.
		if mt.excluded(path.Base(cleanSub), false) {
			return nil
		}
		_, err := streamArchive(c, m.extractCmd(root, root), func(w io.Writer) ([]string, error) {
			return nil, writeTarFile(w, src)
		})
		if err != nil {
			return fmt.Errorf("extract on host: %w", err)
		}
		return nil
	}

	local, err := scanLocal(src, mt)
	if err != nil {
		return err
	}
	host, err := m.listHost(c, root, dest)
	if err != nil {
		return err
	}
	collide, stale := plan(host, local, mt)

	if len(collide) > 0 {
		if err := m.removeOnHost(c, root, dest, collide); err != nil {
			return err
		}
	}
	if _, err := streamArchive(c, m.extractCmd(root, dest), func(w io.Writer) ([]string, error) {
		return writeTar(w, src, mt)
	}); err != nil {
		return fmt.Errorf("extract on host: %w", err)
	}
	if len(stale) > 0 {
		return m.removeOnHost(c, root, dest, stale)
	}
	return nil
}

// guard refuses to go on when dest, or a directory between root and dest, is a symlink on the
// host. Following one would extract and prune somewhere else.
func guard(root, dest string) string {
	if dest == root {
		return ""
	}
	var paths []string
	cur := root
	for _, part := range strings.Split(strings.TrimPrefix(dest, root+"/"), "/") {
		cur += "/" + part
		paths = append(paths, sshx.Quote(cur))
	}
	return `for p in ` + strings.Join(paths, " ") +
		`; do if [ -L "$p" ]; then echo "refusing: $p is a symlink" >&2; exit 1; fi; done; `
}

// The remote command builders are separate from the logic above so another host OS can have its
// own set (tar.exe on Windows) without touching it.

// extractCmd makes the destination and unpacks stdin into it. -p keeps the modes the archive
// carries, and --no-same-owner leaves ownership to whoever runs it (the SSH user, or root under
// Sudo) rather than trusting numeric ids from the operator's machine.
func (m *Manifest) extractCmd(root, dest string) string {
	q := sshx.Quote(dest)
	return sshx.SudoShell(m.Sudo, guard(root, dest)+"mkdir -p "+q+" && tar -xpf - --no-same-owner -C "+q)
}

// listCmd prints every path under dest, NUL separated and relative to it, each preceded by its
// type: "d" for a directory, "f" for anything else. Nothing is pruned from the listing, because
// which paths are excluded is decided in Go, with the same matcher the archive uses.
func (m *Manifest) listCmd(root, dest string) string {
	q := sshx.Quote(dest)
	find := `find . -mindepth 1 \( -type d -exec printf 'd\000%s\000' {} + \) -o \( -exec printf 'f\000%s\000' {} + \)`
	return sshx.SudoShell(m.Sudo, guard(root, dest)+"mkdir -p "+q+" && cd "+q+" && "+find)
}

// removeScript is run by xargs with a batch of paths. It resolves each path's parent on the host
// immediately before removing the path, and skips any whose parent is not inside the release dir
// (the working directory), so a parent swapped for a symlink since the listing is not followed.
const removeScript = `D=$(pwd -P) || exit 1; rc=0; for p in "$@"; do parent=${p%/*}; ` +
	`if r=$(cd "$parent" 2>/dev/null && pwd -P) && case "$r" in "$D"|"$D"/*) true;; *) false;; esac; ` +
	`then rm -rf -- "$p"; else echo "skipped $p: its parent is not inside the release dir" >&2; rc=1; fi; done; exit $rc`

func (m *Manifest) removeCmd(root, dest string) string {
	return sshx.SudoShell(m.Sudo, guard(root, dest)+"cd "+sshx.Quote(dest)+" && xargs -0 sh -c "+sshx.Quote(removeScript)+" sh")
}

type hostEntry struct {
	rel string // "./a/b"
	dir bool
}

// listHost returns what the host has under dest. Every path is checked to be a plain relative
// name before anything can be done with it.
func (m *Manifest) listHost(c sshx.StdinRunner, root, dest string) ([]hostEntry, error) {
	out, err := c.Run(m.listCmd(root, dest))
	if err != nil {
		return nil, fmt.Errorf("list host files: %w", err)
	}
	toks := strings.Split(out, "\x00")
	if n := len(toks); n > 0 && toks[n-1] == "" {
		toks = toks[:n-1]
	}
	if len(toks)%2 != 0 {
		return nil, fmt.Errorf("host listing is malformed: nothing was removed")
	}
	var entries []hostEntry
	for i := 0; i < len(toks); i += 2 {
		kind, p := toks[i], toks[i+1]
		if (kind != "d" && kind != "f") || !safeRel(p) {
			return nil, fmt.Errorf("host listed %q, which is not a path inside %s: nothing was removed", p, dest)
		}
		entries = append(entries, hostEntry{rel: p, dir: kind == "d"})
	}
	return entries, nil
}

// plan decides what to delete. collide are host paths whose type differs from the local one.
// stale are host paths with no local counterpart. Neither includes an excluded path or a
// directory that holds one, and neither lists a path below another it already lists.
func plan(host []hostEntry, local map[string]bool, mt *matcher) (collide, stale []string) {
	protected := map[string]bool{}
	for _, e := range host {
		rel := strings.TrimPrefix(e.rel, "./")
		if !mt.excluded(rel, e.dir) {
			continue
		}
		parts := strings.Split(rel, "/")
		for i := range parts {
			protected[strings.Join(parts[:i+1], "/")] = true
		}
	}
	var col, st []string
	for _, e := range host {
		rel := strings.TrimPrefix(e.rel, "./")
		if protected[rel] {
			continue
		}
		isDir, ok := local[rel]
		switch {
		case !ok:
			st = append(st, e.rel)
		case isDir != e.dir:
			col = append(col, e.rel)
		}
	}
	collide = topmost(col)
	for _, p := range topmost(st) {
		if !below(p, collide) {
			stale = append(stale, p)
		}
	}
	return collide, stale
}

func below(p string, dirs []string) bool {
	for _, d := range dirs {
		if strings.HasPrefix(p, d+"/") {
			return true
		}
	}
	return false
}

// topmost drops every path that sits below another path in the list, since removing a directory
// removes what is in it.
func topmost(paths []string) []string {
	sort.Strings(paths)
	var top []string
	for _, p := range paths {
		if n := len(top); n > 0 && strings.HasPrefix(p, top[n-1]+"/") {
			continue
		}
		top = append(top, p)
	}
	return top
}

func (m *Manifest) removeOnHost(c sshx.StdinRunner, root, dest string, paths []string) error {
	var list bytes.Buffer
	for _, p := range paths {
		list.WriteString(p)
		list.WriteByte(0)
	}
	if _, err := c.RunStdin(m.removeCmd(root, dest), &list); err != nil {
		return fmt.Errorf("remove stale files on host: %w", err)
	}
	return nil
}

// safeRel reports whether p is "./" followed by plain components, the only shape that is ever
// passed to rm. No "..", no absolute path, no empty component.
func safeRel(p string) bool {
	if !strings.HasPrefix(p, "./") || len(p) == 2 {
		return false
	}
	for _, part := range strings.Split(p[2:], "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

// streamArchive runs cmd on the host with the archive piped into its stdin as write produces it.
// Nothing larger than the copy buffers is ever held. If the local writer fails, its error is
// returned with the host's, because the host only ever sees a truncated archive.
func streamArchive(c sshx.StdinRunner, cmd string, write func(io.Writer) ([]string, error)) ([]string, error) {
	pr, pw := io.Pipe()
	type result struct {
		keep []string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		keep, err := write(pw)
		pw.CloseWithError(err)
		done <- result{keep, err}
	}()
	_, runErr := c.RunStdin(cmd, pr)
	if runErr != nil {
		_ = pr.CloseWithError(runErr)
		res := <-done
		if res.err != nil && !errors.Is(res.err, runErr) {
			return nil, fmt.Errorf("writing archive: %w (host said: %v)", res.err, runErr)
		}
		return nil, runErr
	}
	// The host may exit before reading the trailer. Let the writer finish before judging it.
	_, _ = io.Copy(io.Discard, pr)
	res := <-done
	return res.keep, res.err
}

// walkTree visits everything under src that is not excluded and can travel: directories,
// regular files and symlinks. Symlinks are reported, never followed.
func walkTree(src string, mt *matcher, fn func(rel, p string, info fs.FileInfo, link string) error) error {
	root := src
	if fi, err := os.Lstat(src); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
		resolved, err := filepath.EvalSymlinks(src)
		if err != nil {
			return err
		}
		root = resolved
	}
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		relOS, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel := filepath.ToSlash(relOS)
		if mt.excluded(rel, d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		mode := info.Mode()
		var link string
		switch {
		case mode.IsDir(), mode.IsRegular():
		case mode&fs.ModeSymlink != 0:
			if link, err = os.Readlink(p); err != nil {
				return err
			}
		default:
			return nil
		}
		return fn(rel, p, info, link)
	})
}

// scanLocal maps each path that will be sent, relative and slash separated, to whether it is a
// directory.
func scanLocal(src string, mt *matcher) (map[string]bool, error) {
	out := map[string]bool{}
	err := walkTree(src, mt, func(rel, _ string, info fs.FileInfo, _ string) error {
		out[rel] = info.IsDir()
		return nil
	})
	return out, err
}

// writeTar streams the tree under src to w as a tar archive and returns the paths it wrote,
// as "./<rel>". Names are relative and slash separated. Symlinks go as links and are never
// followed, so nothing is ever written beneath one. Owners are zeroed, and setuid and setgid
// are dropped: the extracting side decides ownership, and a root extract must not turn a local
// file into a setuid-root one.
func writeTar(w io.Writer, src string, mt *matcher) ([]string, error) {
	tw := tar.NewWriter(w)
	var keep []string
	err := walkTree(src, mt, func(rel, p string, info fs.FileInfo, link string) error {
		h, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		h.Name = rel
		if info.IsDir() {
			h.Name += "/"
		}
		scrub(h)
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		keep = append(keep, "./"+rel)
		if info.Mode().IsRegular() {
			return copyFile(tw, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return keep, nil
}

func scrub(h *tar.Header) {
	h.Uid, h.Gid, h.Uname, h.Gname = 0, 0, "", ""
	h.Mode &^= 0o6000
}

func writeTarFile(w io.Writer, file string) error {
	info, err := os.Lstat(file)
	if err != nil {
		return err
	}
	h, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	h.Name = filepath.Base(file)
	scrub(h)
	tw := tar.NewWriter(w)
	if err := tw.WriteHeader(h); err != nil {
		return err
	}
	if err := copyFile(tw, file); err != nil {
		return err
	}
	return tw.Close()
}

func copyFile(w io.Writer, file string) error {
	f, err := os.Open(file) // #nosec G304 -- local file chosen by the user; the tar writer guards what it archives
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}
