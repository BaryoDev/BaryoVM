// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package repoinit

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Options is one init run.
type Options struct {
	Repo     Repo
	Template *Template
	Dir      string // the local checkout the files are written into
	// CodeOwner is the CODEOWNERS handle, @owner or @org/team. Default @<owner>.
	CodeOwner string
	// Branch is the branch to protect. Empty means the repository's default branch.
	Branch string
	// Checks replaces the template's required checks when not nil.
	Checks    []string
	Approvals int
	Force     bool
	// ReplaceProtection overwrites protection that already exists on the branch. Without it an
	// existing protection is left alone: the API call replaces the whole rule, so a rerun would
	// otherwise reset what someone hardened by hand (admins, restrictions, signatures).
	ReplaceProtection bool
}

// File statuses in a Report.
const (
	FileCreate    = "create"
	FileOverwrite = "overwrite"
	FileExists    = "exists"
	FileRefused   = "refused" // the path leaves the checkout, e.g. through a symlinked folder
)

// FileAction is one file init writes or leaves alone.
type FileAction struct {
	Path    string `json:"path"`
	Action  string `json:"action"` // create, overwrite, exists (left alone, pass --force to replace), or refused
	Written bool   `json:"written"`
	Reason  string `json:"reason,omitempty"`
}

// ProtectionPlan is the branch protection init applies.
type ProtectionPlan struct {
	Branch  string `json:"branch"` // empty in a plan when it is the default branch, not yet looked up
	Default bool   `json:"defaultBranch"`
	Protection
	Applied bool `json:"applied"`
	// Note says why nothing was applied, e.g. protection that already exists.
	Note string `json:"note,omitempty"`
}

// TrustedPublishing is what to enter on the registry. BaryoVM never enters it.
type TrustedPublishing struct {
	Registry    string   `json:"registry"`
	URL         string   `json:"url"`
	Owner       string   `json:"owner"`
	Repository  string   `json:"repository"`
	Workflow    string   `json:"workflow"`
	Environment string   `json:"environment"` // empty means leave the field blank
	Notes       []string `json:"notes"`
}

// Report is the Data of repo init.
type Report struct {
	DryRun            bool              `json:"dryRun"`
	Repo              string            `json:"repo"`
	Template          string            `json:"template"`
	Source            string            `json:"source"`
	Dir               string            `json:"dir"`
	Files             []FileAction      `json:"files"`
	Protection        ProtectionPlan    `json:"protection"`
	TrustedPublishing TrustedPublishing `json:"trustedPublishing"`
	NextSteps         []string          `json:"nextSteps"`
}

// Values is the registry entry for a template and a repository.
func Values(t *Template, r Repo) TrustedPublishing {
	tp := TrustedPublishing{
		Registry: t.Manifest.Registry, URL: t.Manifest.RegistryURL,
		Owner: r.Owner, Repository: r.Name,
		Workflow: t.Manifest.Workflow, Environment: t.Manifest.Environment,
		Notes: append([]string{}, t.Manifest.Notes...),
	}
	for _, n := range tp.Notes {
		if n == TagNote {
			return tp
		}
	}
	// Whoever can push a v* tag can publish, so a custom template that forgets to say so is told.
	tp.Notes = append(tp.Notes, TagNote)
	return tp
}

// TagNote is printed with every set of registry values.
const TagNote = "Pushing a v* tag is the publish right: protect v* tags with a repository ruleset, or limit who can push tags."

// NextSteps is the order that works. It is written into the output because the order is the one
// thing that is easy to get wrong: a policy created before the workflow is on the default branch
// names a file that is not there, and nothing complains until the first publish.
func NextSteps(t *Template, r Repo) []string {
	return []string{
		"commit and push the new files to the default branch, so " + t.Manifest.Workflow + " exists on GitHub",
		"create the trusted publishing policy on " + t.Manifest.Registry + " with the values above, once, by hand: BaryoVM cannot do this part",
		"run `baryovm repo verify " + r.String() + " --template " + t.Name + "` to see whether the policy took",
	}
}

