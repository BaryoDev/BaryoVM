package envfile

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// sh runs a generated remote command on this machine with stdin, standing in for the VM. The
// scripts are plain POSIX sh and awk, so running them here is the real test of replace, append and
// unset, not a model of them.
func sh(t *testing.T, cmd, stdin string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the remote scripts are POSIX sh")
	}
	c := exec.Command("sh", "-c", cmd)
	c.Stdin = strings.NewReader(stdin)
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("command failed: %v\n%s\ncmd: %s", err, out, cmd)
	}
	return string(out)
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestValidKey(t *testing.T) {
	good := []string{"A", "_A", "DB_PASSWORD", "a1_b2"}
	bad := []string{"", "1A", "A-B", "A B", "A=B", "A;rm", "A\nB", "$X", "A.B", "a'b"}
	for _, k := range good {
		if err := ValidKey(k); err != nil {
			t.Errorf("ValidKey(%q) = %v, want nil", k, err)
		}
	}
	for _, k := range bad {
		if err := ValidKey(k); err == nil {
			t.Errorf("ValidKey(%q) accepted", k)
		}
	}
}

func TestValidValueRefusesWhatCannotBeOneLine(t *testing.T) {
	if err := ValidValue("a b=c\"d'e$f"); err != nil {
		t.Errorf("plain value refused: %v", err)
	}
	for _, v := range []string{"a\nb", "a\x00b"} {
		if err := ValidValue(v); err == nil {
			t.Errorf("ValidValue(%q) accepted", v)
		}
	}
}

func TestPath(t *testing.T) {
	cases := []struct{ dir, file, want string }{
		{"/opt/app", "", "/opt/app/.env"},
		{"/opt/app", ".env", "/opt/app/.env"},
		{"/opt/app/", "deploy/.env", "/opt/app/deploy/.env"},
		{"/opt/app", "/etc/app.env", "/etc/app.env"},
	}
	for _, c := range cases {
		if got := Path(c.dir, c.file); got != c.want {
			t.Errorf("Path(%q,%q) = %q, want %q", c.dir, c.file, got, c.want)
		}
	}
}

func TestSetAppendsReplacesAndKeepsMode600(t *testing.T) {
	f := filepath.Join(t.TempDir(), "stack", "sub", ".env")

	// The file and its directory do not exist yet.
	sh(t, SetCmd(f, "ONE", false), "1\n")
	if got := read(t, f); got != "ONE=1\n" {
		t.Fatalf("after create: %q", got)
	}
	sh(t, SetCmd(f, "TWO", false), "2\n")
	sh(t, SetCmd(f, "ONE", false), "uno\n")
	if got := read(t, f); got != "ONE=uno\nTWO=2\n" {
		t.Fatalf("after replace: %q", got)
	}
	st, err := os.Stat(f)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 600", st.Mode().Perm())
	}
}

func TestSetTightensAnExistingFileToOwnerOnly(t *testing.T) {
	f := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(f, []byte("A=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sh(t, SetCmd(f, "B", false), "2\n")
	st, _ := os.Stat(f)
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 600", st.Mode().Perm())
	}
}

func TestSetDoesNotTouchAKeyThatOnlySharesAPrefix(t *testing.T) {
	f := filepath.Join(t.TempDir(), ".env")
	os.WriteFile(f, []byte("DB=x\nDB_PASSWORD=y\n# DB=z\n"), 0o600)
	sh(t, SetCmd(f, "DB", false), "new\n")
	if got := read(t, f); got != "DB=new\nDB_PASSWORD=y\n# DB=z\n" {
		t.Fatalf("got %q", got)
	}
}

func TestSetKeepsAnUnterminatedLastLine(t *testing.T) {
	f := filepath.Join(t.TempDir(), ".env")
	os.WriteFile(f, []byte("A=1"), 0o600)
	sh(t, SetCmd(f, "B", false), "2\n")
	if got := read(t, f); got != "A=1\nB=2\n" {
		t.Fatalf("got %q", got)
	}
}

func TestSetStoresAwkwardValuesVerbatim(t *testing.T) {
	values := []string{
		"a=b=c",
		`he said "hi"`,
		`it's`,
		"price $HOME ${X} $(id) `id`",
		"  spaces  inside  ",
		`back\slash \n \t`,
		"&amp; & \\1",
		"",
	}
	for _, v := range values {
		f := filepath.Join(t.TempDir(), ".env")
		os.WriteFile(f, []byte("K=old\nOTHER=keep\n"), 0o600)
		sh(t, SetCmd(f, "K", false), v+"\n")
		want := "K=" + v + "\nOTHER=keep\n"
		if got := read(t, f); got != want {
			t.Errorf("replace %q: got %q, want %q", v, got, want)
		}
		f2 := filepath.Join(t.TempDir(), ".env")
		sh(t, SetCmd(f2, "K", false), v+"\n")
		if got := read(t, f2); got != "K="+v+"\n" {
			t.Errorf("append %q: got %q", v, got)
		}
	}
}

func TestUnsetRemovesOnlyThatKey(t *testing.T) {
	f := filepath.Join(t.TempDir(), ".env")
	os.WriteFile(f, []byte("A=1\nAB=2\nB=3\n"), 0o600)
	sh(t, UnsetCmd(f, "A", false), "")
	if got := read(t, f); got != "AB=2\nB=3\n" {
		t.Fatalf("got %q", got)
	}
	st, _ := os.Stat(f)
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 600", st.Mode().Perm())
	}
}

