// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package repomove moves a stack's source repository to another Git host and repoints what pulls
// from it. It builds argvs and remote command strings, parses what comes back, and decides the
// order; it never prints and never runs a program itself. The cli layer owns the seams (local
// argv, one SSH runner per VM), so every step is testable with fakes and no network.
package repomove

import (
	"fmt"
	"net/url"
	"strings"
)

// Kind names the Git host of a URL, as far as this package can drive it.
type Kind string

const (
	GitHub Kind = "github"
	Azure  Kind = "azure"
	GitLab Kind = "gitlab"
	Other  Kind = "git"
)

// Repo is a parsed remote URL.
type Repo struct {
	URL  string `json:"url"`
	Kind Kind   `json:"kind"`
	Host string `json:"host"`
	Path string `json:"path"` // owner/name for GitHub, org/project/repo for Azure, the path otherwise
	Org  string `json:"-"`    // Azure organisation
	Proj string `json:"-"`    // Azure project
	Name string `json:"-"`    // repository name
	Key  string `json:"-"`    // normalized host/path, for comparing two URLs
}

// Parse validates a remote URL and splits it. A URL that carries a credential is refused: a token
// in a URL ends up in argv, in git's config and in every error that quotes the URL.
func Parse(raw string) (Repo, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Repo{}, fmt.Errorf("empty repository URL")
	}
	if strings.HasPrefix(raw, "-") || strings.ContainsAny(raw, " \t\r\n") {
		return Repo{}, fmt.Errorf("not a repository URL")
	}
	r := Repo{URL: raw}
	var host, path string
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			return Repo{}, fmt.Errorf("not a repository URL")
		}
		host, path = u.Hostname(), strings.Trim(u.Path, "/")
		if u.User != nil {
			if _, hasPw := u.User.Password(); hasPw {
				return Repo{}, fmt.Errorf("the repository URL carries a password or token: remove it and let git or the host CLI sign in")
			}
			if u.Scheme == "http" || u.Scheme == "https" {
				// Azure's own clone URL is https://<org>@dev.azure.com/<org>/..., a name and not a
				// secret. Anything else in front of the @ may be a token.
				org := strings.SplitN(path, "/", 2)[0]
				if !(strings.EqualFold(host, "dev.azure.com") && strings.EqualFold(u.User.Username(), org)) {
					return Repo{}, fmt.Errorf("the repository URL carries a user or token before the host: remove it and let git or the host CLI sign in")
				}
			}
		}
	} else {
		// scp style: git@host:path
		at := strings.Index(raw, "@")
		colon := strings.Index(raw, ":")
		if colon < 0 || strings.Contains(raw[:colon], "/") {
			return Repo{}, fmt.Errorf("not a repository URL")
		}
		hostPart := raw[:colon]
		if at >= 0 && at < colon {
			hostPart = raw[at+1 : colon]
		}
		host, path = hostPart, strings.Trim(raw[colon+1:], "/")
	}
	host = strings.ToLower(host)
	path = strings.TrimSuffix(path, ".git")
	if host == "" || path == "" {
		return Repo{}, fmt.Errorf("not a repository URL")
	}
	r.Host, r.Path, r.Kind = host, path, Other
	parts := strings.Split(path, "/")
	r.Name = parts[len(parts)-1]
	switch {
	case host == "github.com" && len(parts) == 2:
		r.Kind = GitHub
	case host == "dev.azure.com" && len(parts) == 4 && parts[2] == "_git":
		r.Kind, r.Org, r.Proj, r.Name = Azure, parts[0], parts[1], parts[3]
		r.Path = parts[0] + "/" + parts[1] + "/" + parts[3]
	case host == "ssh.dev.azure.com" && len(parts) == 4 && parts[0] == "v3":
		r.Kind, r.Org, r.Proj, r.Name = Azure, parts[1], parts[2], parts[3]
		r.Path = parts[1] + "/" + parts[2] + "/" + parts[3]
		r.Host = "dev.azure.com"
	case host == "gitlab.com":
		r.Kind = GitLab
	}
	r.Key = strings.ToLower(r.Host + "/" + r.Path)
	return r, nil
}

// Same reports whether two parsed URLs name one repository, whatever the scheme or user.
func Same(a, b Repo) bool { return a.Key == b.Key }

// CreateArgv is the host CLI call that creates an empty private repository at r. It is the host's
// own signed-in CLI, never an API call with a token held here.
func CreateArgv(r Repo) ([]string, error) {
	switch r.Kind {
	case GitHub:
		return []string{"gh", "repo", "create", r.Path, "--private"}, nil
	case Azure:
		return []string{"az", "repos", "create", "--name", r.Name, "--project", r.Proj,
			"--organization", "https://dev.azure.com/" + r.Org}, nil
	}
	return nil, fmt.Errorf("baryovm cannot create a repository on %s: create it there by hand (empty, no README), then run the move again", r.Host)
}

// OldRepoCommands are the commands to retire the old repository. They are printed after the move,
// never run: the old repository is left alone.
func OldRepoCommands(r Repo) []string {
	if r.Kind == GitHub {
		return []string{"gh repo archive " + r.Path, "gh repo delete " + r.Path}
	}
	return []string{"archive or delete " + r.URL + " on " + r.Host + " when you are sure"}
}
