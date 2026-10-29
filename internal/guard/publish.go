// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package guard

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Size caps for what a publish check reads. A package over a cap is refused, never skipped: a
// file that is not scanned could hold a key. They are variables so tests can lower them.
var (
	MaxEntryBytes int64 = 64 << 20
	MaxTotalBytes int64 = 512 << 20
)

const (
	npmDefaultRegistry = "https://registry.npmjs.org/"
	nugetDefaultSource = "https://api.nuget.org/v3/index.json"
)

// publicRegistries are the hosts anyone can read from, so a private package must not go there.
var publicRegistries = map[string]bool{
	"registry.npmjs.org":   true,
	"registry.yarnpkg.com": true,
	"api.nuget.org":        true,
	"www.nuget.org":        true,
	"nuget.org":            true,
}

// Exec runs a command in dir and returns its stdout. It is the seam to npm, so tests never run it.
type Exec func(dir, name string, args []string) (string, error)

// registryHost returns the lowercased host of a registry URL. The error never echoes the URL: it
// can carry a token as userinfo.
func registryHost(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" || !strings.Contains(raw, "://") {
		return "", errors.New("the registry is not a readable URL")
	}
	return strings.ToLower(u.Hostname()), nil
}

// npmArgs holds what the wrapper needs from the publish arguments. Everything is passed on to npm
// unchanged; this only reads them.
type npmArgs struct {
	dir string
	// cfg are every option the user gave (flags with their values, not the package argument), passed
	// to `npm config get` so it answers for the same command line, including abbreviated and
	// unknown flags.
	cfg []string
}

var npmValueFlags = map[string]bool{"--tag": true, "--access": true, "--otp": true, "--registry": true, "--userconfig": true, "--globalconfig": true, "--prefix": true, "--provenance-file": true}

func parseNpmArgs(args []string) (npmArgs, error) {
	var a npmArgs
	var pos []string
	for i := 0; i < len(args); i++ {
		w := args[i]
		if w == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		switch {
		case w == "-w" || w == "--workspace" || w == "--workspaces" || w == "-ws" || strings.HasPrefix(w, "--workspace=") || strings.HasPrefix(w, "-w="):
			return a, errors.New("workspace publishing cannot be checked: publish each package directory on its own")
		case strings.HasPrefix(w, "--"):
			name, _, hasVal := strings.Cut(w, "=")
			a.cfg = append(a.cfg, w)
			if !hasVal && npmValueFlags[name] && i+1 < len(args) {
				i++
				a.cfg = append(a.cfg, args[i])
			}
		case strings.HasPrefix(w, "-") && len(w) > 1:
			a.cfg = append(a.cfg, w)
		default:
			pos = append(pos, w)
		}
	}
	if len(pos) > 1 {
		return a, errors.New("name one package directory")
	}
	a.dir = "."
	if len(pos) == 1 {
		a.dir = pos[0]
		low := strings.ToLower(a.dir)
		if strings.HasSuffix(low, ".tgz") || strings.HasSuffix(low, ".tar.gz") {
			return a, errors.New("a tarball cannot be checked: publish the package directory so every file is scanned")
		}
	}
	return a, nil
}

type packFile struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

type packEntry struct {
	Files []packFile `json:"files"`
}

// parsePack reads the JSON `npm pack --json` prints. A lifecycle script can print text before it,
// so when the whole output is not JSON it tries from each later line that starts an array.
func parsePack(out string) ([]packEntry, error) {
	try := func(s string) ([]packEntry, bool) {
		var e []packEntry
		if json.Unmarshal([]byte(s), &e) == nil && len(e) > 0 {
			return e, true
		}
		return nil, false
	}
	if e, ok := try(out); ok {
		return e, nil
	}
	lines := strings.SplitAfter(out, "\n")
	for i := range lines {
		if strings.HasPrefix(lines[i], "[") {
			if e, ok := try(strings.Join(lines[i:], "")); ok {
				return e, nil
			}
		}
	}
	return nil, errors.New("npm pack printed JSON that could not be read")
}

