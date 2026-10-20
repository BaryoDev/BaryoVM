// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package ghrunner installs and removes a GitHub Actions self-hosted runner on a VM over SSH.
//
// The scripts are built by pure functions (scripts.go) and the decisions live in Install and
// Remove, which talk to a Remote and a GitHub interface so tests drive them with fakes. Nothing
// here prints, shells out locally, or imports internal/cli.
//
// The registration and removal tokens are the one secret in play. They are fetched through the
// GitHub interface just before use, travel to the VM on stdin, and are scrubbed from every error
// this package returns.
package ghrunner

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	repoRe    = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	nameRe    = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
	labelRe   = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
	versionRe = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	memoryRe  = regexp.MustCompile(`^[1-9][0-9]*[KMGT]?$`)
	cpuRe     = regexp.MustCompile(`^[1-9][0-9]*%$`)
	sha256Re  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

const (
	DefaultMemoryMax = "4G"
	DefaultCPUQuota  = "200%"
)

// Options is everything the user chose. Validate fills the defaults and refuses anything that
// should not reach a remote shell; the scripts still quote every value on top of that.
type Options struct {
	VM        string
	Repo      string // owner/name
	Name      string
	Labels    []string
	Version   string // empty means the latest release
	MemoryMax string
	CPUQuota  string
}

// Validate applies defaults and checks every field. Call it before anything else.
func (o *Options) Validate() error {
	if !repoRe.MatchString(o.Repo) || strings.Contains(o.Repo, "..") {
		return fmt.Errorf("--repo %q must look like owner/name with letters, digits, dot, dash or underscore only", o.Repo)
	}
	if o.Name == "" {
		o.Name = o.VM + "-" + o.Repo[strings.Index(o.Repo, "/")+1:]
	}
	if !nameRe.MatchString(o.Name) {
		return fmt.Errorf("runner name %q is not valid: pass --name with 1 to 64 letters, digits, dot, dash or underscore", o.Name)
	}
	for _, l := range o.Labels {
		if !labelRe.MatchString(l) {
			return fmt.Errorf("--labels entry %q is not valid: use 1 to 64 letters, digits, dot, dash or underscore each", l)
		}
	}
	if o.Version != "" && !versionRe.MatchString(o.Version) {
		return fmt.Errorf("--version %q must look like 2.320.0: leave it out for the latest release", o.Version)
	}
	if o.MemoryMax == "" {
		o.MemoryMax = DefaultMemoryMax
	}
	if !memoryRe.MatchString(o.MemoryMax) {
		return fmt.Errorf("--memory-max %q must be a number with an optional K, M, G or T suffix, such as 4G", o.MemoryMax)
	}
	if o.CPUQuota == "" {
		o.CPUQuota = DefaultCPUQuota
	}
	if !cpuRe.MatchString(o.CPUQuota) {
		return fmt.Errorf("--cpu-quota %q must be a whole percentage such as 200%%", o.CPUQuota)
	}
	return nil
}

// Arch maps `uname -m` to the runner release's arch name. An unknown one is refused rather than
// guessed, since the wrong tarball unpacks fine and fails at run time.
func Arch(uname string) (string, error) {
	switch strings.TrimSpace(uname) {
	case "aarch64", "arm64":
		return "arm64", nil
	case "x86_64":
		return "x64", nil
	}
	return "", fmt.Errorf("unsupported CPU architecture %q: the runner is published for x86_64 and aarch64 only", strings.TrimSpace(uname))
}

// ExtraLabels is Labels without the ones the runner adds itself (self-hosted, Linux, the arch
// label), and without duplicates. config.sh would tolerate them, but the registration then lists
// them twice in the GitHub UI.
func (o Options) ExtraLabels(arch string) []string {
	builtin := map[string]bool{"self-hosted": true, "linux": true}
	builtin[strings.ToLower(arch)] = true
	seen := map[string]bool{}
	var out []string
	for _, l := range o.Labels {
		k := strings.ToLower(l)
		if builtin[k] || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, l)
	}
	return out
}

