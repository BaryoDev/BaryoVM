// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/BaryoDev/BaryoVM/internal/repoinit"
	"github.com/BaryoDev/BaryoVM/internal/ui"
	"github.com/spf13/cobra"
)

// repoGitHub builds the GitHub client from a token. A variable so tests hand back a fake and no
// test needs a network.
var repoGitHub = func(token string) repoinit.GitHub { return &repoinit.API{Token: token} }

// repoToken resolves the GitHub token. With a reference it uses that one. Without, it tries
// GITHUB_TOKEN, GH_TOKEN, then the sign-in of the gh CLI. The token never goes into an error or
// the envelope. A variable so a test can fake it.
var repoToken = func(ref string) (string, error) {
	r := cmdResolver()
	if ref != "" {
		return r.Resolve(ref)
	}
	for _, c := range []string{"env:GITHUB_TOKEN", "env:GH_TOKEN", "cmd:gh auth token"} {
		if v, err := r.Resolve(c); err == nil {
			return v, nil
		}
	}
	return "", fmt.Errorf("no GitHub token: set GITHUB_TOKEN, sign in with `gh auth login`, or pass --token-ref env:NAME, file:PATH or cmd:<command>")
}

// repoSleep is the wait between polls of verify. A variable so tests do not wait.
var repoSleep = repoinit.RealSleep

func newRepoInitCmd() *cobra.Command {
	var (
		templateName, templatesDir, path, codeowner, branch, tokenRef string
		checks                                                        []string
		approvals                                                     int
		force, yes, dryRun                                            bool
	)
	cmd := &cobra.Command{
		Use:   "init <owner/repo>",
		Short: "Scaffold release CI from a template and protect the default branch",
		Long: strings.TrimSpace(`
Writes the release workflows and the repository furniture (CODEOWNERS, issue templates,
CONTRIBUTING) from a template into a local checkout, applies branch protection with the template's
required checks through the GitHub API, and prints the values to enter on the registry for
trusted publishing. Without --yes (or with --dry-run) it prints that plan and does nothing: no
file written, no GitHub call, no token read.

Built-in templates: dotnet-nuget (nuget.org) and node-npm (npmjs.com). --templates-dir names a
directory whose subdirectories are templates of your own (template.json plus a files/ tree); one
with the same name as a built-in replaces it. Text in the files may use @@OWNER@@, @@REPO@@,
@@FULL_NAME@@ and @@CODEOWNER@@.

A file that already exists is never overwritten without --force: it is listed as "exists" and left
alone, so a rerun after a half finished first run is safe.

Branch protection requires the template's checks (--checks replaces them), an up to date branch,
and a code owner review (--approvals sets the number of approving reviews, default 0). Admins can
still push. It uses the token from --token-ref, else GITHUB_TOKEN, GH_TOKEN, or gh auth token.

BaryoVM does not create the trusted publishing policy. nuget.org and npm create it through their
web pages and have no API for it, so it is yours to do once, by hand. The order that works:

  1. init writes the workflow and protects the branch
  2. commit and push the files, so the workflow exists on the default branch
  3. create the policy on the registry with the values init printed
  4. baryovm repo verify <owner/repo> --template <name> says whether it took

Creating the policy before step 2 names a workflow file that is not there yet, and nothing
complains until the first publish.
`),
		Example: "  baryovm repo init acme/widgets --template dotnet-nuget\n" +
			"  baryovm repo init acme/widgets --template node-npm --path ~/src/widgets --yes",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			err := runRepoInit(args[0], templateName, templatesDir, path, codeowner, branch, tokenRef, checks, approvals, force, yes && !dryRun)
			if err != nil {
				ui.Emit(ui.Result{OK: false, Action: "repo init", Error: err.Error()})
			}
			return err
		},
	}
	cmd.Flags().StringVar(&templateName, "template", "", "template name: dotnet-nuget, node-npm, or one from --templates-dir (required)")
	cmd.Flags().StringVar(&templatesDir, "templates-dir", "", "a directory of your own templates; one named like a built-in replaces it")
	cmd.Flags().StringVar(&path, "path", ".", "the local checkout to write into")
	cmd.Flags().StringVar(&codeowner, "codeowner", "", "CODEOWNERS handle, @user or @org/team (default @<owner>)")
	cmd.Flags().StringVar(&branch, "branch", "", "the branch to protect (default the repository's default branch)")
	cmd.Flags().StringSliceVar(&checks, "checks", nil, "required status checks, replacing the template's (repeatable)")
	cmd.Flags().IntVar(&approvals, "approvals", 0, "approving reviews required, besides the code owner review")
	cmd.Flags().StringVar(&tokenRef, "token-ref", "", "GitHub token reference: env:NAME, file:PATH or cmd:<command> (default GITHUB_TOKEN, GH_TOKEN, gh auth token)")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite files that already exist")
	cmd.Flags().BoolVar(&yes, "yes", false, "really write the files and call GitHub; without it the plan is printed and nothing is done")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the plan and do nothing (this is already the default without --yes)")
	_ = cmd.MarkFlagRequired("template")
	return cmd
}

