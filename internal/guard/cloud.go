// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package guard

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// CloudProjectPolicyKey is the git config key for the cloud projects a repository may deploy to:
//
//	git config --add baryovm.allowedCloudProjects my-project-id
const CloudProjectPolicyKey = "baryovm.allowedCloudProjects"

// ParseCloudProjects reads project id allowlist values: comma or space separated ids.
func ParseCloudProjects(values []string) []string {
	var out []string
	for _, e := range splitList(values) {
		out = append(out, strings.ToLower(e))
	}
	return out
}

// CloudProjectsRestricted reports whether a project allowlist is set.
func (p Policy) CloudProjectsRestricted() bool { return len(p.AllowedCloudProjects) > 0 }

// AllowsCloudProject reports whether deploying to the project id is allowed. Without an allowlist
// it always is.
func (p Policy) AllowsCloudProject(id string) bool {
	if !p.CloudProjectsRestricted() {
		return true
	}
	id = strings.ToLower(id)
	for _, a := range p.AllowedCloudProjects {
		if a == id {
			return true
		}
	}
	return false
}

// CloudFS is the seam to the file system for the cloud checks. ListFiles returns every file under
// dir as a slash path relative to dir. Exists reports whether a path exists. Getwd is the current
// directory. Tests supply a fake.
type CloudFS interface {
	ListFiles(dir string) ([]string, error)
	Exists(path string) bool
	Getwd() (string, error)
}

// CloudEnv holds the seams a cloud check needs: R for git and reading files, FS for the file
// system, Run for the cloud CLI (wrangler's dry run), and TempDir for a scratch directory the
// caller removes with the returned cleanup.
type CloudEnv struct {
	R       Runner
	FS      CloudFS
	Run     Exec
	TempDir func() (string, func(), error)
}

// leadingFlag reports whether the command line starts with an option. That is refused, because
// the subcommand cannot then be told apart from an option value. Help and version are not refused.
func leadingFlag(args []string) bool {
	if len(args) == 0 || !strings.HasPrefix(args[0], "-") {
		return false
	}
	switch args[0] {
	case "-h", "--help", "-v", "--version", "-V":
		return false
	}
	return true
}

// flagValues reads a flag given as "--name value", "--name=value" or "-n value". It returns the
// values in order, and an error when a value is missing.
func flagValues(args []string, names ...string) (vals []string, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			break
		}
		for _, n := range names {
			if a == n {
				if i+1 >= len(args) {
					return nil, fmt.Errorf("%s has no value", n)
				}
				vals = append(vals, args[i+1])
				i++
				break
			}
			if strings.HasPrefix(n, "--") && strings.HasPrefix(a, n+"=") {
				vals = append(vals, strings.TrimPrefix(a, n+"="))
				break
			}
		}
	}
	return vals, nil
}

// scanFiles reads and scans every file in files under dir, refusing one over the size cap or
// that cannot be read. total carries the bytes read so far across calls.
func scanFiles(r Runner, action, dir string, files []string, total *int64) ([]Finding, Verdict, bool) {
	var fs []Finding
	for _, f := range files {
		p := filepath.Join(dir, filepath.FromSlash(f))
		b, err := r.ReadFile(p)
		if err != nil {
			return nil, refuse(action, fmt.Sprintf("%s refused: cannot read %s to scan it (%v)", action, p, err), nil), false
		}
		*total += int64(len(b))
		if int64(len(b)) > MaxEntryBytes || *total > MaxTotalBytes {
			return nil, refuse(action, fmt.Sprintf("%s refused: %s is over the size the scan reads", action, p), nil), false
		}
		found, why := scanContent(p, b)
		if why != "" {
			return nil, refuse(action, action+" refused: "+why, nil), false
		}
		fs = append(fs, found...)
	}
	return fs, Verdict{}, true
}

// ---- wrangler ----

var wranglerConfigs = []string{"wrangler.toml", "wrangler.json", "wrangler.jsonc"}

