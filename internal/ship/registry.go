// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package ship

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
)

// Registry is the read-only seam to a package registry. Two implementations, nuget and npm, and no
// third until something needs it. Nothing here publishes or holds a credential.
type Registry interface {
	Name() string
	// Version reads the version out of the versionFrom file's bytes.
	Version(file []byte) (string, error)
	// PackageName is the package the versionFrom file declares, path being its path.
	PackageName(path string, file []byte) string
	// ReleaseNotes is the registry's own release notes field in that file, and whether it has one.
	ReleaseNotes(file []byte) (text string, present bool)
	// Published reports whether pkg is indexed at version.
	Published(ctx context.Context, pkg, version string) (bool, error)
	// Metadata is the published artifact's declared metadata: the nuspec, or the version document.
	Metadata(ctx context.Context, pkg, version string) ([]byte, error)
	// MetadataVersion reads the version the published metadata declares.
	MetadataVersion(meta []byte) (string, error)
	// SameVersion compares two versions the way the registry does: NuGet ignores build metadata
	// and a zero fourth segment, so 1.3.0.0 and 1.3.0+build are 1.3.0.
	SameVersion(a, b string) bool
}

// Doer is the HTTP seam.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// NewRegistry builds the registry a spec names over doer.
func NewRegistry(s Spec, doer Doer) (Registry, error) {
	switch s.Registry {
	case NuGet:
		base := strings.TrimRight(s.RegistryURL, "/")
		if base == "" {
			base = "https://api.nuget.org/v3-flatcontainer"
		}
		return nugetRegistry{base: base, h: doer}, nil
	case Npm:
		base := strings.TrimRight(s.RegistryURL, "/")
		if base == "" {
			base = "https://registry.npmjs.org"
		}
		return npmRegistry{base: base, h: doer}, nil
	}
	return nil, fmt.Errorf("registry %q is not supported", s.Registry)
}

const maxBody = 4 << 20

// get reads one URL. found is false on a 404; any other non-200 is an error.
func get(ctx context.Context, h Doer, u string) (body []byte, found bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, false, err
	}
	resp, err := h.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("GET %s: %w", u, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("GET %s: HTTP %d", u, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, false, fmt.Errorf("GET %s: %w", u, err)
	}
	return b, true, nil
}

// ---- nuget ----

type nugetRegistry struct {
	base string
	h    Doer
}

var (
	nugetVersionRe = regexp.MustCompile(`(?s)<Version>\s*([^<\s]+)\s*</Version>`)
	nugetNotesRe   = regexp.MustCompile(`(?s)<PackageReleaseNotes>(.*?)</PackageReleaseNotes>`)
	nugetIDRe      = regexp.MustCompile(`(?s)<PackageId>\s*([^<\s]+)\s*</PackageId>`)
	nuspecVerRe    = regexp.MustCompile(`(?s)<version>\s*([^<\s]+)\s*</version>`)
)

func (nugetRegistry) Name() string { return NuGet }

func (nugetRegistry) Version(f []byte) (string, error) {
	m := nugetVersionRe.FindSubmatch(f)
	if m == nil {
		return "", errors.New("no <Version> element")
	}
	return string(m[1]), nil
}

// PackageName is the declared PackageId, or the project's own name when the file is a project. A
// $(property) value, or a shared props file such as Directory.Build.props, names no package, so
// the answer is empty and the gate asks for publish.packages.
func (nugetRegistry) PackageName(path string, f []byte) string {
	if m := nugetIDRe.FindSubmatch(f); m != nil {
		id := string(m[1])
		if strings.Contains(id, "$(") {
			return ""
		}
		return id
	}
	base := filepath.Base(path)
	switch strings.ToLower(filepath.Ext(base)) {
	case ".csproj", ".fsproj", ".vbproj":
		return strings.TrimSuffix(base, filepath.Ext(base))
	}
	return ""
}