// Redact replaces every secret in s with [redacted]. Empty secrets are skipped, since replacing
// the empty string would shred the text.
func Redact(s string, secrets ...string) string {
	for _, sec := range secrets {
		if sec != "" {
			s = strings.ReplaceAll(s, sec, "[redacted]")
		}
	}
	return s
}

// Remote is one live connection to the VM.
type Remote interface {
	Run(cmd string) (string, error)
	RunInput(cmd string, stdin string) (string, error)
	Close() error
}

// Dial opens the connection. It is called at most once, and only after the repo check passed.
type Dial func() (Remote, error)

// Asset is one downloadable file of a runner release.
type Asset struct {
	Name   string
	Digest string // "sha256:<hex>", empty when GitHub did not report one
}

// Release is a runner release as GitHub reports it.
type Release struct {
	Version string // without the leading v
	Assets  []Asset
}

// GitHub is the local side: the gh CLI in production. Tokens come back as plain strings that this
// package never stores.
type GitHub interface {
	// IsPrivate reports whether the repo is private.
	IsPrivate(repo string) (bool, error)
	// Release fetches the latest runner release, or the given version when it is not empty.
	Release(version string) (Release, error)
	RegistrationToken(repo string) (string, error)
	RemoveToken(repo string) (string, error)
}

// Report is what happened, for the JSON envelope.
type Report struct {
	VM               string `json:"vm"`
	Repo             string `json:"repo"`
	Name             string `json:"name"`
	Dir              string `json:"dir"`
	Version          string `json:"version,omitempty"`
	Arch             string `json:"arch,omitempty"`
	AlreadyReady     bool   `json:"alreadyReady,omitempty"`
	Installed        bool   `json:"installed,omitempty"`
	Configured       bool   `json:"configured,omitempty"`
	ServiceInstalled bool   `json:"serviceInstalled,omitempty"`
	Started          bool   `json:"started,omitempty"`
	// Removed is set by Remove. LocalOnly means the GitHub entry still exists and has to be deleted
	// by hand, because no remove token could be fetched.
	Removed   bool `json:"removed,omitempty"`
	LocalOnly bool `json:"localOnly,omitempty"`
}

// State is what the probe found.
type State struct {
	Uname      string
	Binary     bool
	Configured bool
	Unit       string
	Active     bool
}

// ParseProbe reads BuildProbe's output.
func ParseProbe(out string) State {
	var s State
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "arch":
			s.Uname = v
		case "binary":
			s.Binary = v == "1"
		case "configured":
			s.Configured = v == "1"
		case "unit":
			s.Unit = v
		case "active":
			s.Active = v == "1"
		}
	}
	return s
}