// CheckWrangler plans a wrangler command. A deploy (deploy, publish, versions upload) is
// bundled with `--dry-run --outdir`, and the bundle, the wrangler config and the arguments are
// scanned. Anything else passes.
func CheckWrangler(env CloudEnv, args []string) Verdict {
	const action = "cloud wrangler"
	if leadingFlag(args) {
		return refuse(action, action+" refused: put options after the command, as in `baryovm cloud wrangler deploy --env prod`, so the command can be told from an option value", nil)
	}
	if len(args) == 0 {
		return allow(action)
	}
	switch {
	case args[0] == "deploy" || args[0] == "publish":
	case args[0] == "versions" && len(args) > 1 && args[1] == "upload":
	case args[0] == "pages" && len(args) > 1 && (args[1] == "deploy" || args[1] == "publish"):
		return checkWranglerPages(env, args[2:])
	default:
		return allow(action)
	}
	for _, a := range args {
		if a == "--outdir" || strings.HasPrefix(a, "--outdir=") {
			return refuse(action, action+" refused: --outdir is set by baryovm for the bundle check; remove it", nil)
		}
	}
	var fs []Finding
	for i, a := range args {
		for _, rule := range ScanLine(a) {
			fs = append(fs, Finding{File: fmt.Sprintf("argument %d", i+1), Line: 1, Rule: rule})
		}
	}
	if len(fs) > 0 {
		return keyRefusal(action, "the wrangler arguments (a --var value is stored in clear text; use `wrangler secret put NAME`)", fs)
	}

	cfg, err := wranglerConfig(env.R, args)
	if err != nil {
		return refuse(action, action+" refused: "+err.Error(), nil)
	}
	if cfg.path != "" {
		fs = ScanText(cfg.path, cfg.text)
		if len(fs) > 0 {
			return refuse(action, fmt.Sprintf("%s refused: %d suspected key(s) in %s: %s. Values under vars are stored in clear text and shipped with the Worker. Remove the key from the file and store it with `wrangler secret put NAME`",
				action, len(fs), cfg.path, Summarise(fs, 5)), fs)
		}
	}

	if cfg.path == "" && !hasConfigFlag(args) {
		if p, err := parentWranglerConfig(env); err != nil {
			return refuse(action, action+" refused: "+err.Error(), nil)
		} else if p != "" {
			return refuse(action, fmt.Sprintf("%s refused: wrangler would find its config at %s, a parent of this directory (the search goes up to the file system root), and that cannot be scanned from here. Run baryovm from that directory", action, p), nil)
		}
	}

	assetDirs, why := wranglerAssetDirs(args, cfg)
	if why != "" {
		return refuse(action, action+" refused: "+why, nil)
	}
	for _, d := range assetDirs {
		if v, ok := scanDirVerdict(env, action, d, "the assets directory"); !ok {
			return v
		}
	}

	out, cleanup, err := env.TempDir()
	if err != nil {
		return refuse(action, fmt.Sprintf("%s refused: cannot make a scratch directory for the bundle (%v)", action, err), nil)
	}
	defer cleanup()
	// The user's own --dry-run, in any form, is dropped: the bundle run sets it once.
	var dry []string
	for _, a := range args {
		if a != "--dry-run" && !strings.HasPrefix(a, "--dry-run=") {
			dry = append(dry, a)
		}
	}
	dry = append(dry, "--dry-run", "--outdir", out)
	if _, err := env.Run("", "wrangler", dry); err != nil {
		return refuse(action, fmt.Sprintf("%s refused: wrangler %s --dry-run failed (%s), so the bundle cannot be checked", action, args[0], tailLines(err.Error(), 4)), nil)
	}
	files, err := env.FS.ListFiles(out)
	if err != nil || len(files) == 0 {
		return refuse(action, action+" refused: the dry run wrote no bundle, so it cannot be checked", nil)
	}
	var total int64
	found, v, ok := scanFiles(env.R, action, out, files, &total)
	if !ok {
		return v
	}
	if len(found) > 0 {
		return keyRefusal(action, "the Worker bundle", found)
	}
	return allow(action)
}

