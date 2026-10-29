package release

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func mustMatcher(t *testing.T, prefix string, patterns ...string) *matcher {
	t.Helper()
	mt, err := newMatcher(patterns, prefix)
	if err != nil {
		t.Fatal(err)
	}
	return mt
}

func TestExcludeFollowsRsyncRules(t *testing.T) {
	cases := []struct {
		prefix, pattern, rel string
		dir, want            bool
	}{
		{"", ".env", ".env", false, true},
		{"", ".env", "a/b/.env", false, true},
		{"", "secrets/.env", "secrets/.env", false, true},
		{"", "secrets/.env", "a/secrets/.env", false, true},
		{"", "secrets/.env", "a/mysecrets/.env", false, false},
		{"", "/secrets/.env", "a/secrets/.env", false, false},
		{"", "/secrets/.env", "secrets/.env", false, true},
		{"", "**/.env", "a/b/.env", false, true},
		{"", "**/.env", ".env", false, true},
		{"", "**/node_modules", "node_modules", true, true},
		{"", "**/node_modules", "node_modules/p/i.js", false, true},
		{"", "**/*.log", "x.log", false, true},
		{"", "**/secrets", "secrets/.env", false, true},
		{"", "**/build/", "build", true, true},
		{"", "**/build/", "build", false, false},
		{"", "**/x/**", "x/y", false, true},
		{"", "**/*", "anything", false, true},
		{"", "/**/y", "y", false, false},
		{"", "/**/y", "a/y", false, true},
		{"out", "**/.env", ".env", false, true},
		{"", "a/**/c", "a/b/x/c", false, true},
		{"", "*.map", "a/b/app.js.map", false, true},
		{"", "a/*", "a/b/c", false, true},
		{"", "logs/", "logs", true, true},
		{"", "logs/", "logs", false, false},
		{"", "logs/", "x/logs/y.txt", false, true},
		{"", "node_modules", "node_modules/x/y.js", false, true},
		{"out", "/out/secret", "secret", false, true},
		{"out", "/secret", "secret", false, false},
		{"out", "out", "anything", false, true},
		{"", "a?c", "abc", false, true},
		{"", "a?c", "a/c", false, false},
	}
	for _, c := range cases {
		mt := mustMatcher(t, c.prefix, c.pattern)
		if got := mt.excluded(c.rel, c.dir); got != c.want {
			t.Errorf("prefix %q pattern %q path %q dir=%v: got %v, want %v", c.prefix, c.pattern, c.rel, c.dir, got, c.want)
		}
	}
}

func TestUnsupportedExcludeIsRefusedBeforeTheHostIsTouched(t *testing.T) {
	skipWindows(t)
	for _, p := range []string{"[ab].txt", `a\*b`, "***", "!x", "- x", "//build"} {
		m, local, _ := tarManifest(t)
		m.Exclude = []string{p}
		write(t, filepath.Join(local, "out", "a.txt"), "a", 0o644)
		h := &localHost{}
		if err := m.TarSync(h, "out/"); err == nil {
			t.Errorf("pattern %q was accepted", p)
		}
		if len(h.seen) != 0 {
			t.Errorf("pattern %q reached the host", p)
		}
	}
}

func TestExcludedFilesNeverShip(t *testing.T) {
	skipWindows(t)
	m, local, remote := tarManifest(t)
	m.Exclude = []string{"secrets/.env", "**/.key", "/rootonly", "cache/"}
	for _, f := range []string{"secrets/.env", "a/secrets/.env", "a/b/.key", "rootonly", "a/rootonly", "cache/x", "a/cache/x", "ok.txt"} {
		write(t, filepath.Join(local, "out", f), "x", 0o644)
	}
	if err := m.TarSync(&localHost{}, "out/"); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"secrets/.env", "a/secrets/.env", "a/b/.key", "rootonly", "cache/x", "a/cache/x"} {
		if _, err := os.Lstat(filepath.Join(remote, f)); err == nil {
			t.Errorf("excluded file shipped: %s", f)
		}
	}
	for _, f := range []string{"ok.txt", "a/rootonly"} {
		if _, err := os.Lstat(filepath.Join(remote, f)); err != nil {
			t.Errorf("file was wrongly excluded: %s", f)
		}
	}
}

