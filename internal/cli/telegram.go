package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/blackpaw-studio/leo/internal/telegram"
	"github.com/spf13/cobra"
)

func newTelegramCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "telegram",
		Short: "Telegram utilities",
	}

	cmd.AddCommand(newTelegramTopicsCmd())

	return cmd
}

func newTelegramTopicsCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "topics",
		Short: "Discover forum topics from recent messages",
		Long: `Discover Telegram forum topics by inspecting recent messages.

Requires telegram.group_id in leo.yaml. Topics are discovered from
pending getUpdates results — send a message in each topic first if
no topics appear. Results are cached to state/topics.json.

During an active chat session, reads from the cache (seeded at startup)
since the plugin consumes getUpdates.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			if cfg.Telegram.GroupID == "" {
				return fmt.Errorf("telegram.group_id is not configured in leo.yaml")
			}

			cachePath := filepath.Join(cfg.Agent.Workspace, "state", "topics.json")

			// Try live discovery first
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			topics, err := telegram.FetchTopics(ctx, cfg.Telegram.BotToken, cfg.Telegram.GroupID)
			if err == nil && len(topics) > 0 {
				// Update cache with fresh data
				_ = telegram.WriteTopicCache(cachePath, topics)
			} else {
				// Fall back to cache (e.g. during active chat session)
				cached, cacheErr := telegram.ReadTopicCache(cachePath)
				if cacheErr == nil && len(cached) > 0 {
					topics = cached
				}
			}

			if len(topics) == 0 {
				if jsonOutput {
					fmt.Println("[]")
				} else {
					info.Println("No topics found. Send a message in each forum topic and try again.")
				}
				return nil
			}

			if jsonOutput {
				data, _ := json.Marshal(topics)
				fmt.Println(string(data))
				return nil
			}

			fmt.Printf("Discovered %d topic(s) in group %s:\n\n", len(topics), cfg.Telegram.GroupID)
			for _, t := range topics {
				if t.Name != "" {
					fmt.Printf("  %-20s  message_thread_id: %d\n", t.Name, t.ID)
				} else {
					fmt.Printf("  (unnamed)             message_thread_id: %d\n", t.ID)
				}
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output as JSON")

	return cmd
}
