package release

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// localHost plays the VM: every command runs in a local sh, so the real tar, find and xargs
// do the extracting and pruning. seen records each command, in order.
type localHost struct {
	seen []string
	// failTar makes the extract exit non-zero after reading its input.
	failTar bool
	// listOverride replaces the output of the remote file listing.
	listOverride *string
}

func (h *localHost) Run(cmd string) (string, error) { return h.RunStdin(cmd, nil) }

func (h *localHost) RunStdin(cmd string, in io.Reader) (string, error) {
	h.seen = append(h.seen, cmd)
	if h.failTar && strings.Contains(cmd, "tar -x") {
		if in != nil {
			_, _ = io.Copy(io.Discard, in)
		}
		return "", errors.New("tar: boom")
	}
	if h.listOverride != nil && strings.Contains(cmd, "find ") {
		return *h.listOverride, nil
	}
	c := exec.Command("sh", "-c", cmd)
	c.Stdin = in
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	if err := c.Run(); err != nil {
		return out.String(), fmt.Errorf("%w: %s", err, errb.String())
	}
	return out.String(), nil
}

func skipWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs sh, tar and find")
	}
}

func write(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// listTree returns every path under root, relative, with symlink targets, sorted.
func listTree(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if rel == "." {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			tgt, _ := os.Readlink(p)
			rel += " -> " + tgt
		}
		out = append(out, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

func tarNames(t *testing.T, b []byte) map[string]*tar.Header {
	t.Helper()
	got := map[string]*tar.Header{}
	r := tar.NewReader(bytes.NewReader(b))
	for {
		h, err := r.Next()
		if err == io.EOF {
			return got
		}
		if err != nil {
			t.Fatal(err)
		}
		got[h.Name] = h
	}
}

func TestPickTransportPrefersRsyncWhenPresent(t *testing.T) {
	found := func(string) (string, error) { return "/usr/bin/rsync", nil }
	missing := func(string) (string, error) { return "", exec.ErrNotFound }
	if got := PickTransport(found); got != TransportRsync {
		t.Errorf("rsync on PATH: got %q", got)
	}
	if got := PickTransport(missing); got != TransportTar {
		t.Errorf("rsync missing: got %q", got)
	}
}

func TestWriteTarNamesAreRelativeAndNeverEscape(t *testing.T) {
	skipWindows(t)
	src := t.TempDir()
	outside := t.TempDir()
	write(t, filepath.Join(src, "a", "b.txt"), "b", 0o644)
	write(t, filepath.Join(outside, "secret"), "s", 0o644)
	// A link out of the tree and a link to a directory outside. Neither may be followed.
	if err := os.Symlink(outside, filepath.Join(src, "out")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(src, "abs")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../up", filepath.Join(src, "a", "up")); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if _, err := writeTar(&buf, src, nil); err != nil {
		t.Fatal(err)
	}
	got := tarNames(t, buf.Bytes())
	for name := range got {
		if strings.HasPrefix(name, "/") || name == ".." || strings.HasPrefix(name, "../") ||
			strings.Contains(name, "/../") || strings.HasSuffix(name, "/..") {
			t.Errorf("entry escapes the destination: %q", name)
		}
		if strings.HasPrefix(name, "out/") || name == "secret" {
			t.Errorf("walked through a symlink: %q", name)
		}
	}
	for _, want := range []string{"a/", "a/b.txt", "out", "abs", "a/up"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing entry %q, have %v", want, keysOf(got))
		}
	}
	if h := got["out"]; h == nil || h.Typeflag != tar.TypeSymlink || h.Linkname != outside {
		t.Errorf("symlink should travel as a link: %+v", h)
	}
}

func keysOf(m map[string]*tar.Header) []string {
	var k []string
	for n := range m {
		k = append(k, n)
	}
	sort.Strings(k)
	return k
}

func TestWriteTarSkipsExcludesAndKeepsModes(t *testing.T) {
	skipWindows(t)
	src := t.TempDir()
	write(t, filepath.Join(src, "run.sh"), "#!/bin/sh", 0o755)
	write(t, filepath.Join(src, "keep.txt"), "k", 0o640)
	write(t, filepath.Join(src, "node_modules", "x.js"), "x", 0o644)
	write(t, filepath.Join(src, "deep", "bin", "y"), "y", 0o644)

	var buf bytes.Buffer
	keep, err := writeTar(&buf, src, []string{"node_modules", "bin"})
	if err != nil {
		t.Fatal(err)
	}
	got := tarNames(t, buf.Bytes())
	for n := range got {
		if strings.Contains(n, "node_modules") || strings.Contains(n, "bin") {
			t.Errorf("excluded path travelled: %q", n)
		}
	}
	if got["run.sh"].Mode&0o777 != 0o755 || got["keep.txt"].Mode&0o777 != 0o640 {
		t.Errorf("modes lost: %o %o", got["run.sh"].Mode, got["keep.txt"].Mode)
	}
	if !contains(keep, "./keep.txt") || !contains(keep, "./deep") {
		t.Errorf("keep list is incomplete: %v", keep)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Streaming: a large file goes through a fixed buffer, so total allocation stays far below
// the size of the data. Building the archive in memory would allocate at least 48 MB here.
func TestWriteTarDoesNotBufferTheArchive(t *testing.T) {
	skipWindows(t)
	src := t.TempDir()
	f, err := os.Create(filepath.Join(src, "big.bin"))
	if err != nil {
		t.Fatal(err)
	}
	const size = 48 << 20
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	f.Close()

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	cw := &countWriter{}
	if _, err := writeTar(cw, src, nil); err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)
	if cw.n < size {
		t.Fatalf("archive is %d bytes, want at least %d", cw.n, size)
	}
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > size/4 {
		t.Errorf("allocated %d bytes writing a %d byte file; the archive is being buffered", alloc, size)
	}
}

type countWriter struct{ n int64 }

func (c *countWriter) Write(p []byte) (int, error) { c.n += int64(len(p)); return len(p), nil }

func tarManifest(t *testing.T) (*Manifest, string, string) {
	t.Helper()
	local, remote := t.TempDir(), filepath.Join(t.TempDir(), "www")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	return &Manifest{LocalRoot: local, RemoteRoot: remote, Sync: []string{"out/"}}, local, remote
}

func TestTarSyncCopiesTheTree(t *testing.T) {
	skipWindows(t)
	m, local, remote := tarManifest(t)
	write(t, filepath.Join(local, "out", "index.html"), "hi", 0o644)
	write(t, filepath.Join(local, "out", "run.sh"), "x", 0o750)
	write(t, filepath.Join(local, "out", "my file's \"name\".txt"), "q", 0o644)
	write(t, filepath.Join(local, "out", "sub dir", "$HOME;x.txt"), "d", 0o600)
	if err := os.Symlink("index.html", filepath.Join(local, "out", "link")); err != nil {
		t.Fatal(err)
	}

	h := &localHost{}
	if err := m.TarSync(h, "out/"); err != nil {
		t.Fatalf("%v\n%s", err, strings.Join(h.seen, "\n"))
	}
	want := listTree(t, filepath.Join(local, "out"))
	if got := listTree(t, remote); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("tree differs\nwant %v\n got %v", want, got)
	}
	if fi, _ := os.Stat(filepath.Join(remote, "run.sh")); fi == nil || fi.Mode().Perm() != 0o750 {
		t.Errorf("mode lost: %v", fi)
	}
	if fi, _ := os.Stat(filepath.Join(remote, "sub dir", "$HOME;x.txt")); fi == nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("odd name or mode lost: %v", fi)
	}
}

func TestTarSyncRemovesFilesDeletedLocally(t *testing.T) {
	skipWindows(t)
	m, local, remote := tarManifest(t)
	write(t, filepath.Join(local, "out", "a.txt"), "a", 0o644)
	write(t, filepath.Join(local, "out", "gone", "b.txt"), "b", 0o644)
	write(t, filepath.Join(local, "out", "my file's.txt"), "c", 0o644)
	h := &localHost{}
	if err := m.TarSync(h, "out/"); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(local, "out", "gone")); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(local, "out", "a.txt"))
	// Present on the host, absent locally, with an awkward name.
	write(t, filepath.Join(remote, "-rf stale 'x'.txt"), "s", 0o644)

	if err := m.TarSync(h, "out/"); err != nil {
		t.Fatal(err)
	}
	want := listTree(t, filepath.Join(local, "out"))
	if got := listTree(t, remote); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("host does not match local\nwant %v\n got %v", want, got)
	}
}

