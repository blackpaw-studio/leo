package cli

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"syscall"

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
	return &cobra.Command{
		Use:   "reset <name>",
		Short: "Kill tmux session and clear stored session id — next supervisor pass starts a fresh claude",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := sessionTmuxTarget(args[0])
			if tmuxBin, err := exec.LookPath("tmux"); err == nil {
				_ = exec.Command(tmuxBin, tmux.Args("kill-session", "-t", target)...).Run()
			}
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			store := session.NewStore(cfg.HomePath)
			if err := store.Delete("session:" + args[0]); err != nil {
				return fmt.Errorf("clear session id: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "reset %s: tmux killed (if it existed) and stored session id cleared\n", args[0])
			return nil
		},
	}
}

func newSessionDrainCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "drain <name>",
		Short: "Block until the session's queue is empty (not yet implemented)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), "drain: not yet implemented — use 'leo session status' to check live state")
			return nil
		},
	}
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