func checkWranglerPages(env CloudEnv, rest []string) Verdict {
	const action = "cloud wrangler"
	var dir string
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		if strings.HasPrefix(a, "-") {
			if !strings.Contains(a, "=") && wranglerValueFlags[a] {
				i++
			}
			continue
		}
		if dir == "" {
			dir = a
		}
	}
	if dir == "" {
		return refuse(action, action+" refused: pages deploy needs the directory as its first argument, so the files can be checked", nil)
	}
	if v, ok := scanDirVerdict(env, action, dir, "the files pages deploy would upload"); !ok {
		return v
	}
	return allow(action)
}

// scanDirVerdict scans every file under dir. ok is true when the directory is clean.
func scanDirVerdict(env CloudEnv, action, dir, what string) (Verdict, bool) {
	files, err := env.FS.ListFiles(dir)
	if err != nil {
		return refuse(action, fmt.Sprintf("%s refused: cannot list %s (%v)", action, dir, err), nil), false
	}
	var total int64
	found, v, ok := scanFiles(env.R, action, dir, files, &total)
	if !ok {
		return v, false
	}
	if len(found) > 0 {
		return keyRefusal(action, what, found), false
	}
	return Verdict{}, true
}

// A section header or a key named assets, in any spelling: [assets], [env.x.assets], assets = ,
// assets.directory =, "assets": . A name that merely contains the word (my-assets) does not match.
var assetsMention = regexp.MustCompile(`(?m)^\s*\[(?:[^\]]*\.)?assets\]|(?:^|[\s,{."'])assets["']?\s*[=:.]`)
var assetsDirectory = regexp.MustCompile(`["']?\bdirectory["']?\s*[=:]\s*["']([^"']+)["']`)
var assetsDirectoryKey = regexp.MustCompile(`["']?\bdirectory["']?\s*[=:]`)

// wranglerAssetDirs lists the static asset directories a deploy uploads outside the bundle: the
// --assets flag and the assets directory in the config. Assets named in the config that cannot be
// read out of it are a refusal, not a skip.
func wranglerAssetDirs(args []string, cfg wranglerCfg) ([]string, string) {
	dirs, err := flagValues(args, "--assets")
	if err != nil {
		return nil, err.Error()
	}
	site, err := flagValues(args, "--site")
	if err != nil {
		return nil, err.Error()
	}
	dirs = append(dirs, site...)
	if cfg.path == "" {
		return dirs, ""
	}
	base := filepath.Dir(cfg.path)
	// Every directory or bucket key must resolve to a quoted string. One that does not (a variable,
	// a number, a spelling the pattern misses) is a refusal, not a skip.
	if assetsMention.MatchString(cfg.text) {
		m := assetsDirectory.FindAllStringSubmatch(cfg.text, -1)
		if len(m) == 0 || len(m) != len(assetsDirectoryKey.FindAllString(cfg.text, -1)) {
			return nil, "the config sets assets but a directory cannot be read, so the assets cannot be scanned"
		}
		for _, x := range m {
			dirs = append(dirs, resolveDir(base, x[1]))
		}
	}
	if siteMention.MatchString(cfg.text) || siteBucketKey.MatchString(cfg.text) {
		m := siteBucket.FindAllStringSubmatch(cfg.text, -1)
		if len(m) == 0 || len(m) != len(siteBucketKey.FindAllString(cfg.text, -1)) {
			return nil, "the config sets site but a bucket cannot be read, so the site files cannot be scanned"
		}
		for _, x := range m {
			dirs = append(dirs, resolveDir(base, x[1]))
		}
	}
	return dirs, ""
}

// resolveDir reads a directory from a config: relative to the config file, or as given if absolute.
func resolveDir(base, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(base, p)
}

var siteMention = regexp.MustCompile(`(?m)^\s*\[(?:[^\]]*\.)?site\]|(?:^|[\s,{."'])site["']?\s*[=:.]`)
var siteBucket = regexp.MustCompile(`["']?\bbucket["']?\s*[=:]\s*["']([^"']+)["']`)
var siteBucketKey = regexp.MustCompile(`["']?\bbucket["']?\s*[=:]`)