func runRepoInit(repoArg, templateName, templatesDir, path, codeowner, branch, tokenRef string, checks []string, approvals int, force, apply bool) error {
	repo, err := repoinit.ParseRepo(repoArg)
	if err != nil {
		return err
	}
	tpl, err := repoinit.Load(templateName, templatesDir)
	if err != nil {
		return err
	}
	if approvals < 0 || approvals > 6 {
		return fmt.Errorf("--approvals %d: GitHub accepts 0 to 6", approvals)
	}
	if tokenRef != "" {
		if err := cmdResolver().Check(tokenRef); err != nil {
			return fmt.Errorf("--token-ref: %w", err)
		}
	}
	o := repoinit.Options{
		Repo: repo, Template: tpl, Dir: path, CodeOwner: codeowner, Branch: branch,
		Checks: checks, Approvals: approvals, Force: force,
	}
	var gh repoinit.GitHub
	if apply {
		token, err := repoToken(tokenRef)
		if err != nil {
			return err
		}
		gh = repoGitHub(token)
	}
	// Each call inside has its own timeout. This outer one only bounds a stuck sequence of them.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var rep *repoinit.Report
	if apply {
		err = ui.Step("scaffolding "+repo.String()+" from "+tpl.Name, func() error {
			var e error
			rep, e = repoinit.Run(ctx, o, gh, true)
			return e
		})
	} else {
		rep, err = repoinit.Run(ctx, o, nil, false)
	}
	if err != nil {
		emitDataFailure("repo init", rep, err)
		return err
	}
	if !ui.JSON() {
		printRepoInit(rep)
	}
	msg := "plan only: nothing was done, rerun with --yes to write the files and protect the branch"
	if apply {
		msg = fmt.Sprintf("scaffolded %s from %s: the registry policy is still yours to create", repo, tpl.Name)
	}
	ui.Emit(ui.Result{OK: true, Action: "repo init", Message: msg, Data: rep})
	return nil
}

func printRepoInit(r *repoinit.Report) {
	if r.DryRun {
		ui.Title("repo init plan (nothing is done without --yes)")
	}
	for _, f := range r.Files {
		verb := f.Action
		switch {
		case f.Written:
			verb = "wrote"
		case f.Action == repoinit.FileExists:
			verb = "exists, left alone"
		case f.Action == repoinit.FileCreate:
			verb = "would create"
		case f.Action == repoinit.FileOverwrite:
			verb = "would overwrite"
		}
		ui.Detail(verb, f.Path)
	}
	b := r.Protection.Branch
	if r.Protection.Default && b == "" {
		b = "the default branch"
	}
	state := "would protect"
	if r.Protection.Applied {
		state = "protected"
	}
	ui.Detail(state, fmt.Sprintf("%s: required checks %s, up to date branch, code owner review, %d approving reviews", b, strings.Join(r.Protection.Checks, ", "), r.Protection.Approvals))
	printTrustedValues(r.TrustedPublishing)
	for i, s := range r.NextSteps {
		ui.Detail(fmt.Sprintf("step %d", i+1), s)
	}
}

func printTrustedValues(v repoinit.TrustedPublishing) {
	ui.Title("enter these on " + v.Registry + " (BaryoVM does not do this part)")
	ui.Detail("owner", v.Owner)
	ui.Detail("repository", v.Repository)
	ui.Detail("workflow file", v.Workflow)
	env := v.Environment
	if env == "" {
		env = "(leave blank)"
	}
	ui.Detail("environment", env)
	if v.URL != "" {
		ui.Detail("where", v.URL)
	}
	for _, n := range v.Notes {
		ui.Notef("%s", n)
	}
}