// rsync --delete keeps excluded files, and the directories that hold them.
func TestPruneKeepsExcludedContentAndItsDirectories(t *testing.T) {
	skipWindows(t)
	m, local, remote := tarManifest(t)
	m.Exclude = []string{".git"}
	write(t, filepath.Join(local, "out", "a.txt"), "a", 0o644)
	write(t, filepath.Join(remote, "old", ".git", "HEAD"), "h", 0o644)
	write(t, filepath.Join(remote, "old", "gone.txt"), "g", 0o644)
	write(t, filepath.Join(remote, "stale", "x.txt"), "s", 0o644)
	if err := m.TarSync(&localHost{}, "out/"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(remote, "old", ".git", "HEAD")); err != nil {
		t.Errorf("excluded content was deleted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(remote, "old", "gone.txt")); err == nil {
		t.Error("stale file beside excluded content survived")
	}
	if _, err := os.Stat(filepath.Join(remote, "stale")); err == nil {
		t.Error("fully stale directory survived")
	}
}

func TestLeadingDoubleStarCoversTheTopLevel(t *testing.T) {
	skipWindows(t)
	m, local, remote := tarManifest(t)
	m.Exclude = []string{"**/.env", "**/node_modules", "**/*.log", "**/build/"}
	for _, f := range []string{".env", "node_modules/p.js", "x.log", "build/o", "a/.env", "ok.txt"} {
		write(t, filepath.Join(local, "out", f), "new", 0o644)
	}
	for _, f := range []string{".env", "node_modules/keep.js", "x.log", "build/old", "stale.txt"} {
		write(t, filepath.Join(remote, f), "host", 0o644)
	}
	if err := m.TarSync(&localHost{}, "out/"); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"a/.env", "node_modules/p.js", "build/o"} {
		if _, err := os.Lstat(filepath.Join(remote, f)); err == nil {
			t.Errorf("excluded file shipped: %s", f)
		}
	}
	for _, f := range []string{".env", "node_modules/keep.js", "x.log", "build/old"} {
		b, err := os.ReadFile(filepath.Join(remote, f))
		if err != nil || string(b) != "host" {
			t.Errorf("host file %s was pruned or overwritten: %q %v", f, b, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(remote, "stale.txt")); err == nil {
		t.Error("stale file survived")
	}
	if _, err := os.Lstat(filepath.Join(remote, "ok.txt")); err != nil {
		t.Errorf("ok.txt not shipped: %v", err)
	}
}

func TestTarSyncRefusesASymlinkedDestination(t *testing.T) {
	skipWindows(t)
	m, local, remote := tarManifest(t)
	outside := filepath.Join(hostJail, "outside")
	write(t, filepath.Join(outside, "precious"), "p", 0o644)
	if err := os.Symlink(outside, filepath.Join(remote, "out")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(local, "out", "a.txt"), "a", 0o644)
	err := m.TarSync(&localHost{}, "out")
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("want a symlink refusal, got %v", err)
	}
	if _, e := os.Stat(filepath.Join(outside, "precious")); e != nil {
		t.Errorf("pruned through the symlink: %v", e)
	}
	if _, e := os.Stat(filepath.Join(outside, "a.txt")); e == nil {
		t.Error("extracted through the symlink")
	}
}

// A stale path whose parent was swapped for a symlink after the listing must be skipped.
func TestRemoveSkipsAParentThatIsNowASymlink(t *testing.T) {
	skipWindows(t)
	m, local, remote := tarManifest(t)
	outside := filepath.Join(hostJail, "outside")
	write(t, filepath.Join(outside, "victim"), "v", 0o644)
	if err := os.Symlink(outside, filepath.Join(remote, "swapped")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(local, "out", "a.txt"), "a", 0o644)
	listing := "f\x00./swapped/victim\x00"
	h := &localHost{listOverride: &listing}
	err := m.TarSync(h, "out/")
	if err == nil || !strings.Contains(err.Error(), "skipped") {
		t.Fatalf("want the swapped path reported as skipped, got %v", err)
	}
	if _, e := os.Stat(filepath.Join(outside, "victim")); e != nil {
		t.Errorf("removed a file through a swapped parent: %v", e)
	}
}

func TestTypeChangeIsHandled(t *testing.T) {
	skipWindows(t)
	m, local, remote := tarManifest(t)
	write(t, filepath.Join(local, "out", "x"), "file now", 0o644)
	write(t, filepath.Join(local, "out", "y", "z"), "dir now", 0o644)
	write(t, filepath.Join(remote, "x", "inner.txt"), "i", 0o644)
	write(t, filepath.Join(remote, "y"), "was a file", 0o644)
	h := &localHost{}
	if err := m.TarSync(h, "out/"); err != nil {
		t.Fatal(err)
	}
	// bsdtar happens to replace a directory with a file, GNU tar does not, so check the order of
	// the commands rather than trusting the local tar: the colliding paths go before the extract.
	rm, extract := -1, -1
	for i, c := range h.seen {
		if rm < 0 && strings.Contains(c, "rm -rf --") {
			rm = i
		}
		if extract < 0 && strings.Contains(c, "tar -x") {
			extract = i
		}
	}
	if rm < 0 || rm > extract {
		t.Errorf("colliding host paths were not removed before the extract: %v", h.seen)
	}
	if b, _ := os.ReadFile(filepath.Join(remote, "x")); string(b) != "file now" {
		t.Errorf("x is %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(remote, "y", "z")); string(b) != "dir now" {
		t.Errorf("y/z is %q", b)
	}
	if err := m.TarSync(h, "out/"); err != nil {
		t.Errorf("second release fails: %v", err)
	}
}

func TestSetuidAndSetgidAreDropped(t *testing.T) {
	skipWindows(t)
	src := t.TempDir()
	write(t, filepath.Join(src, "tool"), "x", 0o755)
	if err := os.Chmod(filepath.Join(src, "tool"), 0o755|os.ModeSetuid|os.ModeSetgid); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := writeTar(&buf, src, nil); err != nil {
		t.Fatal(err)
	}
	h := tarNames(t, buf.Bytes())["tool"]
	if h.Mode&0o6000 != 0 {
		t.Errorf("setuid or setgid reached the archive: %o", h.Mode)
	}
	if h.Mode&0o777 != 0o755 {
		t.Errorf("permission bits changed: %o", h.Mode)
	}
}

func TestOwnersAreZeroed(t *testing.T) {
	skipWindows(t)
	src := t.TempDir()
	write(t, filepath.Join(src, "f"), "x", 0o644)
	if st, ok := mustStat(t, filepath.Join(src, "f")).Sys().(*syscall.Stat_t); !ok || st.Uid == 0 {
		t.Skip("needs a non-root owner to prove anything")
	}
	var buf bytes.Buffer
	if _, err := writeTar(&buf, src, nil); err != nil {
		t.Fatal(err)
	}
	h := tarNames(t, buf.Bytes())["f"]
	if h.Uid != 0 || h.Gid != 0 || h.Uname != "" || h.Gname != "" {
		t.Errorf("owner leaked into the archive: %+v", h)
	}
}

func mustStat(t *testing.T, p string) os.FileInfo {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi
}

func TestSymlinkSyncEntryIsRejected(t *testing.T) {
	skipWindows(t)
	m, local, _ := tarManifest(t)
	write(t, filepath.Join(local, "real.txt"), "r", 0o644)
	if err := os.Symlink("real.txt", filepath.Join(local, "link.txt")); err != nil {
		t.Fatal(err)
	}
	h := &localHost{}
	err := m.TarSync(h, "link.txt")
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("want a clear symlink error, got %v", err)
	}
	if len(h.seen) != 0 {
		t.Error("host was touched")
	}
}

func TestLocalWriterErrorIsReported(t *testing.T) {
	skipWindows(t)
	if os.Geteuid() == 0 {
		t.Skip("root can read anything")
	}
	m, local, _ := tarManifest(t)
	write(t, filepath.Join(local, "out", "locked"), "x", 0o000)
	err := m.TarSync(&localHost{}, "out/")
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("the local cause is lost: %v", err)
	}
}