func TestTarSyncLeavesExcludedPathsOnTheHost(t *testing.T) {
	skipWindows(t)
	m, local, remote := tarManifest(t)
	m.Exclude = []string{"node_modules"}
	write(t, filepath.Join(local, "out", "a.txt"), "a", 0o644)
	write(t, filepath.Join(remote, "node_modules", "x.js"), "x", 0o644)
	if err := m.TarSync(&localHost{}, "out/"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(remote, "node_modules", "x.js")); err != nil {
		t.Errorf("rsync --delete leaves excluded paths alone, and so must this: %v", err)
	}
}

func TestTarSyncWithoutTrailingSlashLandsInASubdirAndPrunesOnlyThere(t *testing.T) {
	skipWindows(t)
	m, local, remote := tarManifest(t)
	write(t, filepath.Join(local, "out", "a.txt"), "a", 0o644)
	write(t, filepath.Join(remote, "other", "keep.txt"), "k", 0o644)
	write(t, filepath.Join(remote, "out", "stale.txt"), "s", 0o644)
	if err := m.TarSync(&localHost{}, "out"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(remote, "out", "a.txt")); err != nil {
		t.Error(err)
	}
	if _, err := os.Stat(filepath.Join(remote, "out", "stale.txt")); err == nil {
		t.Error("stale file under the sync dir survived")
	}
	if _, err := os.Stat(filepath.Join(remote, "other", "keep.txt")); err != nil {
		t.Errorf("pruned outside the sync dir: %v", err)
	}
}

