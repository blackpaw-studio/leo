package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/blackpaw-studio/leo/internal/prompt"
	"github.com/blackpaw-studio/leo/internal/session"
	"github.com/spf13/cobra"
)

// Testability seams — replaced in tests.
var (
	sessionIsTTY  = defaultIsTTY
	sessionStdin  *bufio.Reader // if set, used instead of os.Stdin by confirm prompt
	sessionStdout = os.Stdout
)

func sessionReader() *bufio.Reader {
	if sessionStdin != nil {
		return sessionStdin
	}
	return bufio.NewReader(os.Stdin)
}

func newSessionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "session",
		Short: "Manage stored session mappings",
		Long: `List and clear stored Claude session IDs used for session persistence.
Leo records a session ID per task and supervised process so subsequent runs
resume the same Claude conversation. Clearing a session forces the next run
to start a fresh conversation.`,
		Example: `  leo session list
  leo session list --json
  leo session clear task:heartbeat
  leo session clear --all`,
	}

	cmd.AddCommand(
		newSessionListCmd(),
		newSessionClearCmd(),
	)

	return cmd
}

// sessionListEntry is the shape emitted by `leo session list --json`.
type sessionListEntry struct {
	Key       string    `json:"key"`
	SessionID string    `json:"session_id"`
	UpdatedAt time.Time `json:"updated_at"`
}

func newSessionListCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List stored sessions",
		Long: `List every stored session mapping. Each entry pairs a supervised process
or task key (for example 'task:heartbeat', 'service:dm') with a Claude
session ID and the timestamp of the last update. Pass --json for
machine-readable output.`,
		Example: `  leo session list
  leo session list --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			store := session.NewStore(cfg.HomePath)
			entries, err := store.List()
			if err != nil {
				return fmt.Errorf("listing sessions: %w", err)
			}

			// Sort keys for stable output.
			keys := make([]string, 0, len(entries))
			for k := range entries {
				keys = append(keys, k)
			}
			sort.Strings(keys)

			if asJSON {
				out := make([]sessionListEntry, 0, len(keys))
				for _, k := range keys {
					e := entries[k]
					out = append(out, sessionListEntry{
						Key:       k,
						SessionID: e.SessionID,
						UpdatedAt: e.UpdatedAt,
					})
				}
				enc := json.NewEncoder(sessionStdout)
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}

			if len(entries) == 0 {
				fmt.Println("No stored sessions.")
				return nil
			}

			for _, k := range keys {
				e := entries[k]
				fmt.Printf("%-30s  %s  (updated %s)\n", k, e.SessionID, e.UpdatedAt.Local().Format("2006-01-02 15:04"))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "output as JSON")
	return cmd
}

func newSessionClearCmd() *cobra.Command {
	var all, yes bool

	cmd := &cobra.Command{
		Use:   "clear [key]",
		Short: "Clear stored session(s)",
		Long: `Clear a specific session by key (for example 'task:heartbeat',
'service:dm') or clear every session with --all. By default the command
prompts for confirmation when a TTY is attached; pass --yes/-y to skip
the prompt (required for non-interactive use).`,
		Example: `  leo session clear task:heartbeat
  leo session clear task:heartbeat --yes
  leo session clear --all
  leo session clear --all --yes`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			store := session.NewStore(cfg.HomePath)

			if all {
				if !yes {
					if !sessionIsTTY() {
						return fmt.Errorf("refusing to clear all sessions without --yes in non-interactive mode")
					}
					if !prompt.YesNo(sessionReader(), "Clear all stored sessions?", false) {
						info.Println("Aborted.")
						return nil
					}
				}
				if err := store.DeleteAll(); err != nil {
					return fmt.Errorf("clearing sessions: %w", err)
				}
				success.Println("All sessions cleared.")
				return nil
			}

			if len(args) == 0 {
				return fmt.Errorf("specify a session key to clear, or use --all")
			}

			key := args[0]
			_, found, getErr := store.Get(key)
			if getErr != nil {
				return fmt.Errorf("reading session store: %w", getErr)
			}
			if !found {
				return fmt.Errorf("session %q not found", key)
			}

			if !yes {
				if !sessionIsTTY() {
					return fmt.Errorf("refusing to clear session %q without --yes in non-interactive mode", key)
				}
				if !prompt.YesNo(sessionReader(), fmt.Sprintf("Clear session %q?", key), false) {
					info.Println("Aborted.")
					return nil
				}
			}

			if err := store.Delete(key); err != nil {
				return fmt.Errorf("clearing session: %w", err)
			}
			success.Printf("Session %q cleared.\n", key)
			return nil
		},
	}

	cmd.Flags().BoolVar(&all, "all", false, "clear all stored sessions")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip confirmation prompt")

	return cmd
}