func TestSingleFileLandsInRemoteRootAndPrunesNothing(t *testing.T) {
	skipWindows(t)
	m, local, remote := tarManifest(t)
	write(t, filepath.Join(local, "robots.txt"), "r", 0o644)
	write(t, filepath.Join(remote, "keep.txt"), "k", 0o644)
	h := &localHost{}
	if err := m.TarSync(h, "robots.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(remote, "robots.txt")); err != nil {
		t.Errorf("file not at remoteRoot: %v", err)
	}
	if _, err := os.Stat(filepath.Join(remote, "keep.txt")); err != nil {
		t.Errorf("a single file sync pruned: %v", err)
	}
	for _, c := range h.seen {
		if strings.Contains(c, "rm -rf") {
			t.Errorf("single file sync ran a delete: %s", c)
		}
	}
}

func TestInsideRoot(t *testing.T) {
	cases := []struct {
		dest, root string
		want       bool
	}{
		{"/opt/app", "/opt/app", true},
		{"/opt/app/out", "/opt/app", true},
		{"/opt/app2", "/opt/app", false},
		{"/opt", "/opt/app", false},
		{"", "/opt/app", false},
		{".", "/opt/app", false},
		{"opt/app", "/opt/app", false},
	}
	for _, c := range cases {
		if got := insideRoot(c.dest, c.root); got != c.want {
			t.Errorf("insideRoot(%q, %q) = %v", c.dest, c.root, got)
		}
	}
}