func hasConfigFlag(args []string) bool {
	v, _ := flagValues(args, "--config", "-c")
	return len(v) > 0
}

// parentWranglerConfig looks for a wrangler config above the current directory, all the way to the
// file system root: wrangler does not stop at the git root, so neither does this.
func parentWranglerConfig(env CloudEnv) (string, error) {
	wd, err := env.FS.Getwd()
	if err != nil {
		return "", errors.New("cannot read the current directory, so a parent wrangler config cannot be ruled out")
	}
	for dir := wd; ; {
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		dir = parent
		for _, name := range wranglerConfigs {
			text, err := readOptional(env.R, filepath.Join(dir, name))
			if err != nil {
				return "", fmt.Errorf("cannot read %s to look for a wrangler config", filepath.Join(dir, name))
			}
			if text != "" {
				return filepath.Join(dir, name), nil
			}
		}
	}
}

// tailLines keeps the last n non-empty lines of a tool's error output and withholds any line that
// looks like a key, so a refusal never carries a whole build log.
func tailLines(s string, n int) string {
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, strings.TrimSpace(l))
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	for i, l := range lines {
		if len(ScanLine(l)) > 0 {
			lines[i] = "[line withheld]"
		}
	}
	return strings.Join(lines, " | ")
}

var wranglerValueFlags = map[string]bool{
	"--project-name": true, "--branch": true, "--commit-hash": true, "--commit-message": true,
	"--commit-dirty": true, "--config": true, "-c": true, "--env": true, "-e": true,
}

type wranglerCfg struct{ path, text string }

// wranglerConfig reads the config named by -c/--config, or the first of wrangler.toml,
// wrangler.json and wrangler.jsonc in the current directory. No config is allowed: wrangler can
// deploy from flags alone, and the bundle is scanned anyway.
func wranglerConfig(r Runner, args []string) (wranglerCfg, error) {
	vals, err := flagValues(args, "--config", "-c")
	if err != nil {
		return wranglerCfg{}, err
	}
	if len(vals) > 1 {
		return wranglerCfg{}, errors.New("more than one --config, so the config cannot be checked")
	}
	if len(vals) == 1 {
		b, err := r.ReadFile(vals[0])
		if err != nil {
			return wranglerCfg{}, fmt.Errorf("cannot read the config %s to scan it", vals[0])
		}
		return wranglerCfg{vals[0], string(b)}, nil
	}
	for _, name := range wranglerConfigs {
		text, err := readOptional(r, name)
		if err != nil {
			return wranglerCfg{}, fmt.Errorf("cannot read %s to scan it", name)
		}
		if text != "" {
			return wranglerCfg{name, text}, nil
		}
	}
	return wranglerCfg{}, nil
}

// ---- firebase ----

// FirebaseDeploy is what CheckFirebase learned: the project the deploy would reach.
type FirebaseDeploy struct {
	Verdict Verdict
	Project string
}

type firebaseRC struct {
	Projects map[string]string `json:"projects"`
}

type firebaseTarget struct {
	Public    string          `json:"public"`
	Source    string          `json:"source"`
	Ignore    []string        `json:"ignore"`
	Predeploy json.RawMessage `json:"predeploy"`
}

type firebaseJSON struct {
	Hosting   json.RawMessage `json:"hosting"`
	Functions json.RawMessage `json:"functions"`
}

