package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/follow"
	"github.com/blackpaw-studio/leo/internal/web"
	"github.com/spf13/cobra"
)

// Testability seams for `leo sync`: the clock status ages are measured
// against, and the runner an in-process pull execs ssh through.
var (
	syncNow                  = time.Now
	syncRunner follow.Runner = follow.ExecRunner{}
)

// syncResult is `leo sync --json`'s output, and the daemon's
// POST /config/follow/sync body.
type syncResult struct {
	Changed bool          `json:"changed"`
	Status  follow.Report `json:"status"`
}

func newSyncCmd() *cobra.Command {
	var status, asJSON bool
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Pull the lead's shared config now (follower hosts)",
		Long: `On a follower (a leo.yaml with a follow: block), pull the lead's templates,
environments, delegation and defaults now instead of waiting for the next
interval. The running daemon does the pull and reloads its config; with no
daemon, leo pulls directly and the change applies when it starts.

--status shows the follow state without pulling: the lead host, when the
cache was last pulled, whether it is stale, the lead's revision and version,
and the last error. See docs/configuration/follow-lead.md.`,
		Example: `  leo sync
  leo sync --status
  leo sync --status --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if status {
				return printFollowStatus(out, follow.BuildReport(cfg, syncNow()), asJSON)
			}
			if !cfg.IsFollower() {
				return follow.ErrNotFollowing
			}
			res, err := syncFollow(cmd, cfg)
			if err != nil {
				return err
			}
			if asJSON {
				return writeIndentedJSON(out, res)
			}
			verb := "unchanged"
			if res.Changed {
				verb = "updated"
			}
			fmt.Fprintf(out, "Pulled lead %q (leo %s, revision %s): %s.\n", res.Status.Host, res.Status.LeadVersion, res.Status.LeadRevision, verb)
			printFollowMessages(out, res.Status)
			return nil
		},
	}
	cmd.Flags().BoolVar(&status, "status", false, "show the follow state without pulling")
	cmd.Flags().BoolVar(&asJSON, "json", false, "output as JSON")
	return cmd
}

// syncFollow pulls through the running daemon, whose follow loop also
// reloads it. With no daemon, or one without a follow loop (an older leo),
// it pulls in process and asks a running daemon to reload.
func syncFollow(cmd *cobra.Command, cfg *config.Config) (syncResult, error) {
	if daemon.IsRunning(cfg.HomePath) {
		resp, err := daemon.Send(cmd.Context(), cfg.HomePath, "POST", "/config/follow/sync", nil)
		switch {
		case err == nil && resp.OK:
			var res syncResult
			if err := json.Unmarshal(resp.Data, &res); err != nil {
				return syncResult{}, fmt.Errorf("decoding sync result: %w", err)
			}
			return res, nil
		case err == nil && resp.Code != web.CodeFollowUnavailable:
			return syncResult{}, fmt.Errorf("sync failed: %s", resp.Error)
		}
	}
	path, err := configPath()
	if err != nil {
		return syncResult{}, err
	}
	load := func() (*config.Config, error) { return config.Load(path) }
	puller := &follow.Puller{LoadConfig: load, Runner: syncRunner, Now: syncNow}
	outcome, err := puller.Pull(cmd.Context())
	if err != nil {
		return syncResult{}, err
	}
	if outcome.Changed {
		if _, err := reloadIfRunning(cmd, cfg); err != nil {
			return syncResult{}, err
		}
	}
	fresh, err := load()
	if err != nil {
		return syncResult{}, err
	}
	return syncResult{Changed: outcome.Changed, Status: follow.BuildReport(fresh, syncNow())}, nil
}

func printFollowStatus(out io.Writer, r follow.Report, asJSON bool) error {
	if asJSON {
		return writeIndentedJSON(out, r)
	}
	if !r.Following {
		fmt.Fprintln(out, "This host does not follow a lead (no follow: block in leo.yaml).")
		return nil
	}
	fmt.Fprintf(out, "Following lead %q (%s), every %s\n", r.Host, r.SSH, r.Interval)
	if r.PulledAt == nil {
		fmt.Fprintln(out, "  Last pull:     never")
	} else {
		age := time.Duration(r.AgeSeconds) * time.Second
		stale := ""
		if r.Stale {
			stale = "  [stale]"
		}
		fmt.Fprintf(out, "  Last pull:     %s (%s ago)%s\n", r.PulledAt.Format(time.RFC3339), age, stale)
		fmt.Fprintf(out, "  Lead revision: %s\n", r.LeadRevision)
		fmt.Fprintf(out, "  Lead version:  %s\n", r.LeadVersion)
	}
	if r.LastAttempt != nil {
		fmt.Fprintf(out, "  Last attempt:  %s\n", r.LastAttempt.Format(time.RFC3339))
	}
	printFollowMessages(out, r)
	return nil
}

func printFollowMessages(out io.Writer, r follow.Report) {
	msgs := r.Messages(Version)
	if len(msgs) == 0 {
		return
	}
	fmt.Fprintln(out, "Warnings:")
	for _, m := range msgs {
		fmt.Fprintf(out, "  - %s\n", m)
	}
}

func writeIndentedJSON(out io.Writer, v any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