type pkgJSON struct {
	Name          string         `json:"name"`
	Private       bool           `json:"private"`
	PublishConfig map[string]any `json:"publishConfig"`
}

func readOptional(r Runner, path string) (string, error) {
	b, err := r.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	return string(b), nil
}

// CheckNpmPublish plans `npm publish`: pack the package without writing anything, scan every file
// npm would ship, then check the registry npm would publish to against the policy.
func CheckNpmPublish(r Runner, run Exec, args []string, pol Policy) Verdict {
	const action = "publish npm"
	a, err := parseNpmArgs(args)
	if err != nil {
		return refuse(action, action+" refused: "+err.Error(), nil)
	}
	out, err := run(a.dir, "npm", []string{"pack", "--dry-run", "--json"})
	if err != nil {
		return refuse(action, fmt.Sprintf("%s refused: npm pack --dry-run failed (%v), so the files cannot be checked", action, err), nil)
	}
	packs, err := parsePack(out)
	if err != nil {
		return refuse(action, action+" refused: "+err.Error()+", so the files cannot be checked", nil)
	}

	pj, err := readOptional(r, filepath.Join(a.dir, "package.json"))
	var pkg pkgJSON
	if err == nil && pj != "" {
		err = json.Unmarshal([]byte(pj), &pkg)
	}
	if err != nil || pj == "" {
		return refuse(action, action+" refused: cannot read package.json in "+a.dir, nil)
	}

	// Ask npm which registry applies, so project, user, global and builtin config, environment
	// variables and flags are resolved by npm and not by a copy of its rules here. A failure or an
	// answer that is not a URL refuses.
	layers := []map[string]string{pubConfig(pkg.PublishConfig)}
	if scope, _, ok := strings.Cut(pkg.Name, "/"); ok && strings.HasPrefix(scope, "@") {
		v, err := npmConfigGet(run, a, scope+":registry")
		if err != nil {
			return refuse(action, fmt.Sprintf("%s refused: %v, so the registry cannot be resolved", action, err), nil)
		}
		layers = append(layers, map[string]string{scope + ":registry": v})
	}
	v, err := npmConfigGet(run, a, "registry")
	if err != nil || v == "" {
		return refuse(action, fmt.Sprintf("%s refused: npm config get registry gave no answer (%v), so the registry cannot be resolved", action, err), nil)
	}
	layers = append(layers, map[string]string{"registry": v})
	raw := resolveRegistry(pkg.Name, layers)
	host, err := registryHost(raw)
	if err != nil {
		return refuse(action, fmt.Sprintf("%s refused: %s. Fix the registry setting npm reports", action, err), nil)
	}
	if pkg.Private && publicRegistries[host] {
		return refuse(action, fmt.Sprintf("%s refused: package.json is private but the registry resolves to the public registry %s. Point publishConfig.registry or .npmrc at your private registry", action, host), nil)
	}
	if !pol.AllowsRegistry(host) {
		return refuse(action, fmt.Sprintf("%s refused: registry %s is not in %s (allowed: %s)", action, host, RegistryPolicyKey, strings.Join(pol.AllowedRegistries, ", ")), nil)
	}

	var (
		fs    []Finding
		total int64
	)
	for _, p := range packs {
		for _, f := range p.Files {
			if !filepath.IsLocal(filepath.FromSlash(f.Path)) {
				return refuse(action, fmt.Sprintf("%s refused: npm listed a path outside the package (%s)", action, f.Path), nil)
			}
			if f.Size > MaxEntryBytes {
				return refuse(action, fmt.Sprintf("%s refused: %s is %d bytes, over the %d byte limit the scan reads", action, f.Path, f.Size, MaxEntryBytes), nil)
			}
			b, err := r.ReadFile(filepath.Join(a.dir, filepath.FromSlash(f.Path)))
			if err != nil {
				return refuse(action, fmt.Sprintf("%s refused: cannot read %s to scan it (%v)", action, f.Path, err), nil)
			}
			total += int64(len(b))
			if int64(len(b)) > MaxEntryBytes || total > MaxTotalBytes {
				return refuse(action, fmt.Sprintf("%s refused: the package is over the size the scan reads (%s)", action, f.Path), nil)
			}
			fs = append(fs, ScanText(f.Path, string(b))...)
		}
	}
	if len(fs) > 0 {
		return keyRefusal(action, "the files npm would publish", fs)
	}
	return allow(action)
}

