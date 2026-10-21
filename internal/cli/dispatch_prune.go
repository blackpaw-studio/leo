package cli

import (
	"fmt"
	"io"
	"sort"
	"text/tabwriter"

	"github.com/blackpaw-studio/leo/internal/consult"
	"github.com/spf13/cobra"
)

// pruneWorktreesFn is the prune implementation, replaced in tests.
var pruneWorktreesFn = consult.PruneWorktrees

func newDispatchPruneCmd() *cobra.Command {
	var dryRun, asJSON bool
	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Remove the worktrees of finished dispatches",
		Long: `Reclaim disk held by the isolated worktrees of terminal dispatches.

Only dispatches that have ended and whose worktree is still on disk are
considered; running dispatches are never touched. A worktree is removed when
it is clean and either still at its base commit or has every commit reachable
from a ref other than its own leo/* branch. Dirty worktrees, worktrees with
unmerged commits, and anything that cannot be verified are kept. The leo/*
branch is deleted only when its tip is reachable from another ref.

Directories with no dispatch record (orphans) and dispatches whose repository
is gone are reported, never deleted.

Use --dry-run first: it lists every worktree with its action and size, and
the total reclaimable, without changing anything.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			run := func() (consult.PruneReport, error) {
				cfg, err := loadConfig()
				if err != nil {
					return consult.PruneReport{}, err
				}
				return pruneWorktreesFn(cfg.StatePath(), consult.PruneOptions{DryRun: dryRun})
			}
			if asJSON {
				return runJSON(cmd.OutOrStdout(), func() (any, error) {
					report, err := run()
					if err != nil {
						return nil, failJSON("prune_failed", err)
					}
					return report, nil
				})
			}
			report, err := run()
			if err != nil {
				return err
			}
			return printPruneReport(cmd.OutOrStdout(), report)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "list what would be removed, with sizes, and change nothing")
	cmd.Flags().BoolVar(&asJSON, "json", false, "output the report as one JSON object")
	return markJSON(cmd)
}

func printPruneReport(out io.Writer, report consult.PruneReport) error {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ACTION\tSIZE\tID\tBRANCH\tREASON")
	for _, it := range report.Items {
		action := it.Action
		if it.Removed {
			action = "removed"
		} else if it.Error != "" {
			action = "failed"
		}
		reason := it.Reason
		if it.Error != "" {
			reason = it.Error
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", action, formatBytes(it.Bytes), it.ID, dashIfEmpty(it.Branch), reason)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintln(out)
	actions := make([]string, 0, len(report.Totals))
	for action := range report.Totals {
		actions = append(actions, action)
	}
	sort.Strings(actions)
	for _, action := range actions {
		fmt.Fprintf(out, "%s: %s\n", action, formatPruneTotal(report.Totals[action]))
	}
	if report.DryRun {
		fmt.Fprintf(out, "\ndry run: nothing was removed; reclaimable: %s\n", formatPruneTotal(report.Reclaimable))
		return nil
	}
	fmt.Fprintf(out, "\nreclaimed: %s", formatPruneTotal(report.Reclaimed))
	if n := len(report.PrunedRepositories); n > 0 {
		fmt.Fprintf(out, "; ran git worktree prune in %d %s", n, plural(n, "repository", "repositories"))
	}
	fmt.Fprintln(out)
	return nil
}

func formatPruneTotal(t consult.PruneTotal) string {
	return fmt.Sprintf("%d %s, %s", t.Count, plural(t.Count, "worktree", "worktrees"), formatBytes(t.Bytes))
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// formatBytes renders n in binary units to one decimal ("3.0 GB").
func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	value, suffix := float64(n), "B"
	for _, s := range []string{"KB", "MB", "GB", "TB"} {
		value /= unit
		suffix = s
		if value < unit {
			break
		}
	}
	return fmt.Sprintf("%.1f %s", value, suffix)
}
