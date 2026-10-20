// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package release

import (
	"archive/tar"
	"bytes"
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

// TarSync copies one sync entry to the host as a tar stream, then removes what is on the host
// and no longer local, giving the result rsync -az --delete gives.
//
// Order matters: the archive is extracted first and the host is pruned only after the extract
// exited cleanly, so a transfer that dies midway never leaves a half-pruned directory.
//
// Pruning is confined to the entry's destination directory. The destination must sit inside
// remoteRoot, remoteRoot must be absolute and not "/", and every path the host reports is checked
// to be a plain relative name before it is handed back to be deleted.
func (m *Manifest) TarSync(c sshx.StdinRunner, sub string) error {
	cleanSub := path.Clean(filepath.ToSlash(sub))
	if path.IsAbs(cleanSub) || cleanSub == ".." || strings.HasPrefix(cleanSub, "../") {
		return fmt.Errorf("sync entry %q points outside localRoot", sub)
	}
	root := path.Clean(m.RemoteRoot)
	if !path.IsAbs(root) || root == "/" {
		return fmt.Errorf("remoteRoot %q must be an absolute directory other than /", m.RemoteRoot)
	}
	src := filepath.Join(expand(m.LocalRoot), filepath.FromSlash(cleanSub))
	fi, err := os.Stat(src)
	if err != nil {
		return err
	}

	// A single file lands at remoteRoot/<name>, so extract into remoteRoot and prune nothing.
	if !fi.IsDir() {
		return m.sendFile(c, root, src)
	}

	dest := path.Clean(m.SyncDest(sub))
	if dest != root && !strings.HasPrefix(dest, root+"/") {
		return fmt.Errorf("sync entry %q lands on %s, outside remoteRoot %s", sub, dest, root)
	}

	keep, err := m.streamTar(c, dest, src)
	if err != nil {
		return fmt.Errorf("extract on host: %w", err)
	}
	return m.prune(c, dest, keep)
}

func (m *Manifest) sendFile(c sshx.StdinRunner, root, src string) error {
	_, err := streamArchive(c, m.extractCmd(root), func(w io.Writer) ([]string, error) {
		return nil, writeTarFile(w, src)
	})
	if err != nil {
		return fmt.Errorf("extract on host: %w", err)
	}
	return nil
}

func (m *Manifest) streamTar(c sshx.StdinRunner, dest, src string) ([]string, error) {
	return streamArchive(c, m.extractCmd(dest), func(w io.Writer) ([]string, error) {
		return writeTar(w, src, m.Exclude)
	})
}

// extractCmd makes the destination and unpacks stdin into it. -p keeps the modes the archive
// carries, and --no-same-owner leaves ownership to whoever runs it (the SSH user, or root under
// Sudo) rather than trusting numeric ids from the operator's machine.
func (m *Manifest) extractCmd(dest string) string {
	q := sshx.Quote(dest)
	return sshx.SudoShell(m.Sudo, "mkdir -p "+q+" && tar -xpf - --no-same-owner -C "+q)
}

// listCmd prints every path under dest, NUL separated and relative to it, skipping what the
// manifest excludes (rsync --delete leaves excluded paths alone).
func (m *Manifest) listCmd(dest string) string {
	var prune []string
	for _, e := range m.Exclude {
		e = strings.Trim(e, "/")
		if e == "" {
			continue
		}
		if strings.Contains(e, "/") {
			prune = append(prune, "-path "+sshx.Quote("./"+e))
		} else {
			prune = append(prune, "-name "+sshx.Quote(e))
		}
	}
	find := "find . -mindepth 1"
	if len(prune) > 0 {
		find += " \\( " + strings.Join(prune, " -o ") + " \\) -prune -o"
	}
	find += " -print0"
	return sshx.SudoShell(m.Sudo, "cd "+sshx.Quote(dest)+" && "+find)
}

func (m *Manifest) removeCmd(dest string) string {
	return sshx.SudoShell(m.Sudo, "cd "+sshx.Quote(dest)+" && xargs -0 rm -rf --")
}

// prune deletes what the host has under dest and the local tree does not.
func (m *Manifest) prune(c sshx.StdinRunner, dest string, keep []string) error {
	out, err := c.Run(m.listCmd(dest))
	if err != nil {
		return fmt.Errorf("list host files: %w", err)
	}
	keepSet := make(map[string]bool, len(keep))
	for _, k := range keep {
		keepSet[k] = true
	}
	var stale []string
	for _, p := range strings.Split(out, "\x00") {
		if p == "" {
			continue
		}
		if !safeRel(p) {
			return fmt.Errorf("host listed %q, which is not a path inside %s: nothing was removed", p, dest)
		}
		if !keepSet[p] {
			stale = append(stale, p)
		}
	}
	if len(stale) == 0 {
		return nil
	}
	// Deleting a directory takes its children, so only the topmost stale paths are sent.
	sort.Strings(stale)
	var top []string
	for _, p := range stale {
		if n := len(top); n > 0 && strings.HasPrefix(p, top[n-1]+"/") {
			continue
		}
		top = append(top, p)
	}
	var list bytes.Buffer
	for _, p := range top {
		list.WriteString(p)
		list.WriteByte(0)
	}
	if _, err := c.RunStdin(m.removeCmd(dest), &list); err != nil {
		return fmt.Errorf("remove stale files on host: %w", err)
	}
	return nil
}

// safeRel reports whether p is "./" followed by plain components, the only shape prune will
// pass to rm. No "..", no absolute path, no empty component.
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
// Nothing larger than the copy buffers is ever held.
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
		pr.CloseWithError(runErr)
		<-done
		return nil, runErr
	}
	// The host may exit before reading the trailer. Let the writer finish before judging it.
	_, _ = io.Copy(io.Discard, pr)
	res := <-done
	return res.keep, res.err
}

// excluded applies rsync's name rules to one path: a pattern without a slash matches any
// component name, one with a slash matches the path from the sync root.
func excluded(rel string, patterns []string) bool {
	base := path.Base(rel)
	for _, p := range patterns {
		p = strings.Trim(p, "/")
		if p == "" {
			continue
		}
		target := base
		if strings.Contains(p, "/") {
			target = rel
		}
		if ok, _ := path.Match(p, target); ok {
			return true
		}
	}
	return false
}

// writeTar streams the tree under src to w as a tar archive and returns the paths it wrote,
// as "./<rel>", for the prune step. Names are relative and slash separated. Symlinks go as
// links and are never followed, so nothing is ever written beneath one. Owners are zeroed:
// the extracting side decides ownership.
func writeTar(w io.Writer, src string, exclude []string) ([]string, error) {
	root := src
	if fi, err := os.Lstat(src); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
		resolved, err := filepath.EvalSymlinks(src)
		if err != nil {
			return nil, err
		}
		root = resolved
	}
	tw := tar.NewWriter(w)
	var keep []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
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
		if excluded(rel, exclude) {
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
		h, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		h.Name = rel
		if mode.IsDir() {
			h.Name += "/"
		}
		h.Uid, h.Gid, h.Uname, h.Gname = 0, 0, "", ""
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		keep = append(keep, "./"+rel)
		if mode.IsRegular() {
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
	h.Uid, h.Gid, h.Uname, h.Gname = 0, 0, "", ""
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
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}