// normNuGet is NuGet's normalised version: build metadata dropped, leading zeros dropped, three
// segments (a fourth only when not zero), the prerelease label lower case.
func normNuGet(v string) string {
	v = strings.TrimSpace(v)
	if i := strings.Index(v, "+"); i >= 0 {
		v = v[:i]
	}
	pre := ""
	if i := strings.Index(v, "-"); i >= 0 {
		v, pre = v[:i], strings.ToLower(v[i:])
	}
	parts := strings.Split(v, ".")
	for i, p := range parts {
		if t := strings.TrimLeft(p, "0"); t == "" {
			parts[i] = "0"
		} else {
			parts[i] = t
		}
	}
	for len(parts) < 3 {
		parts = append(parts, "0")
	}
	if len(parts) == 4 && parts[3] == "0" {
		parts = parts[:3]
	}
	return strings.Join(parts, ".") + pre
}

func (nugetRegistry) SameVersion(a, b string) bool { return normNuGet(a) == normNuGet(b) }

func (nugetRegistry) ReleaseNotes(f []byte) (string, bool) {
	m := nugetNotesRe.FindSubmatch(f)
	if m == nil {
		return "", false
	}
	return string(m[1]), true
}

func (r nugetRegistry) Published(ctx context.Context, pkg, version string) (bool, error) {
	b, found, err := get(ctx, r.h, r.base+"/"+url.PathEscape(strings.ToLower(pkg))+"/index.json")
	if err != nil || !found {
		return false, err
	}
	var idx struct {
		Versions []string `json:"versions"`
	}
	if err := json.Unmarshal(b, &idx); err != nil {
		return false, fmt.Errorf("nuget index for %s is not JSON: %w", pkg, err)
	}
	for _, v := range idx.Versions {
		if normNuGet(v) == normNuGet(version) {
			return true, nil
		}
	}
	return false, nil
}

func (r nugetRegistry) Metadata(ctx context.Context, pkg, version string) ([]byte, error) {
	id, v := url.PathEscape(strings.ToLower(pkg)), url.PathEscape(normNuGet(version))
	b, found, err := get(ctx, r.h, r.base+"/"+id+"/"+v+"/"+id+".nuspec")
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("%s %s has no nuspec on the registry yet", pkg, version)
	}
	return b, nil
}

func (nugetRegistry) MetadataVersion(meta []byte) (string, error) {
	m := nuspecVerRe.FindSubmatch(meta)
	if m == nil {
		return "", errors.New("the nuspec declares no <version>")
	}
	return string(m[1]), nil
}

// ---- npm ----

type npmRegistry struct {
	base string
	h    Doer
}

func (npmRegistry) Name() string { return Npm }

func (npmRegistry) Version(f []byte) (string, error) {
	var p struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(f, &p); err != nil {
		return "", fmt.Errorf("not valid JSON: %w", err)
	}
	if p.Version == "" {
		return "", errors.New("no \"version\" field")
	}
	return p.Version, nil
}

func (npmRegistry) PackageName(_ string, f []byte) string {
	var p struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(f, &p)
	return p.Name
}

// npm has no release notes field: the changelog check is the whole of it.
func (npmRegistry) ReleaseNotes([]byte) (string, bool) { return "", false }

func (r npmRegistry) docURL(pkg, version string) string {
	return r.base + "/" + strings.Replace(url.PathEscape(pkg), "/", "%2F", 1) + "/" + url.PathEscape(version)
}

func (r npmRegistry) Published(ctx context.Context, pkg, version string) (bool, error) {
	_, found, err := get(ctx, r.h, r.docURL(pkg, version))
	return found, err
}

func (r npmRegistry) Metadata(ctx context.Context, pkg, version string) ([]byte, error) {
	b, found, err := get(ctx, r.h, r.docURL(pkg, version))
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("%s %s is not on the registry yet", pkg, version)
	}
	return b, nil
}

func (npmRegistry) SameVersion(a, b string) bool { return a == b }

func (npmRegistry) MetadataVersion(meta []byte) (string, error) {
	return npmRegistry{}.Version(meta)
}