// Plan works out what init would do and touches nothing.
func Plan(o Options) (*Report, map[string][]byte, error) {
	if o.Template == nil {
		return nil, nil, errors.New("no template")
	}
	if o.Dir == "" {
		return nil, nil, errors.New("no directory to write into")
	}
	st, err := os.Stat(o.Dir)
	if err != nil || !st.IsDir() {
		return nil, nil, fmt.Errorf("--path %s is not a directory: clone the repository first", o.Dir)
	}
	owner := o.CodeOwner
	if owner == "" {
		owner = "@" + o.Repo.Owner
	}
	if !strings.HasPrefix(owner, "@") {
		return nil, nil, fmt.Errorf("--codeowner %q must start with @ (a user, or @org/team)", owner)
	}
	for _, c := range owner {
		if c <= 0x20 || c == 0x7f {
			return nil, nil, errors.New("--codeowner must be one handle with no spaces or control characters: a line break would add CODEOWNERS lines")
		}
	}
	root, err := os.OpenRoot(o.Dir)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = root.Close() }()
	files := o.Template.Render(Vars{Owner: o.Repo.Owner, Repo: o.Repo.Name, CodeOwner: owner})
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	rep := &Report{
		DryRun: true, Repo: o.Repo.String(), Template: o.Template.Name, Source: o.Template.Source,
		Dir: filepath.Clean(o.Dir), Files: []FileAction{},
	}
	for _, p := range paths {
		a := FileAction{Path: p, Action: FileCreate}
		// Through the root, so a symlinked parent that leaves the checkout is refused here, and a
		// dry run shows it, instead of a file landing somewhere else.
		fi, err := root.Lstat(filepath.FromSlash(p))
		switch {
		case err != nil && !errors.Is(err, fs.ErrNotExist):
			a.Action, a.Reason = FileRefused, "the path leaves the checkout (a symlinked folder?): "+err.Error()
		case err == nil && fi.IsDir():
			return nil, nil, fmt.Errorf("%s is a directory: move it away, init writes a file there", p)
		case err == nil && o.Force:
			a.Action = FileOverwrite
		case err == nil:
			a.Action = FileExists
		}
		rep.Files = append(rep.Files, a)
	}
	checks := o.Checks
	if checks == nil {
		checks = o.Template.Manifest.RequiredChecks
	}
	rep.Protection = ProtectionPlan{
		Branch: o.Branch, Default: o.Branch == "",
		Protection: Protection{Checks: append([]string{}, checks...), Strict: true, CodeOwner: true, Approvals: o.Approvals},
	}
	rep.TrustedPublishing = Values(o.Template, o.Repo)
	rep.NextSteps = NextSteps(o.Template, o.Repo)
	return rep, files, nil
}

// Run does init. With apply false it is Plan. With apply true it writes the files, never
// replacing one that exists unless Force is set, then applies branch protection. The files go
// first: a failure to protect leaves them written, and a rerun skips them and retries the call.
func Run(ctx context.Context, o Options, gh GitHub, apply bool) (*Report, error) {
	rep, files, err := Plan(o)
	if err != nil {
		return nil, err
	}
	if !apply {
		return rep, nil
	}
	for _, f := range rep.Files {
		if f.Action == FileRefused {
			return rep, fmt.Errorf("%s: %s: nothing was written", f.Path, f.Reason)
		}
	}
	rep.DryRun = false
	root, err := os.OpenRoot(o.Dir)
	if err != nil {
		return rep, err
	}
	defer func() { _ = root.Close() }()
	for i := range rep.Files {
		f := &rep.Files[i]
		if f.Action == FileExists {
			continue
		}
		if err := writeFile(root, f.Path, files[f.Path], o.Force); err != nil {
			return rep, fmt.Errorf("write %s: %w", f.Path, err)
		}
		f.Written = true
	}
	branch := o.Branch
	if branch == "" {
		branch, err = gh.DefaultBranch(ctx, o.Repo)
		if err != nil {
			return rep, fmt.Errorf("find the default branch: %w (the files are written; rerun to retry, existing files are skipped)", err)
		}
	}
	rep.Protection.Branch = branch
	if !o.ReplaceProtection {
		exists, err := gh.HasProtection(ctx, o.Repo, branch)
		if err != nil {
			return rep, fmt.Errorf("read the protection of %s: %w (the files are written; rerun to retry, existing files are skipped)", branch, err)
		}
		if exists {
			rep.Protection.Note = "protection exists, not changed: pass --replace-protection to replace it with the template's"
			return rep, nil
		}
	}
	if err := gh.ProtectBranch(ctx, o.Repo, branch, rep.Protection.Protection); err != nil {
		return rep, fmt.Errorf("apply branch protection to %s: %w (the files are written; rerun to retry, existing files are skipped)", branch, err)
	}
	rep.Protection.Applied = true
	return rep, nil
}

// writeFile writes one file inside root, which refuses any path that leaves the checkout. Without
// force it opens with O_EXCL, so a file that appeared since the plan is never replaced. A symlink
// at the path is refused either way.
func writeFile(root *os.Root, rel string, data []byte, force bool) error {
	full := filepath.FromSlash(rel)
	if d := filepath.Dir(full); d != "." {
		if err := root.MkdirAll(d, 0o755); err != nil { // #nosec G301 -- a repository directory
			return err
		}
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if force {
		if fi, err := root.Lstat(full); err == nil {
			if fi.Mode()&os.ModeSymlink != 0 {
				return errors.New("is a symlink: refusing to write through it")
			}
			flags = os.O_WRONLY | os.O_TRUNC
		}
	}
	f, err := root.OpenFile(full, flags, 0o644)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return errors.New("appeared since the plan: not overwritten")
		}
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
