package cli

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/web"
)

// newAPIClientCmd manages external API clients: agents Leo does not supervise
// (an opencode container, a CI job) that hold a bearer token of their own.
//
// Distinct from `leo host`, which configures this machine as a CLI client of a
// remote leo daemon.
func newAPIClientCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "client",
		Short: "Manage external API clients and their scoped tokens",
		Long: `Manage external API clients.

An API client is an agent Leo does not supervise — typically running in a
container — that needs to message one Leo agent. Its token is scoped: it may
POST to /web/agent/<target>/message for the targets you name here, and is
refused everywhere else, including every /api/* route.`,
	}
	cmd.AddCommand(newAPIClientAddCmd(), newAPIClientListCmd(), newAPIClientRemoveCmd())
	return cmd
}

func newAPIClientAddCmd() *cobra.Command {
	var canMessage []string
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Create an API client and print its token once",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if len(canMessage) == 0 {
				return fmt.Errorf("--can-message is required: a client with no targets can message nothing")
			}

			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if cfg.APIClients == nil {
				cfg.APIClients = map[string]config.APIClientConfig{}
			}
			if _, exists := cfg.APIClients[name]; exists {
				return fmt.Errorf("api client %q already exists; remove it first or edit leo.yaml", name)
			}
			cfg.APIClients[name] = config.APIClientConfig{CanMessage: canMessage}
			if err := saveConfig(cfg); err != nil {
				return err
			}

			token, err := web.EnsureClientToken(cfg.StatePath(), name)
			if err != nil {
				return fmt.Errorf("creating client token: %w", err)
			}

			fmt.Printf("Created API client %q (may message: %s)\n\n", name, strings.Join(canMessage, ", "))
			fmt.Printf("  token: %s\n\n", token)
			fmt.Printf("Stored at %s (mode 0600).\n", config.APIClientTokenPath(cfg.HomePath, name))
			fmt.Println("Restart the daemon for it to take effect: leo service restart")
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&canMessage, "can-message", nil,
		"agent names this client may message; globs allowed (repeatable)")
	return cmd
}

func newAPIClientListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List external API clients",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if len(cfg.APIClients) == 0 {
				fmt.Println("No API clients configured.")
				return nil
			}
			names := make([]string, 0, len(cfg.APIClients))
			for name := range cfg.APIClients {
				names = append(names, name)
			}
			sort.Strings(names)

			fmt.Printf("%-24s %-40s %s\n", "NAME", "CAN MESSAGE", "TOKEN")
			for _, name := range names {
				status := "missing"
				if _, statErr := os.Stat(config.APIClientTokenPath(cfg.HomePath, name)); statErr == nil {
					status = "present"
				}
				fmt.Printf("%-24s %-40s %s\n", name, strings.Join(cfg.APIClients[name].CanMessage, ", "), status)
			}
			return nil
		},
	}
}

func newAPIClientRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "rm <name>",
		Aliases: []string{"remove"},
		Short:   "Remove an API client and delete its token",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if _, exists := cfg.APIClients[name]; !exists {
				return fmt.Errorf("no api client named %q", name)
			}
			delete(cfg.APIClients, name)
			if err := saveConfig(cfg); err != nil {
				return err
			}
			tokenPath := config.APIClientTokenPath(cfg.HomePath, name)
			if err := os.Remove(tokenPath); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("removing %s: %w", tokenPath, err)
			}
			fmt.Printf("Removed API client %q and its token.\n", name)
			fmt.Println("Restart the daemon for it to take effect: leo service restart")
			return nil
		},
	}
}
