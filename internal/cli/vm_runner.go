// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/BaryoDev/BaryoVM/internal/ghrunner"
	"github.com/BaryoDev/BaryoVM/internal/sshx"
	"github.com/BaryoDev/BaryoVM/internal/ui"
	"github.com/spf13/cobra"
)

func newVMRunnerCmd() *cobra.Command {
	var o ghrunner.Options
	var labels string
	cmd := &cobra.Command{
		Use:   "runner <vm>",
		Short: "Install a GitHub Actions self-hosted runner on a VM",
		Long: strings.TrimSpace(`
Install a GitHub Actions self-hosted runner for one private repo on a registered VM, over SSH.
It is the one place BaryoVM leaves an agent behind on the VM: GitHub's own runner, outbound
connections only, and only when you ask for it.

A public or internal repo is refused, because a fork pull request could run its code on your VM.

The registration token is fetched locally with gh just before it is used and sent on stdin. It is
never stored, never on a command line and never in an error. The gh login needs admin rights on
the repo.

Rerunning is safe: a configured, running runner is left alone, a stopped one is started, and a
missing service is installed. The memory and CPU limits cover the runner and what it runs
directly. Containers a job starts through Docker run under dockerd and are not bounded by them.
`),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			o.VM = args[0]
			o.Labels = splitLabels(labels)
			o.ApplyLimits = cmd.Flags().Changed("memory-max") || cmd.Flags().Changed("cpu-quota")
			return runRunner("vm runner", o, false)
		},
	}
	cmd.Flags().StringVar(&o.Repo, "repo", "", "repo to serve, as owner/name (required)")
	cmd.Flags().StringVar(&o.Name, "name", "", "runner name (default <vm>-<repo>)")
	cmd.Flags().StringVar(&labels, "labels", "", "extra labels, comma separated, added to self-hosted, Linux and the arch")
	cmd.Flags().StringVar(&o.Version, "version", "", "runner version, such as 2.320.0 (default the latest release)")
	cmd.Flags().StringVar(&o.MemoryMax, "memory-max", ghrunner.DefaultMemoryMax, "systemd MemoryMax for the runner service")
	cmd.Flags().StringVar(&o.CPUQuota, "cpu-quota", ghrunner.DefaultCPUQuota, "systemd CPUQuota for the runner service")
	_ = cmd.MarkFlagRequired("repo")
	cmd.AddCommand(newVMRunnerRemoveCmd())
	return cmd
}

func newVMRunnerRemoveCmd() *cobra.Command {
	var o ghrunner.Options
	cmd := &cobra.Command{
		Use:   "remove <vm>",
		Short: "Take a GitHub Actions runner off a VM and out of GitHub",
		Long: strings.TrimSpace(`
Stop and uninstall the runner service, unregister it from GitHub with a remove token (fetched
locally, sent on stdin), and delete the runner directory. If no remove token can be fetched, or
GitHub rejects it, the local runner is still removed and the GitHub entry has to be deleted by
hand. If that local removal fails too, the command fails and the directory stays for another try.
`),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			o.VM = args[0]
			return runRunner("vm runner remove", o, true)
		},
	}
	cmd.Flags().StringVar(&o.Repo, "repo", "", "repo the runner serves, as owner/name (required)")
	cmd.Flags().StringVar(&o.Name, "name", "", "runner name (default <vm>-<repo>)")
	_ = cmd.MarkFlagRequired("repo")
	return cmd
}