func TestTarSyncDoesNotPruneAfterAFailedExtract(t *testing.T) {
	skipWindows(t)
	m, local, remote := tarManifest(t)
	write(t, filepath.Join(local, "out", "a.txt"), "a", 0o644)
	write(t, filepath.Join(remote, "stale.txt"), "s", 0o644)
	h := &localHost{failTar: true}
	if err := m.TarSync(h, "out/"); err == nil {
		t.Fatal("a failed extract was reported as success")
	}
	if _, err := os.Stat(filepath.Join(remote, "stale.txt")); err != nil {
		t.Errorf("pruned after a failed extract: %v", err)
	}
	for _, c := range h.seen {
		if strings.Contains(c, "rm -rf") {
			t.Errorf("a delete ran after a failed extract: %s", c)
		}
	}
}

func TestTarSyncRefusesAnEscapingSubBeforeTouchingTheHost(t *testing.T) {
	skipWindows(t)
	for _, sub := range []string{"..", "../x", "a/../../x", "/etc", "/", "out/../../.."} {
		m, local, remote := tarManifest(t)
		write(t, filepath.Join(local, "out", "a.txt"), "a", 0o644)
		write(t, filepath.Join(remote, "keep.txt"), "k", 0o644)
		h := &localHost{}
		if err := m.TarSync(h, sub); err == nil {
			t.Errorf("sub %q was accepted", sub)
		}
		if len(h.seen) != 0 {
			t.Errorf("sub %q reached the host: %v", sub, h.seen)
		}
	}
}

