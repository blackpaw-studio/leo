package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"syscall"
	"time"

	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/prompt"
	"github.com/blackpaw-studio/leo/internal/session"
	"github.com/blackpaw-studio/leo/internal/tmux"
	"github.com/spf13/cobra"
)

func newSessionCmd() *cobra.Command {
	parent := &cobra.Command{
		Use:   "session",
		Short: "Manage persistent task sessions",
		Long: `Inspect and manage persistent Claude sessions configured under
` + "`sessions:`" + ` in leo.yaml. Each persistent session is supervised by leo as
a long-lived claude process inside its own tmux session.`,
		Example: `  leo session list
  leo session status daily
  leo session attach daily
  leo session logs daily
  leo session reset daily`,
	}
	parent.AddCommand(
		newSessionListCmd(),
		newSessionStatusCmd(),
		newSessionAttachCmd(),
		newSessionLogsCmd(),
		newSessionResetCmd(),
		newSessionDrainCmd(),
	)
	return parent
}

func newSessionListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List configured persistent sessions",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if len(cfg.Sessions) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "(no sessions configured)")
				return nil
			}
			names := make([]string, 0, len(cfg.Sessions))
			for name := range cfg.Sessions {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				s := cfg.Sessions[name]
				fmt.Fprintf(cmd.OutOrStdout(), "%s\tworkspace=%s\tmodel=%s\tchannels=%v\n",
					name, s.Workspace, s.Model, s.Channels)
			}
			return nil
		},
	}
}

func newSessionStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status <name>",
		Short: "Show session runtime status (stored session id + tmux presence)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			store := session.NewStore(cfg.HomePath)
			id, _, _ := store.Get("session:" + args[0])
			running := "no"
			if isTmuxSessionLive(sessionTmuxTarget(args[0])) {
				running = "yes"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "session=%s\nsession_id=%s\ntmux_running=%s\n", args[0], id, running)
			return nil
		},
	}
}

func newSessionAttachCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "attach <name>",
		Short: "tmux attach to the session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			tmuxBin, err := exec.LookPath("tmux")
			if err != nil {
				return fmt.Errorf("tmux not found: %w", err)
			}
			target := sessionTmuxTarget(args[0])
			argv := append([]string{"tmux"}, tmux.Args("attach", "-t", target)...)
			return syscall.Exec(tmuxBin, argv, os.Environ())
		},
	}
}

func newSessionLogsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logs <name>",
		Short: "Capture recent pane scrollback to stdout",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			tmuxBin, err := exec.LookPath("tmux")
			if err != nil {
				return fmt.Errorf("tmux not found: %w", err)
			}
			target := sessionTmuxTarget(args[0])
			out, err := exec.Command(tmuxBin, tmux.Args("capture-pane", "-p", "-t", target, "-S", "-200")...).Output()
			if err != nil {
				return fmt.Errorf("capture-pane: %w", err)
			}
			_, _ = cmd.OutOrStdout().Write(out)
			return nil
		},
	}
}

func newSessionResetCmd() *cobra.Command {
	var assumeYes bool
	c := &cobra.Command{
		Use:   "reset <name>",
		Short: "Kill tmux session, clear stored session id, and drop queued invocations",
		Long: `Reset destroys a session's live state: it kills the tmux session, clears
the stored session id, and drops any in-flight/queued invocations. Prompts
for confirmation by default when stdin is a TTY; in non-interactive runs
(pipes, cron, CI) pass --yes to confirm up front, otherwise it refuses.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if !assumeYes {
				if !processIsTTY() {
					return fmt.Errorf("refusing to reset %q without confirmation: pass --yes to skip the prompt", name)
				}
				reader := bufio.NewReader(processStdin)
				if !prompt.YesNo(reader, fmt.Sprintf("Reset session %q? This kills its tmux session and drops queued work.", name), false) {
					fmt.Fprintln(cmd.OutOrStdout(), "Cancelled.")
					return nil
				}
			}
			// Notify the router FIRST so any in-flight waiter gets a clean
			// "reset" error instead of hanging until its task timeout. Skip
			// silently if the daemon isn't running.
			if daemon.IsRunning(cfg.HomePath) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				resp, derr := daemon.ResetSession(ctx, cfg.HomePath, name, "cli reset")
				cancel()
				if derr != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "warning: daemon reset failed: %v\n", derr)
				} else if resp.Cleared > 0 {
					fmt.Fprintf(cmd.OutOrStdout(), "reset %s: cleared %d in-flight/queued invocation(s)\n", name, resp.Cleared)
				}
			}
			target := sessionTmuxTarget(name)
			if tmuxBin, err := exec.LookPath("tmux"); err == nil {
				_ = exec.Command(tmuxBin, tmux.Args("kill-session", "-t", target)...).Run()
			}
			store := session.NewStore(cfg.HomePath)
			if err := store.Delete("session:" + name); err != nil {
				return fmt.Errorf("clear session id: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "reset %s: tmux killed (if it existed) and stored session id cleared\n", name)
			return nil
		},
	}
	c.Flags().BoolVar(&assumeYes, "yes", false, "skip the confirmation prompt")
	return c
}

func newSessionDrainCmd() *cobra.Command {
	var timeout time.Duration
	c := &cobra.Command{
		Use:   "drain <name>",
		Short: "Block until the session's queue is empty (queued + in-flight == 0)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if !daemon.IsRunning(cfg.HomePath) {
				return fmt.Errorf("daemon not running — nothing to drain")
			}
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			ticker := time.NewTicker(500 * time.Millisecond)
			defer ticker.Stop()
			for {
				resp, err := daemon.SessionDepth(ctx, cfg.HomePath, name)
				if err != nil {
					return fmt.Errorf("polling depth: %w", err)
				}
				if resp.Depth == 0 {
					fmt.Fprintf(cmd.OutOrStdout(), "drain %s: queue empty\n", name)
					return nil
				}
				select {
				case <-ctx.Done():
					return fmt.Errorf("drain timeout after %s (depth=%d)", timeout, resp.Depth)
				case <-ticker.C:
				}
			}
		},
	}
	c.Flags().DurationVar(&timeout, "timeout", 5*time.Minute, "maximum time to wait for queue to drain")
	return c
}

// sessionTmuxTarget returns the tmux session name leo uses for a persistent
// session, matching the convention in internal/service/session.go.
func sessionTmuxTarget(name string) string { return "leo-session-" + name }

// isTmuxSessionLive returns true when tmux reports the target session exists
// on the leo socket. Degrades to false when tmux is unavailable.
func isTmuxSessionLive(target string) bool {
	tmuxBin, err := exec.LookPath("tmux")
	if err != nil {
		return false
	}
	return exec.Command(tmuxBin, tmux.Args("has-session", "-t", target)...).Run() == nil
}