// CheckFirebase plans a firebase command. A deploy (deploy, hosting:channel:deploy) resolves the
// project from --project or .firebaserc, checks it against the policy, and scans the files in the
// hosting public directories and functions sources, minus the ignore rules in firebase.json.
// Anything else passes.
func CheckFirebase(env CloudEnv, args []string, pol Policy) FirebaseDeploy {
	const action = "cloud firebase"
	fail := func(reason string) FirebaseDeploy {
		return FirebaseDeploy{Verdict: refuse(action, action+" refused: "+reason, nil)}
	}
	if leadingFlag(args) {
		return fail("put options after the command, as in `baryovm cloud firebase deploy --project my-app`, so the command can be told from an option value")
	}
	if len(args) == 0 || (args[0] != "deploy" && args[0] != "hosting:channel:deploy") {
		return FirebaseDeploy{Verdict: allow(action)}
	}
	rest := args[1:]
	rcText, rcErr := readOptional(env.R, ".firebaserc")
	if rcErr != nil {
		return fail("cannot read .firebaserc")
	}
	var rc firebaseRC
	if rcText != "" {
		if err := json.Unmarshal([]byte(rcText), &rc); err != nil {
			return fail(".firebaserc is not valid JSON, so the project cannot be read")
		}
	}
	proj, why := firebaseProject(rest, rc, pol)
	if why != "" {
		return fail(why)
	}
	if !pol.AllowsCloudProject(proj) {
		return FirebaseDeploy{Project: proj, Verdict: refuse(action, fmt.Sprintf("%s refused: project %s is not in %s (allowed: %s)", action, proj, CloudProjectPolicyKey, strings.Join(pol.AllowedCloudProjects, ", ")), nil)}
	}

	cfgName := "firebase.json"
	if vals, err := flagValues(rest, "--config", "-c"); err != nil {
		return fail(err.Error())
	} else if len(vals) == 1 {
		cfgName = vals[0]
	} else if len(vals) > 1 {
		return fail("more than one --config, so the files cannot be checked")
	}
	cfgText, err := readOptional(env.R, cfgName)
	if err != nil || cfgText == "" {
		return fail("cannot read " + cfgName + " in the current directory, so the files cannot be checked")
	}
	var cfg firebaseJSON
	if err := json.Unmarshal([]byte(cfgText), &cfg); err != nil {
		return fail(cfgName + " is not valid JSON, so the files cannot be checked")
	}
	targets, why := firebaseTargets(cfg)
	if why != "" {
		return fail(why)
	}

	var (
		found []Finding
		total int64
	)
	for _, t := range targets {
		files, err := env.FS.ListFiles(t.dir)
		if err != nil {
			return fail(fmt.Sprintf("cannot list %s (%v), so the files cannot be checked", t.dir, err))
		}
		var keep []string
		for _, f := range files {
			if !ignored(f, t.ignore) {
				keep = append(keep, f)
			}
		}
		fs, v, ok := scanFiles(env.R, action, t.dir, keep, &total)
		if !ok {
			return FirebaseDeploy{Project: proj, Verdict: v}
		}
		found = append(found, fs...)
	}
	if len(found) > 0 {
		return FirebaseDeploy{Project: proj, Verdict: keyRefusal(action, "the files firebase would deploy", found)}
	}
	return FirebaseDeploy{Project: proj, Verdict: allow(action)}
}

// firebaseProject resolves the project id: --project (an id or an alias from .firebaserc), then
// the .firebaserc default. With a project policy set, --project is required, because the active
// project that `firebase use` stores outside the repository cannot be read here.
func firebaseProject(rest []string, rc firebaseRC, pol Policy) (string, string) {
	vals, err := flagValues(rest, "--project", "-P")
	if err != nil {
		return "", err.Error()
	}
	if len(vals) > 1 {
		return "", "more than one --project, so the project cannot be told"
	}
	if len(vals) == 1 {
		if id, ok := rc.Projects[vals[0]]; ok {
			return id, ""
		}
		if vals[0] == "" {
			return "", "--project is empty"
		}
		return vals[0], ""
	}
	if pol.CloudProjectsRestricted() {
		return "", "a project policy is set, so pass --project <id>: the active project from `firebase use` cannot be read here"
	}
	if id := rc.Projects["default"]; id != "" {
		return id, ""
	}
	return "", "the project cannot be determined: pass --project <id> or set a default in .firebaserc"
}

type deployDir struct {
	dir    string
	ignore []string
}

