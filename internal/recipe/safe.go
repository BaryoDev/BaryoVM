// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package recipe

import (
	"fmt"
	"regexp"
	"strings"
)

// A recipe comes from an app repository, so every name, image, port and path in it is untrusted
// input that ends up in a compose file and in remote shell commands. The deploy quotes every value
// with sshx.Quote, and these checks refuse what has no business being there at all, so a value like
// "; rm -rf /" or "../" stops at the parser instead of relying on the quoting alone.
var (
	serviceNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
	imageRe       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@-]*$`)
	portRe        = regexp.MustCompile(`^[0-9]{1,5}:[0-9]{1,5}(/(tcp|udp))?$`)
	healthRe      = regexp.MustCompile(`^https?://[A-Za-z0-9._:/?=&%-]+$`)
	pathRe        = regexp.MustCompile(`^[A-Za-z0-9._/@+-]+$`)
	hostRe        = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?$`)
	routePathRe   = regexp.MustCompile(`^/[A-Za-z0-9._/-]*$`)
	recipeNameRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

// hasDotDot reports a ".." path segment.
func hasDotDot(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}

// checkRelPath validates a path that is relative to the project root.
func checkRelPath(what, p, recipePath string) error {
	if !pathRe.MatchString(p) || strings.HasPrefix(p, "/") || hasDotDot(p) {
		return fmt.Errorf("recipe %s: %s %q must be a plain relative path with letters, digits and ._/@+- only, and no \"..\"", recipePath, what, p)
	}
	return nil
}

// validateSafe checks the shape of every value that reaches a command or a compose file.
func (r *Recipe) validateSafe(path string) error {
	if !recipeNameRe.MatchString(r.Name) {
		return fmt.Errorf("recipe %s: name %q must be letters, digits and ._- only", path, r.Name)
	}
	for _, s := range r.Services {
		if !serviceNameRe.MatchString(s.Name) {
			return fmt.Errorf("recipe %s: service name %q must be lowercase letters, digits, '-' and '_'", path, s.Name)
		}
		if s.Image != "" && !imageRe.MatchString(s.Image) {
			return fmt.Errorf("recipe %s: service %q image %q is not a plain registry reference", path, s.Name, s.Image)
		}
		if s.Build != "" {
			if err := checkRelPath("service "+s.Name+" build", s.Build, path); err != nil {
				return err
			}
		}
		for _, p := range s.Ports {
			if !portRe.MatchString(p) {
				return fmt.Errorf("recipe %s: service %q port %q must look like 8080:80", path, s.Name, p)
			}
		}
		if s.HealthURL != "" && !healthRe.MatchString(s.HealthURL) {
			return fmt.Errorf("recipe %s: service %q healthUrl %q must be a plain http or https URL", path, s.Name, s.HealthURL)
		}
	}
	for i, f := range r.Files {
		if err := checkRelPath(fmt.Sprintf("files %d from", i), f.From, path); err != nil {
			return err
		}
		if !pathRe.MatchString(f.To) || hasDotDot(f.To) {
			return fmt.Errorf("recipe %s: files %d to %q must be an absolute path with letters, digits and ._/@+- only, and no \"..\"", path, i, f.To)
		}
	}
	for _, rt := range r.Routes {
		if !hostRe.MatchString(rt.Host) {
			return fmt.Errorf("recipe %s: route host %q is not a hostname", path, rt.Host)
		}
		if rt.Path != "" && (!routePathRe.MatchString(rt.Path) || hasDotDot(rt.Path)) {
			return fmt.Errorf("recipe %s: route %q path %q must start with / and hold plain path characters", path, rt.Host, rt.Path)
		}
	}
	return nil
}