// npmConfigGet runs `npm config get key` with the config-affecting arguments. npm prints
// "undefined" for a key that is not set; that comes back as "".
func npmConfigGet(run Exec, a npmArgs, key string) (string, error) {
	// Run in the process cwd: `npm publish <folder>` reads project config from there, not from the folder.
	out, err := run("", "npm", append([]string{"config", "get", key}, a.cfg...))
	if err != nil {
		return "", fmt.Errorf("npm config get %s failed (%v)", key, err)
	}
	out = strings.TrimSpace(out)
	if out == "" || out == "undefined" || out == "null" {
		return "", nil
	}
	return out, nil
}

func pubConfig(m map[string]any) map[string]string {
	out := map[string]string{}
	for k, v := range m {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

// resolveRegistry picks the registry npm publishes to: a scoped @scope:registry first, then the
// plain registry, each from the highest layer that sets it, then the npm default.
func resolveRegistry(name string, layers []map[string]string) string {
	if strings.HasPrefix(name, "@") {
		if scope, _, ok := strings.Cut(name, "/"); ok {
			for _, l := range layers {
				if v := l[scope+":registry"]; v != "" {
					return v
				}
			}
		}
	}
	for _, l := range layers {
		if v := l["registry"]; v != "" {
			return v
		}
	}
	return npmDefaultRegistry
}

var nugetValueFlags = map[string]bool{
	"-s": true, "--source": true, "-ss": true, "--symbol-source": true, "-w": true, "--timeout": true,
	"-k": true, "--api-key": true, "-sk": true, "--symbol-api-key": true, "--configfile": true,
}

// CheckNuGetPush plans `dotnet nuget push`: check the source (and symbol source) against the
// policy, then scan every entry of every package named.
func CheckNuGetPush(r Runner, args []string, pol Policy) Verdict {
	const action = "publish nuget"
	var pkgs []string
	source, symbols := "", ""
	noSymbols, configfile := false, false
	for i := 0; i < len(args); i++ {
		w := args[i]
		name, val, hasVal := strings.Cut(w, "=")
		if strings.HasPrefix(w, "-") && len(w) > 1 {
			if !hasVal && nugetValueFlags[name] && i+1 < len(args) {
				i++
				val = args[i]
			}
			switch name {
			case "-n", "--no-symbols":
				noSymbols = true
			case "--configfile":
				configfile = true
			case "-s", "--source":
				source = val
			case "-ss", "--symbol-source":
				symbols = val
			}
			continue
		}
		pkgs = append(pkgs, w)
	}
	if len(pkgs) == 0 {
		return refuse(action, action+" refused: name the .nupkg file to push", nil)
	}
	if pol.RegistriesRestricted() {
		// NuGet.Config can redirect the default source and a .snupkg goes to an implicit symbol
		// source, neither of which can be read here, so a policy needs both made explicit.
		switch {
		case configfile:
			return refuse(action, action+" refused: --configfile can change the source in ways that cannot be checked. Remove it and pass --source", nil)
		case source == "":
			return refuse(action, fmt.Sprintf("%s refused: no --source, and the default comes from NuGet.Config which cannot be checked against %s. Pass --source with the feed URL", action, RegistryPolicyKey), nil)
		case !noSymbols && symbols == "":
			return refuse(action, action+" refused: dotnet pushes a .snupkg to an implicit symbol source. Pass --no-symbols, or --symbol-source with the feed URL", nil)
		}
	}
	if source == "" {
		source = nugetDefaultSource
	}
	for _, s := range []string{source, symbols} {
		if s == "" {
			continue
		}
		if v, bad := checkNuGetSource(action, s, pol); bad {
			return v
		}
	}

	var (
		fs    []Finding
		total int64
	)
	for _, pat := range pkgs {
		files := []string{pat}
		if strings.ContainsAny(pat, "*?[") {
			m, err := filepath.Glob(pat)
			if err != nil || len(m) == 0 {
				return refuse(action, fmt.Sprintf("%s refused: no package matches %s", action, pat), nil)
			}
			files = m
		}
		for _, f := range files {
			found, n, v, bad := scanNupkg(r, action, f, total)
			if bad {
				return v
			}
			total = n
			fs = append(fs, found...)
		}
	}
	if len(fs) > 0 {
		return keyRefusal(action, "the package", fs)
	}
	return allow(action)
}

func checkNuGetSource(action, src string, pol Policy) (Verdict, bool) {
	if !pol.RegistriesRestricted() {
		return Verdict{}, false
	}
	if !strings.Contains(src, "://") {
		if strings.ContainsAny(src, `/\`) || strings.HasPrefix(src, ".") || strings.HasPrefix(src, "~") || strings.Contains(src, ":") {
			return Verdict{}, false // a folder: it never reaches the network
		}
		return refuse(action, fmt.Sprintf("%s refused: source %q is a name from NuGet.Config, which cannot be checked against %s. Pass the feed URL", action, src, RegistryPolicyKey), nil), true
	}
	host, err := registryHost(src)
	if err != nil {
		return refuse(action, action+" refused: "+err.Error()+" (--source)", nil), true
	}
	if !pol.AllowsRegistry(host) {
		return refuse(action, fmt.Sprintf("%s refused: source %s is not in %s (allowed: %s)", action, host, RegistryPolicyKey, strings.Join(pol.AllowedRegistries, ", ")), nil), true
	}
	return Verdict{}, false
}

// scanNupkg scans every entry of one package. It returns the running total of bytes read.
func scanNupkg(r Runner, action, path string, total int64) ([]Finding, int64, Verdict, bool) {
	b, err := r.ReadFile(path)
	if err != nil {
		return nil, total, refuse(action, fmt.Sprintf("%s refused: cannot read %s (%v)", action, path, err), nil), true
	}
	if int64(len(b)) > MaxTotalBytes {
		return nil, total, refuse(action, fmt.Sprintf("%s refused: %s is over the size the scan reads", action, path), nil), true
	}
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return nil, total, refuse(action, fmt.Sprintf("%s refused: %s is not a readable package (%v)", action, path, err), nil), true
	}
	base := filepath.Base(path)
	var fs []Finding
	for _, e := range zr.File {
		if e.FileInfo().IsDir() {
			continue
		}
		name := base + ":" + e.Name
		if e.UncompressedSize64 > uint64(MaxEntryBytes) {
			return nil, total, refuse(action, fmt.Sprintf("%s refused: %s is over the %d byte limit the scan reads", action, name, MaxEntryBytes), nil), true
		}
		rc, err := e.Open()
		if err != nil {
			return nil, total, refuse(action, fmt.Sprintf("%s refused: cannot open %s (%v)", action, name, err), nil), true
		}
		data, err := io.ReadAll(io.LimitReader(rc, MaxEntryBytes+1))
		_ = rc.Close()
		if err != nil {
			return nil, total, refuse(action, fmt.Sprintf("%s refused: cannot read %s (%v)", action, name, err), nil), true
		}
		total += int64(len(data))
		if int64(len(data)) > MaxEntryBytes || total > MaxTotalBytes {
			return nil, total, refuse(action, fmt.Sprintf("%s refused: %s is over the size the scan reads", action, name), nil), true
		}
		fs = append(fs, ScanText(name, string(data))...)
	}
	return fs, total, Verdict{}, false
}