// firebaseTargets lists the directories firebase.json deploys. A hosting entry with a source
// (web frameworks) is refused: what it builds cannot be listed here.
func firebaseTargets(cfg firebaseJSON) ([]deployDir, string) {
	var out []deployDir
	hosting, err := rawList(cfg.Hosting)
	if err != nil {
		return nil, "firebase.json hosting is not readable"
	}
	for _, h := range hosting {
		var t firebaseTarget
		if err := json.Unmarshal(h, &t); err != nil {
			return nil, "firebase.json hosting is not readable"
		}
		if hasPredeploy(t.Predeploy) {
			return nil, predeployWhy("hosting")
		}
		if t.Source != "" {
			return nil, "hosting uses a framework source, which builds files that cannot be listed here"
		}
		if t.Public == "" {
			return nil, "a hosting entry has no public directory, so what it deploys cannot be listed"
		}
		out = append(out, deployDir{filepath.Clean(t.Public), t.Ignore})
	}
	fns, err := rawList(cfg.Functions)
	if err != nil {
		return nil, "firebase.json functions is not readable"
	}
	for _, f := range fns {
		var t firebaseTarget
		if err := json.Unmarshal(f, &t); err != nil {
			return nil, "firebase.json functions is not readable"
		}
		if hasPredeploy(t.Predeploy) {
			return nil, predeployWhy("functions")
		}
		src := t.Source
		if src == "" {
			src = "functions"
		}
		out = append(out, deployDir{filepath.Clean(src), t.Ignore})
	}
	if len(out) == 0 {
		return nil, "firebase.json has no hosting or functions to scan"
	}
	return out, ""
}

// rawList reads a JSON value that is an object or an array of objects.
func rawList(raw json.RawMessage) ([]json.RawMessage, error) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return nil, nil
	}
	if strings.HasPrefix(s, "[") {
		var l []json.RawMessage
		return l, json.Unmarshal(raw, &l)
	}
	return []json.RawMessage{raw}, nil
}

// ignored reports whether rel (a slash path) is excluded by a firebase ignore glob. A glob that
// matches a parent directory excludes everything under it. Patterns are anchored at the deploy
// directory, so a pattern is never read as matching more than it says.
func ignored(rel string, patterns []string) bool {
	parts := strings.Split(rel, "/")
	for _, p := range patterns {
		re, err := globRegexp(p)
		if err != nil {
			continue
		}
		for i := 1; i <= len(parts); i++ {
			if re.MatchString(strings.Join(parts[:i], "/")) {
				return true
			}
		}
	}
	return false
}