func TestTarSyncRefusesToPruneTheFilesystemRoot(t *testing.T) {
	skipWindows(t)
	local := t.TempDir()
	write(t, filepath.Join(local, "out", "a.txt"), "a", 0o644)
	m := &Manifest{LocalRoot: local, RemoteRoot: "/", Sync: []string{"out/"}}
	h := &localHost{}
	if err := m.TarSync(h, "out/"); err == nil || len(h.seen) != 0 {
		t.Fatalf("remoteRoot / was accepted: %v %v", err, h.seen)
	}
	m.RemoteRoot = "relative/path"
	if err := m.TarSync(h, "out/"); err == nil || len(h.seen) != 0 {
		t.Fatalf("a relative remoteRoot was accepted: %v %v", err, h.seen)
	}
}

// A host listing that names something outside the release dir must never turn into a delete.
func TestTarSyncRefusesAHostListingThatEscapes(t *testing.T) {
	skipWindows(t)
	for _, bad := range []string{"./../victim\x00", "/etc/passwd\x00", "./a/../../victim\x00", "../victim\x00"} {
		m, local, remote := tarManifest(t)
		victim := filepath.Join(filepath.Dir(remote), "victim")
		write(t, victim, "v", 0o644)
		write(t, filepath.Join(local, "out", "a.txt"), "a", 0o644)
		listing := bad
		h := &localHost{listOverride: &listing}
		if err := m.TarSync(h, "out/"); err == nil {
			t.Errorf("listing %q accepted", bad)
		}
		if _, err := os.Stat(victim); err != nil {
			t.Errorf("listing %q deleted a file outside the release dir", bad)
		}
		for _, c := range h.seen {
			if strings.Contains(c, "rm -rf") {
				t.Errorf("listing %q still ran a delete: %s", bad, c)
			}
		}
	}
}

// A symlink on the host that points out of the release dir is removed as a link. What it points
// at is not touched, and nothing is written through it.
func TestTarSyncPruningNeverFollowsAHostSymlinkOut(t *testing.T) {
	skipWindows(t)
	m, local, remote := tarManifest(t)
	outside := t.TempDir()
	write(t, filepath.Join(outside, "precious"), "p", 0o644)
	if err := os.Symlink(outside, filepath.Join(remote, "stale-link")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(local, "out", "a.txt"), "a", 0o644)
	if err := m.TarSync(&localHost{}, "out/"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(remote, "stale-link")); err == nil {
		t.Error("stale symlink survived")
	}
	if _, err := os.Stat(filepath.Join(outside, "precious")); err != nil {
		t.Errorf("prune followed the link and deleted its target: %v", err)
	}
}

func TestTarSyncQuotesRemotePathsAndHonoursSudo(t *testing.T) {
	skipWindows(t)
	local := t.TempDir()
	write(t, filepath.Join(local, "out", "a.txt"), "a", 0o644)
	remote := "/srv/my site's root"
	m := &Manifest{LocalRoot: local, RemoteRoot: remote, Sync: []string{"out/"}, Sudo: true}
	rec := &recorder{}
	if err := m.TarSync(rec, "out/"); err != nil {
		t.Fatal(err)
	}
	if len(rec.cmds) == 0 {
		t.Fatal("nothing ran")
	}
	for _, c := range rec.cmds {
		if !strings.HasPrefix(c, "sudo -n ") {
			t.Errorf("sudo release ran a command without root: %s", c)
		}
		if strings.Contains(c, "my site") && strings.Contains(c, "my site's root") {
			t.Errorf("remote path is not quoted: %s", c)
		}
	}
}

type recorder struct{ cmds []string }

func (r *recorder) Run(cmd string) (string, error) { r.cmds = append(r.cmds, cmd); return "", nil }
func (r *recorder) RunStdin(cmd string, in io.Reader) (string, error) {
	r.cmds = append(r.cmds, cmd)
	if in != nil {
		_, _ = io.Copy(io.Discard, in)
	}
	return "", nil
}