func TestUnsetOnAMissingFileIsNotAnError(t *testing.T) {
	f := filepath.Join(t.TempDir(), "nope", ".env")
	sh(t, UnsetCmd(f, "A", false), "")
	if _, err := os.Stat(f); err == nil {
		t.Error("unset created the file")
	}
}

func TestListPrintsKeysAndNeverValues(t *testing.T) {
	f := filepath.Join(t.TempDir(), ".env")
	os.WriteFile(f, []byte("# comment\nA=secret-one\n\nB_2=a=b\nbad key=x\nexport C=3\n"), 0o600)
	out := sh(t, ListCmd(f, false), "")
	if strings.Contains(out, "secret-one") || strings.Contains(out, "a=b") {
		t.Fatalf("list leaked a value: %q", out)
	}
	keys := parseKeys(out)
	if strings.Join(keys, ",") != "A,B_2" {
		t.Fatalf("keys = %v", keys)
	}
}

// fake is a remote that records what it was asked and what was piped to it.
type fake struct {
	cmds   []string
	stdins []string
	out    string
}

func (f *fake) Run(cmd string) (string, error) { f.cmds = append(f.cmds, cmd); return f.out, nil }
func (f *fake) RunInput(cmd, stdin string) (string, error) {
	f.cmds = append(f.cmds, cmd)
	f.stdins = append(f.stdins, stdin)
	return f.out, nil
}

// The golden for #147: the value reaches the VM on stdin, and no part of the command string, with or
// without sudo, carries it.
func TestSetSendsTheValueOnStdinAndNeverInTheCommand(t *testing.T) {
	const value = "s3cr3t-VALUE=with 'quotes' and $dollar"
	for _, sudo := range []bool{false, true} {
		f := &fake{}
		if err := Set(f, "/opt/app/.env", "TOKEN", value, sudo); err != nil {
			t.Fatal(err)
		}
		if len(f.cmds) != 1 || len(f.stdins) != 1 {
			t.Fatalf("calls: %v %v", f.cmds, f.stdins)
		}
		if strings.Contains(f.cmds[0], "s3cr3t") || strings.Contains(f.cmds[0], "dollar") {
			t.Errorf("sudo=%v: command carries the value: %s", sudo, f.cmds[0])
		}
		if f.stdins[0] != value+"\n" {
			t.Errorf("stdin = %q", f.stdins[0])
		}
		if sudo != strings.HasPrefix(f.cmds[0], "sudo -n ") {
			t.Errorf("sudo=%v but command is %s", sudo, f.cmds[0])
		}
		if !strings.Contains(f.cmds[0], "/opt/app/.env") || !strings.Contains(f.cmds[0], "TOKEN") {
			t.Errorf("path or key missing: %s", f.cmds[0])
		}
	}
}

func TestSetRefusesBeforeAnyRemoteCall(t *testing.T) {
	f := &fake{}
	if err := Set(f, "/x/.env", "bad key", "v", false); err == nil {
		t.Error("bad key accepted")
	}
	if err := Set(f, "/x/.env", "K", "a\nb", false); err == nil {
		t.Error("multi-line value accepted")
	}
	if err := Unset(f, "/x/.env", "1K", false); err == nil {
		t.Error("bad key accepted by unset")
	}
	if len(f.cmds) != 0 {
		t.Errorf("remote was called: %v", f.cmds)
	}
}

func TestListParsesKeysFromTheRemote(t *testing.T) {
	f := &fake{out: "A\nB\n\n"}
	keys, err := List(f, "/x/.env", true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(keys, ",") != "A,B" {
		t.Errorf("keys = %v", keys)
	}
	if !strings.HasPrefix(f.cmds[0], "sudo -n ") {
		t.Errorf("sudo missing: %s", f.cmds[0])
	}
}