func splitLabels(s string) []string {
	var out []string
	for _, l := range strings.Split(s, ",") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func runRunner(action string, o ghrunner.Options, remove bool) error {
	vm, err := requireVM(o.VM)
	if err == nil {
		err = requireLinux(&vm, action)
	}
	if err != nil {
		ui.Emit(ui.Result{OK: false, Action: action, Error: err.Error()})
		return err
	}
	dial := func() (ghrunner.Remote, error) { return sshx.Dial(vm.Target()) }

	var rep ghrunner.Report
	verb := "installing a runner on"
	if remove {
		verb = "removing the runner from"
	}
	err = ui.Step(fmt.Sprintf("%s %s", verb, vm.Name), func() error {
		var err error
		if remove {
			rep, err = ghrunner.Remove(o, ghCLI{}, dial)
		} else {
			rep, err = ghrunner.Install(o, ghCLI{}, dial)
		}
		return err
	})
	if err != nil {
		ui.Emit(ui.Result{OK: false, Action: action, Data: rep, Error: err.Error()})
		return err
	}

	msg := runnerMessage(rep)
	if remove && rep.LocalOnly && !ui.JSON() {
		ui.Warnf("no remove token: delete %s from the repo's Settings, Actions, Runners page by hand", rep.Name)
	}
	ui.Emit(ui.Result{OK: true, Action: action, Message: msg, Data: rep})
	return nil
}

func runnerMessage(r ghrunner.Report) string {
	switch {
	case r.Removed && r.LocalOnly:
		return fmt.Sprintf("removed runner %s from %s, the GitHub entry is still there", r.Name, r.VM)
	case r.Removed:
		return fmt.Sprintf("removed runner %s from %s", r.Name, r.VM)
	case r.AlreadyReady:
		return fmt.Sprintf("runner %s on %s was already running", r.Name, r.VM)
	}
	return fmt.Sprintf("runner %s is running on %s for %s", r.Name, r.VM, r.Repo)
}

// ghCLI is the real ghrunner.GitHub: every call is the gh binary with an argv slice, never a shell.
type ghCLI struct{}

func ghVisibilityArgs(repo string) []string {
	return []string{"api", "repos/" + repo, "--jq", ".visibility"}
}

// ghTokenArgs builds the call for a registration-token or remove-token.
func ghTokenArgs(repo, kind string) []string {
	return []string{"api", "-X", "POST", "repos/" + repo + "/actions/runners/" + kind, "--jq", ".token"}
}

func ghReleaseArgs(version string) []string {
	if version == "" {
		return []string{"api", "repos/actions/runner/releases/latest"}
	}
	return []string{"api", "repos/actions/runner/releases/tags/v" + version}
}

const ghTimeout = 60 * time.Second

var errGHTimeout = errors.New("gh timed out")

func runGH(args ...string) (string, string, error) {
	return runWithTimeout("gh", ghTimeout, args...)
}

func runWithTimeout(name string, d time.Duration, args ...string) (string, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	var out, errb bytes.Buffer
	c := exec.CommandContext(ctx, name, args...) // #nosec G204 -- callers pass fixed binary names
	c.Stdout = &out
	c.Stderr = &errb
	if err := c.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", "", errGHTimeout
		}
		if _, lookErr := exec.LookPath(name); lookErr != nil {
			return "", "", fmt.Errorf("the GitHub CLI is not installed: install gh and run `gh auth login`")
		}
		return out.String(), errb.String(), err
	}
	return out.String(), errb.String(), nil
}

// ghError turns a failed gh call into advice. A 403 on the token calls means the login lacks admin
// rights on the repo, which is the usual first-run failure.
func ghError(what, repo, stderr string, err error) error {
	s := strings.TrimSpace(stderr)
	switch {
	case errors.Is(err, errGHTimeout):
		return fmt.Errorf("%s: gh did not answer within %s: check your network and run `gh auth status`, then retry", what, ghTimeout)
	case strings.Contains(s, "HTTP 403") || strings.Contains(s, "Must have admin rights"):
		return fmt.Errorf("%s: gh is not allowed to do this on %s: the token needs repo admin, run `gh auth refresh -s repo` or use an account with admin rights", what, repo)
	case strings.Contains(s, "HTTP 404"):
		return fmt.Errorf("%s: gh cannot see %s: check the name and run `gh auth status`", what, repo)
	case strings.Contains(s, "gh auth login"):
		return fmt.Errorf("%s: gh is not logged in: run `gh auth login`", what)
	}
	if s == "" {
		return fmt.Errorf("%s: %w", what, err)
	}
	return fmt.Errorf("%s: %s", what, s)
}

func (ghCLI) Visibility(repo string) (string, error) {
	out, errs, err := runGH(ghVisibilityArgs(repo)...)
	if err != nil {
		return "", ghError("check the repo", repo, errs, err)
	}
	v := strings.TrimSpace(out)
	if v == "" {
		return "", fmt.Errorf("check the repo: gh returned no visibility for %s", repo)
	}
	return v, nil
}

func (ghCLI) token(repo, kind string) (string, error) {
	out, errs, err := runGH(ghTokenArgs(repo, kind)...)
	if err != nil {
		return "", ghError("fetch a "+kind, repo, errs, err)
	}
	t := strings.TrimSpace(out)
	if t == "" {
		return "", fmt.Errorf("fetch a %s: gh returned no token for %s", kind, repo)
	}
	return t, nil
}

func (g ghCLI) RegistrationToken(repo string) (string, error) {
	return g.token(repo, "registration-token")
}

func (g ghCLI) RemoveToken(repo string) (string, error) { return g.token(repo, "remove-token") }

func (ghCLI) Release(version string) (ghrunner.Release, error) {
	out, errs, err := runGH(ghReleaseArgs(version)...)
	if err != nil {
		return ghrunner.Release{}, ghError("look up the runner release", "actions/runner", errs, err)
	}
	return parseRunnerRelease(out)
}

func parseRunnerRelease(raw string) (ghrunner.Release, error) {
	var r struct {
		Tag    string `json:"tag_name"`
		Assets []struct {
			Name   string `json:"name"`
			Digest string `json:"digest"`
		} `json:"assets"`
	}
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return ghrunner.Release{}, fmt.Errorf("read the runner release: %w", err)
	}
	rel := ghrunner.Release{Version: strings.TrimPrefix(r.Tag, "v")}
	for _, a := range r.Assets {
		rel.Assets = append(rel.Assets, ghrunner.Asset{Name: a.Name, Digest: a.Digest})
	}
	return rel, nil
}