func TestTarDestRefusesEmptyAndDotDestinations(t *testing.T) {
	for _, root := range []string{"", ".", "/", "relative"} {
		m := &Manifest{RemoteRoot: root}
		if _, _, err := m.tarDest("out/"); err == nil {
			t.Errorf("remoteRoot %q was accepted", root)
		}
	}
	m := &Manifest{RemoteRoot: "/opt/app"}
	if _, d, err := m.tarDest("."); err != nil || d != "/opt/app" {
		t.Errorf("sub . : %q %v", d, err)
	}
}

// Only the topmost stale path is sent, and removal is --, so a name like -rf is a name.
func TestRemoveListsOnlyTopmostPathsAndUsesDoubleDash(t *testing.T) {
	skipWindows(t)
	local := t.TempDir()
	write(t, filepath.Join(local, "out", "a.txt"), "a", 0o644)
	m := &Manifest{LocalRoot: local, RemoteRoot: "/srv/site", Sync: []string{"out/"}}
	rec := &recorder{list: "d\x00./d\x00f\x00./d/x\x00d\x00./d/y\x00f\x00./d/y/z\x00f\x00./-rf\x00f\x00./a.txt\x00"}
	if err := m.TarSync(rec, "out/"); err != nil {
		t.Fatal(err)
	}
	last := len(rec.cmds) - 1
	if !strings.Contains(rec.cmds[last], "rm -rf --") {
		t.Errorf("rm without --: %s", rec.cmds[last])
	}
	if got, want := rec.stdins[last], "./-rf\x00./d\x00"; got != want {
		t.Errorf("remove list = %q, want %q", got, want)
	}
}
