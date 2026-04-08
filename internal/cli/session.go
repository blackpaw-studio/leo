package cli

import (
	"fmt"
	"sort"

	"github.com/blackpaw-studio/leo/internal/session"
	"github.com/spf13/cobra"
)

func newSessionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "session",
		Short: "Manage stored session mappings",
		Long:  "List and clear stored Claude session IDs used for session persistence.",
	}

	cmd.AddCommand(
		newSessionListCmd(),
		newSessionClearCmd(),
	)

	return cmd
}

func newSessionListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List stored sessions",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			store := session.NewStore(cfg.Agent.Workspace)
			entries, err := store.List()
			if err != nil {
				return fmt.Errorf("listing sessions: %w", err)
			}

			if len(entries) == 0 {
				fmt.Println("No stored sessions.")
				return nil
			}

			// Sort keys for stable output
			keys := make([]string, 0, len(entries))
			for k := range entries {
				keys = append(keys, k)
			}
			sort.Strings(keys)

			for _, k := range keys {
				e := entries[k]
				fmt.Printf("%-30s  %s  (updated %s)\n", k, e.SessionID, e.UpdatedAt.Local().Format("2006-01-02 15:04"))
			}
			return nil
		},
	}
}

func newSessionClearCmd() *cobra.Command {
	var all bool

	cmd := &cobra.Command{
		Use:   "clear [key]",
		Short: "Clear stored session(s)",
		Long:  "Clear a specific session by key (e.g. 'task:heartbeat', 'chat:dm') or all sessions with --all.",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			store := session.NewStore(cfg.Agent.Workspace)

			if all {
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

			if err := store.Delete(key); err != nil {
				return fmt.Errorf("clearing session: %w", err)
			}
			success.Printf("Session %q cleared.\n", key)
			return nil
		},
	}

	cmd.Flags().BoolVar(&all, "all", false, "clear all stored sessions")

	return cmd
}
