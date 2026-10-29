// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package ship holds the release gate and the drive-and-verify half of a release (#51, #28).
//
// The boundary is the design: Actions publishes, BaryoVM never does. Trusted publishing binds an
// OIDC token to the workflow identity, so a laptop cannot and should not publish. This package
// reads registries, reads and writes git tags, watches the run the tag triggered, checks the
// publish step really ran, checks what the registry now serves, and creates the GitHub release.
// It holds no registry credential and no secret of its own: the GitHub token arrives as a value
// from the caller, resolved from a secretref reference.
//
// Every outside call goes through a seam (Git, GitHub, Registry, Deps.Deployed), each under a
// timeout, so the whole flow runs in tests with fakes. The package never prints.
package ship

import (
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
)

// Registry kinds.
const (
	NuGet = "nuget"
	Npm   = "npm"
)

// Spec is the publish settings of one project: what a release of it must satisfy and where it is
// published. It lives on a stack (`stack set-publish`), in a project file's stack, or in a
// baryovm.publish.json beside the code. Nothing in it is a secret.
type Spec struct {
	// Registry is "nuget" or "npm".
	Registry string `json:"registry"`
	// VersionFrom is the file that holds the version, relative to the repository: a csproj (or
	// props) with <Version> for nuget, package.json for npm.
	VersionFrom string `json:"versionFrom"`
	// Changelog is the file that must hold a section for the version. Default CHANGELOG.md.
	Changelog string `json:"changelog,omitempty"`
	// RequireDeployed names a stack that must have released this exact commit first.
	RequireDeployed string `json:"requireDeployed,omitempty"`
	// Tag is the tag template, {version} replaced. Default v{version}.
	Tag string `json:"tag,omitempty"`
	// Packages are the registry names that must all resolve at the version. Default: the one
	// the versionFrom file declares.
	Packages []string `json:"packages,omitempty"`
	// RegistryURL points at a registry other than nuget.org or registry.npmjs.org. Reads only.
	RegistryURL string `json:"registryUrl,omitempty"`
	// Remote is the git remote. Default origin.
	Remote string `json:"remote,omitempty"`
	// Branch is the default branch. Default: what the remote's HEAD names.
	Branch string `json:"branch,omitempty"`
	// Repo is owner/name on GitHub. Default: read from the remote's URL.
	Repo string `json:"repo,omitempty"`
	// Workflow limits the run search to workflows whose name or file contains this.
	Workflow string `json:"workflow,omitempty"`
	// PublishStep is the exact name of the step that publishes (a job with no steps is matched by
	// its job name), compared without regard to case. Default "publish". Every step it matches must
	// have run and succeeded, so a matrix with one skipped leg fails.
	PublishStep string `json:"publishStep,omitempty"`
	// PublishJob limits the match to the job with this name. Optional.
	PublishJob string `json:"publishJob,omitempty"`
	// Assert are lines the published artifact's metadata must contain. {version} and {package}
	// are replaced. The metadata's own version is always asserted.
	Assert []string `json:"assert,omitempty"`
}

var (
	tagTemplateRe = regexp.MustCompile(`^[A-Za-z0-9._/{}-]+$`)
	repoRe        = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	remoteRe      = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)
	branchRe      = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_./-]*$`)
)

// Validate checks the spec's shape. It touches nothing.
func (s Spec) Validate() error {
	switch s.Registry {
	case NuGet, Npm:
	case "":
		return fmt.Errorf("publish.registry is empty: use %q or %q", NuGet, Npm)
	default:
		return fmt.Errorf("publish.registry %q is not supported: use %q or %q", s.Registry, NuGet, Npm)
	}
	if err := inside("publish.versionFrom", s.VersionFrom); err != nil {
		return err
	}
	if s.Changelog != "" {
		if err := inside("publish.changelog", s.Changelog); err != nil {
			return err
		}
	}
	if t := s.Tag; t != "" && (!tagTemplateRe.MatchString(t) || !strings.Contains(t, "{version}") || strings.Contains(t, "..") || strings.HasPrefix(t, "-")) {
		return fmt.Errorf("publish.tag %q must contain {version} and only letters, digits and . _ / -", t)
	}
	if s.Remote != "" && !remoteRe.MatchString(s.Remote) {
		return fmt.Errorf("publish.remote %q is not a remote name", s.Remote)
	}
	if s.Branch != "" && (!branchRe.MatchString(s.Branch) || strings.Contains(s.Branch, "..")) {
		return fmt.Errorf("publish.branch %q is not a branch name", s.Branch)
	}
	if s.Repo != "" && !repoRe.MatchString(s.Repo) {
		return fmt.Errorf("publish.repo %q must be owner/name", s.Repo)
	}
	if s.RegistryURL != "" {
		u, err := url.Parse(s.RegistryURL)
		local := err == nil && u.Scheme == "http" && u.Hostname() == "127.0.0.1"
		if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && !local) {
			return fmt.Errorf("publish.registryUrl %q must be an https URL without credentials (http only for 127.0.0.1)", redact(s.RegistryURL))
		}
	}
	for _, a := range s.Assert {
		if strings.TrimSpace(a) == "" {
			return fmt.Errorf("publish.assert has an empty line: an empty assertion holds for any artifact")
		}
	}
	for _, p := range s.Packages {
		if strings.TrimSpace(p) == "" || strings.ContainsAny(p, " \t\r\n?#") {
			return fmt.Errorf("publish.packages has a bad name %q", p)
		}
	}
	return nil
}

var versionRe = regexp.MustCompile(`^[0-9]+(\.[0-9]+){0,3}(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)

// validVersion is the shape a version must have before it reaches a tag name, a git argument, a URL
// or a file name: digits and dots, then an optional -prerelease and +build.
func validVersion(v string) error {
	if !versionRe.MatchString(v) || strings.Contains(v, "..") {
		return fmt.Errorf("%q is not a version: expected digits and dots with an optional -prerelease and +build, such as 1.2.3 or 1.2.3-beta.1", v)
	}
	return nil
}

// ValidVersion is validVersion for callers outside the package, such as `baryovm prove`, which
// checks a version the same way ship does before it builds a record path.
func ValidVersion(v string) error { return validVersion(v) }

// inside refuses an empty path, an absolute one and one that climbs out of the repository: a
// committed spec must not make the gate read an arbitrary file.
func inside(field, p string) error {
	if strings.TrimSpace(p) == "" {
		return fmt.Errorf("%s is empty", field)
	}
	c := filepath.Clean(filepath.FromSlash(p))
	if filepath.IsAbs(c) || strings.HasPrefix(p, "/") || c == ".." || strings.HasPrefix(c, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%s %q must be relative and stay inside the repository", field, p)
	}
	return nil
}

func (s Spec) changelog() string {
	if s.Changelog == "" {
		return "CHANGELOG.md"
	}
	return s.Changelog
}

func (s Spec) remote() string {
	if s.Remote == "" {
		return "origin"
	}
	return s.Remote
}

func (s Spec) publishStep() string {
	if strings.TrimSpace(s.PublishStep) == "" {
		return "publish"
	}
	return s.PublishStep
}

// TagFor is the tag a version gets.
func (s Spec) TagFor(version string) string {
	t := s.Tag
	if t == "" {
		t = "v{version}"
	}
	return strings.ReplaceAll(t, "{version}", version)
}