// Install brings the runner to configured, installed as a service and active, doing only the
// steps that are missing, so a rerun after a crash picks up where it stopped.
func Install(o Options, gh GitHub, dial Dial) (rep Report, err error) {
	if err := o.Validate(); err != nil {
		return Report{}, err
	}
	rep = Report{VM: o.VM, Repo: o.Repo, Name: o.Name, Dir: o.Dir()}

	// Before any dial: a public repo's fork PRs would run their code on this VM.
	private, err := gh.IsPrivate(o.Repo)
	if err != nil {
		return rep, err
	}
	if !private {
		return rep, fmt.Errorf("%s is a public repo: refusing to install a self-hosted runner, since a fork pull request could run its code on %s", o.Repo, o.VM)
	}

	r, err := dial()
	if err != nil {
		return rep, err
	}
	defer r.Close()

	var token string
	// Every error leaving this function is scrubbed of the token, whichever step made it.
	defer func() {
		if err != nil {
			err = fmt.Errorf("%s", Redact(err.Error(), token))
		}
	}()

	out, err := r.Run(BuildProbe(o))
	if err != nil {
		return rep, err
	}
	st := ParseProbe(out)
	arch, err := Arch(st.Uname)
	if err != nil {
		return rep, err
	}
	rep.Arch = arch

	if st.Binary && st.Configured && st.Unit != "" && st.Active {
		rep.AlreadyReady = true
		return rep, nil
	}
	if st.Binary && st.Configured && st.Unit != "" {
		// Registered and installed, just stopped.
		if _, err = r.Run(BuildStart(o)); err != nil {
			return rep, err
		}
		rep.Started = true
		return rep, nil
	}

	if !st.Binary {
		rel, err2 := gh.Release(o.Version)
		if err2 != nil {
			return rep, err2
		}
		o.Version = rel.Version
		if !versionRe.MatchString(o.Version) {
			return rep, fmt.Errorf("runner release version %q is not x.y.z: pass --version", rel.Version)
		}
		rep.Version = o.Version
		want := fmt.Sprintf("actions-runner-linux-%s-%s.tar.gz", arch, o.Version)
		sum, err2 := digestFor(rel, want)
		if err2 != nil {
			return rep, err2
		}
		if _, err = r.Run(BuildInstall(o, arch, sum)); err != nil {
			return rep, err
		}
		rep.Installed = true
	}

	if !st.Configured {
		token, err = gh.RegistrationToken(o.Repo)
		if err != nil {
			return rep, err
		}
		if _, err = r.RunInput(BuildConfig(o, arch), token+"\n"); err != nil {
			return rep, err
		}
		rep.Configured = true
	}

	rep.ServiceInstalled = st.Unit == ""
	if _, err = r.Run(BuildService(o)); err != nil {
		return rep, err
	}
	rep.Started = true
	return rep, nil
}

func digestFor(rel Release, name string) (string, error) {
	for _, a := range rel.Assets {
		if a.Name != name {
			continue
		}
		hex := strings.TrimPrefix(a.Digest, "sha256:")
		if a.Digest == "" || !sha256Re.MatchString(hex) {
			return "", fmt.Errorf("release asset %s has no usable sha256 digest: refusing to install an unverified runner, try another --version", name)
		}
		return hex, nil
	}
	return "", fmt.Errorf("release v%s has no asset %s: check --version", rel.Version, name)
}

// Remove takes the runner off the VM and out of GitHub. If no remove token can be had it still
// removes the local runner and says so in the report, so the caller can tell the user to delete the
// GitHub entry by hand.
func Remove(o Options, gh GitHub, dial Dial) (rep Report, err error) {
	if err := o.Validate(); err != nil {
		return Report{}, err
	}
	rep = Report{VM: o.VM, Repo: o.Repo, Name: o.Name, Dir: o.Dir()}

	r, err := dial()
	if err != nil {
		return rep, err
	}
	defer r.Close()

	var token string
	defer func() {
		if err != nil {
			err = fmt.Errorf("%s", Redact(err.Error(), token))
		}
	}()

	out, err := r.Run(BuildProbe(o))
	if err != nil {
		return rep, err
	}
	st := ParseProbe(out)
	if !st.Binary && !st.Configured && st.Unit == "" {
		return rep, fmt.Errorf("no runner for %s on %s: nothing to remove", o.Repo, o.VM)
	}

	if _, err = r.Run(BuildRemoveStop(o)); err != nil {
		return rep, err
	}

	if st.Configured {
		token, err = gh.RemoveToken(o.Repo)
		if err == nil {
			if _, err = r.RunInput(BuildRemoveConfig(o, false), token+"\n"); err != nil {
				return rep, err
			}
		} else {
			token, err = "", nil
			if _, err = r.Run(BuildRemoveConfig(o, true)); err != nil {
				return rep, err
			}
			rep.LocalOnly = true
		}
	}

	if _, err = r.Run(BuildRemoveDelete(o)); err != nil {
		return rep, err
	}
	rep.Removed = true
	return rep, nil
}