func newRepoVerifyCmd() *cobra.Command {
	var (
		templateName, templatesDir, workflow, ref, tokenRef string
		steps                                               []string
		timeout                                             time.Duration
	)
	cmd := &cobra.Command{
		Use:   "verify <owner/repo>",
		Short: "Run the publish workflow's dry run and report whether trusted publishing works",
		Long: strings.TrimSpace(`
Starts the publish workflow by hand with its dry run switch on, waits for the run to finish, and
reports whether the OIDC exchange step succeeded. The dry run publishes nothing: the exchange only
asks the registry for a short-lived credential, which is exactly what fails when the trusted
publishing policy is missing or does not match.

The workflow must be on the default branch (or --ref) first. The workflow file and the step names
come from --template; --workflow and --step override them for a workflow of your own. The verdict
is "took" when every OIDC step succeeded, "did-not-take" when one failed (the four values to check
are printed again), and "inconclusive" when the run ended before reaching the step or did not
finish within --timeout. Only "took" exits zero.

It uses the token from --token-ref, else GITHUB_TOKEN, GH_TOKEN, or gh auth token. The token needs
the actions write permission on the repository.
`),
		Example: "  baryovm repo verify acme/widgets --template dotnet-nuget\n" +
			"  baryovm repo verify acme/widgets --workflow release.yml --step 'Get a NuGet key' -o json",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			err := runRepoVerify(args[0], templateName, templatesDir, workflow, ref, tokenRef, steps, timeout)
			if err != nil {
				ui.Emit(ui.Result{OK: false, Action: "repo verify", Error: err.Error()})
			}
			return err
		},
	}
	cmd.Flags().StringVar(&templateName, "template", "", "the template the repository was scaffolded from: gives the workflow file and step names")
	cmd.Flags().StringVar(&templatesDir, "templates-dir", "", "a directory of your own templates")
	cmd.Flags().StringVar(&workflow, "workflow", "", "the publish workflow file name (default from the template)")
	cmd.Flags().StringSliceVar(&steps, "step", nil, "an OIDC step name that must succeed (repeatable; default from the template)")
	cmd.Flags().StringVar(&ref, "ref", "", "the branch to run it on (default the repository's default branch)")
	cmd.Flags().StringVar(&tokenRef, "token-ref", "", "GitHub token reference: env:NAME, file:PATH or cmd:<command> (default GITHUB_TOKEN, GH_TOKEN, gh auth token)")
	cmd.Flags().DurationVar(&timeout, "timeout", 15*time.Minute, "how long to wait for the run to finish")
	return cmd
}

func runRepoVerify(repoArg, templateName, templatesDir, workflow, ref, tokenRef string, steps []string, timeout time.Duration) error {
	repo, err := repoinit.ParseRepo(repoArg)
	if err != nil {
		return err
	}
	o := repoinit.VerifyOptions{Repo: repo, Workflow: workflow, Steps: steps, Ref: ref, Timeout: timeout}
	if templateName != "" {
		tpl, err := repoinit.Load(templateName, templatesDir)
		if err != nil {
			return err
		}
		if o.Workflow == "" {
			o.Workflow = tpl.Manifest.Workflow
		}
		if len(o.Steps) == 0 {
			o.Steps = tpl.Manifest.OIDCSteps
		}
		o.Inputs = tpl.Manifest.DispatchInputs
		o.Values = repoinit.Values(tpl, repo)
	}
	if o.Workflow == "" || len(o.Steps) == 0 {
		return fmt.Errorf("say which workflow and step to read: pass --template, or --workflow with --step")
	}
	if o.Inputs == nil {
		o.Inputs = map[string]string{"dry_run": "true"}
	}
	if o.Values.Workflow == "" {
		o.Values = repoinit.TrustedPublishing{Owner: repo.Owner, Repository: repo.Name, Workflow: o.Workflow, Registry: "the registry"}
	}
	if tokenRef != "" {
		if err := cmdResolver().Check(tokenRef); err != nil {
			return fmt.Errorf("--token-ref: %w", err)
		}
	}
	token, err := repoToken(tokenRef)
	if err != nil {
		return err
	}
	if timeout <= 0 {
		return fmt.Errorf("--timeout must be positive")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout+2*time.Minute)
	defer cancel()
	var rep *repoinit.VerifyReport
	err = ui.Step("running the dry run of "+o.Workflow+" on "+repo.String(), func() error {
		var e error
		rep, e = repoinit.Verify(ctx, o, repoinit.VerifyDeps{GH: repoGitHub(token), Sleep: repoSleep})
		return e
	})
	if err != nil {
		emitDataFailure("repo verify", rep, err)
		return err
	}
	if !ui.JSON() {
		for _, s := range rep.Steps {
			ui.Detail(s.Conclusion, s.Name)
		}
		if rep.RunURL != "" {
			ui.Detail("run", rep.RunURL)
		}
		if rep.Verdict != repoinit.Took {
			printTrustedValues(rep.Values)
		}
	}
	if rep.Verdict == repoinit.Took {
		ui.Emit(ui.Result{OK: true, Action: "repo verify", Message: "trusted publishing took: the OIDC exchange succeeded on the dry run", Data: rep})
		return nil
	}
	err = fmt.Errorf("trusted publishing is not confirmed (%s): %s", rep.Verdict, rep.Reason)
	emitDataFailure("repo verify", rep, err)
	return err
}

// emitDataFailure puts a failed run's report in the JSON envelope. In human mode it prints nothing:
// the command's own error line carries the failure, so it is not said twice. A nil report is left
// to that line in both modes.
func emitDataFailure(action string, rep any, err error) {
	if !ui.JSON() {
		return
	}
	switch r := rep.(type) {
	case *repoinit.Report:
		if r == nil {
			return
		}
	case *repoinit.VerifyReport:
		if r == nil {
			return
		}
	}
	ui.Emit(ui.Result{OK: false, Action: action, Data: rep, Error: err.Error()})
}
