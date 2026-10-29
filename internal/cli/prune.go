// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"fmt"

	"github.com/BaryoDev/BaryoVM/internal/prune"
	"github.com/BaryoDev/BaryoVM/internal/ui"
	"github.com/spf13/cobra"
)

func newVMPruneCmd() *cobra.Command {
	var apply bool
	var floor int
	var workDirs, worktreeDirs []string
	var stoppedDays, keepImages int
	cmd := &cobra.Command{
		Use:   "prune <name>",
		Short: "Free disk space on a VM, planning first",
		Long: "Lists what could be deleted (item, size, reason) and deletes nothing. Add\n" +
			"--apply to run the plan; it prints the space freed and df before and after.\n\n" +
			"It plans: Docker build cache, dangling images, anonymous unused volumes,\n" +
			"stopped containers older than --stopped-days, rollback images beyond the\n" +
			"newest --keep-images per repository, bin/obj/TestResults beside a .csproj or\n" +
			".fsproj under --work-dir, clean git worktrees under --worktree-dir whose\n" +
			"branch has nothing off the remote, and npm, go-build, pip, playwright and\n" +
			"older editor server caches.\n\n" +
			"It never plans a named volume, an image any container uses, a bin/ without a\n" +
			"project file beside it, or a dirty worktree. --floor <GB> exits 0 with no\n" +
			"changes while free space is at or above it, so a cron job stays quiet.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vm, c, err := dialVM(args[0], "vm prune")
			if err != nil {
				return err
			}
			defer c.Close()

			action := "vm prune"
			if !apply {
				action = "vm prune (plan)"
			}
			fail := func(err error) error {
				ui.Emit(ui.Result{OK: false, Action: action, Error: err.Error()})
				return err
			}

			opt := prune.Options{FloorGB: floor, WorkDirs: workDirs, WorktreeDirs: worktreeDirs, StoppedAgeDays: stoppedDays, KeepImages: keepImages}
			var plan prune.Plan
			if err := ui.Step("planning the prune on "+vm.Name, func() error {
				var perr error
				plan, perr = prune.PlanFor(c, opt)
				return perr
			}); err != nil {
				return fail(err)
			}

			if plan.AboveFloor {
				msg := fmt.Sprintf("free space is %s, at or above the %d GB floor: nothing to do", humanBytes(plan.Disk.FreeBytes), floor)
				ui.Emit(ui.Result{OK: true, Action: action, Message: msg, Data: plan})
				if !ui.JSON() {
					ui.Successf("%s", msg)
				}
				return nil
			}

			if !apply {
				if ui.JSON() {
					ui.Emit(ui.Result{OK: true, Action: action, Data: plan})
					return nil
				}
				ui.Title(fmt.Sprintf("Prune plan for %s", vm.Name))
				ui.Detail("free", humanBytes(plan.Disk.FreeBytes))
				for _, it := range plan.Items {
					ui.Detail(fmt.Sprintf("%s  %s", it.Kind, it.Target), fmt.Sprintf("%s: %s", humanBytes(it.Bytes), it.Reason))
				}
				for _, n := range plan.Notes {
					ui.Warnf("%s", n)
				}
				ui.Detail("could free", humanBytes(plan.TotalBytes))
				ui.Warnf("plan only: nothing was deleted. Run again with --apply to delete.")
				return nil
			}

			var res prune.Result
			if err := ui.Step("pruning "+vm.Name, func() error {
				var aerr error
				res, aerr = prune.Apply(c, plan)
				return aerr
			}); err != nil {
				return fail(err)
			}
			if ui.JSON() {
				ui.Emit(ui.Result{OK: len(res.Failed) == 0, Action: action, Data: res})
			} else {
				ui.Title(fmt.Sprintf("Pruned %s", vm.Name))
				ui.Detail("df before", fmt.Sprintf("%s used, %s free", humanBytes(res.Before.UsedBytes), humanBytes(res.Before.FreeBytes)))
				ui.Detail("df after", fmt.Sprintf("%s used, %s free", humanBytes(res.After.UsedBytes), humanBytes(res.After.FreeBytes)))
				ui.Detail("freed", humanBytes(res.FreedBytes))
				for _, f := range res.Failed {
					ui.Errorf("%s %s: %s", f.Item.Kind, f.Item.Target, f.Error)
				}
				for _, f := range res.Skipped {
					ui.Detail("skipped", fmt.Sprintf("%s %s (%s)", f.Item.Kind, f.Item.Target, f.Error))
				}
			}
			if len(res.Failed) > 0 {
				return fmt.Errorf("%d item(s) did not delete: see the list above, fix them and run vm prune again", len(res.Failed))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false, "run the plan (the default only lists it)")
	cmd.Flags().IntVar(&floor, "floor", 0, "do nothing while free space is at or above this many GB")
	cmd.Flags().StringSliceVar(&workDirs, "work-dir", nil, "folder searched for build output beside a project file (repeatable)")
	cmd.Flags().StringSliceVar(&worktreeDirs, "worktree-dir", nil, "folder searched for git worktrees (repeatable)")
	cmd.Flags().IntVar(&stoppedDays, "stopped-days", 14, "plan stopped containers older than this many days")
	cmd.Flags().IntVar(&keepImages, "keep-images", 2, "rollback images kept per repository")
	return cmd
}

func humanBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}