func globRegexp(glob string) (*regexp.Regexp, error) {
	glob = path.Clean(strings.TrimPrefix(glob, "./"))
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(glob); i++ {
		switch c := glob[i]; c {
		case '*':
			if i+1 < len(glob) && glob[i+1] == '*' {
				i++
				if i+1 < len(glob) && glob[i+1] == '/' {
					i++
					b.WriteString("(?:.*/)?")
				} else {
					b.WriteString(".*")
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

// ---- gcloud ----

// keyCommand is a gcloud command that writes secret material to a file. flag is the option that
// names the file; empty means the file is the first argument after the command words.
type keyCommand struct {
	words []string
	flag  string
}

var gcloudKeyCommands = []keyCommand{
	{[]string{"iam", "service-accounts", "keys", "create"}, ""},
	{[]string{"secrets", "versions", "access"}, "--out-file"},
	{[]string{"kms", "decrypt"}, "--plaintext-file"},
}

// gcloudValueFlags are options that take their value as the next word, so it is not read as a
// command word or file.
var gcloudValueFlags = map[string]bool{
	"--project": true, "--account": true, "--impersonate-service-account": true, "--configuration": true,
	"--format": true, "--filter": true, "--verbosity": true, "--billing-project": true, "--flags-file": true,
	"--iam-account": true, "--key-file-type": true, "--out-file": true, "--plaintext-file": true,
	"--ciphertext-file": true, "--key": true, "--keyring": true, "--location": true, "--secret": true,
	"--version": true, "--quota-project": true,
}

// CheckGcloud lets gcloud through unless the command writes a key or secret to a file inside a git
// working tree. If the command looks like a key command but cannot be read, it is refused.
func CheckGcloud(env CloudEnv, args []string) Verdict {
	const action = "cloud gcloud"
	var pos []string
	flags := map[string]string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			pos = append(pos, args[i+1:]...)
			i = len(args)
		case strings.HasPrefix(a, "--") && strings.Contains(a, "="):
			k, v, _ := strings.Cut(a, "=")
			flags[k] = v
		case strings.HasPrefix(a, "-") && len(a) > 1:
			if gcloudValueFlags[a] && i+1 < len(args) {
				flags[a] = args[i+1]
				i++
			} else {
				flags[a] = ""
			}
		default:
			pos = append(pos, a)
		}
	}
	for len(pos) > 0 && (pos[0] == "alpha" || pos[0] == "beta") {
		pos = pos[1:]
	}
	for _, kc := range gcloudKeyCommands {
		idx := indexWords(pos, kc.words)
		if idx < 0 {
			continue
		}
		// An unknown option's value stays among the positionals, so every word after the command
		// is a possible file. All of them are checked.
		if _, ok := flags["--flags-file"]; ok {
			return refuse(action, action+" refused: "+strings.Join(kc.words, " ")+" with --flags-file can set the output file from the file, which cannot be checked. Pass the options on the command line", nil)
		}
		var files []string
		if kc.flag != "" {
			if f := flags[kc.flag]; f != "" {
				files = []string{f}
			} else {
				return allow(action) // no file flag: the value goes to stdout
			}
		} else {
			files = pos[idx+len(kc.words):]
			if len(files) == 0 {
				return refuse(action, action+" refused: "+strings.Join(kc.words, " ")+" has no output file argument that can be read, so where the key lands cannot be checked", nil)
			}
		}
		for _, file := range files {
			if file == "-" || file == "/dev/stdout" || file == "/dev/stderr" {
				continue
			}
			if v := checkKeyFilePath(env, action, kc, file); !v.Allowed {
				return v
			}
		}
		return allow(action)
	}
	// Looks like key creation but did not parse: do not guess.
	if containsAll(args, "service-accounts", "keys", "create") {
		return refuse(action, action+" refused: this looks like iam service-accounts keys create but the command cannot be read, so where the key lands cannot be checked. Put the options after the command", nil)
	}
	return allow(action)
}

// indexWords returns where words appear, in order and next to each other, among pos, or -1.
func indexWords(pos, words []string) int {
	for i := 0; i+len(words) <= len(pos); i++ {
		match := true
		for j, w := range words {
			if pos[i+j] != w {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

func containsAll(args []string, words ...string) bool {
	for _, w := range words {
		found := false
		for _, a := range args {
			if a == w {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// checkKeyFilePath refuses when the output file would be inside a git working tree.
func checkKeyFilePath(env CloudEnv, action string, kc keyCommand, file string) Verdict {
	abs := file
	if !filepath.IsAbs(abs) {
		wd, err := env.FS.Getwd()
		if err != nil {
			return refuse(action, action+" refused: cannot read the current directory, so where the key lands cannot be checked", nil)
		}
		abs = filepath.Join(wd, abs)
	}
	abs = filepath.Clean(abs)
	dir := filepath.Dir(abs)
	for !env.FS.Exists(dir) {
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	out, err := env.R.Git([]string{"-C", dir}, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		var coded interface{ ExitCode() int }
		if errors.As(err, &coded) && coded.ExitCode() == 128 {
			return allow(action) // not a git repository
		}
		return refuse(action, fmt.Sprintf("%s refused: git could not say whether %s is inside a repository (%v)", action, dir, err), nil)
	}
	if strings.TrimSpace(out) == "true" {
		return refuse(action, fmt.Sprintf("%s refused: `%s` would write the key to %s, which is inside a git working tree. Write it outside the repository (for example under ~/.config/gcloud/) and never commit it", action, strings.Join(kc.words, " "), file), nil)
	}
	return allow(action)
}

func hasPredeploy(raw json.RawMessage) bool {
	switch strings.TrimSpace(string(raw)) {
	case "", "null", "[]", `""`:
		return false
	}
	return true
}

func predeployWhy(what string) string {
	return what + ".predeploy builds files after they are scanned, so the build would never be checked. Build first, then remove predeploy from firebase.json"
}
