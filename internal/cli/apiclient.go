package cli

import (
	"fmt"
	"io"
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

An API client is an agent or job Leo does not supervise — typically running in
a container or a pipeline. Its token is scoped, and each scope is independent:
--can-message lets it POST to /web/agent/<target>/message (starting a turn),
--can-post lets it POST to /web/agent/<target>/chat/post (dropping text into
the target's chat thread, with no turn). It is refused everywhere else,
including every /api/* route. Changes reach a running daemon immediately.`,
	}
	cmd.AddCommand(newAPIClientAddCmd(), newAPIClientListCmd(), newAPIClientRemoveCmd())
	return cmd
}

func newAPIClientAddCmd() *cobra.Command {
	var canMessage, canPost []string
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Create an API client and print its token once",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if len(canMessage) == 0 && len(canPost) == 0 {
				return fmt.Errorf("--can-message or --can-post is required: a client with no targets can reach nothing")
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
			cfg.APIClients[name] = config.APIClientConfig{CanMessage: canMessage, CanPost: canPost}
			if err := saveConfig(cfg); err != nil {
				return err
			}

			token, err := web.EnsureClientToken(cfg.StatePath(), name)
			if err != nil {
				return fmt.Errorf("creating client token: %w", err)
			}
			reloaded, reloadErr := reloadIfRunning(cmd, cfg)

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Created API client %q%s\n\n", name, describeScopes(canMessage, canPost))
			fmt.Fprintf(out, "  token: %s\n\n", token)
			fmt.Fprintf(out, "Stored at %s (mode 0600).\n", config.APIClientTokenPath(cfg.HomePath, name))
			switch {
			case reloadErr != nil:
				return fmt.Errorf("client %q was saved but is not active: %w; run `leo service reload`", name, reloadErr)
			case reloaded:
				fmt.Fprintln(out, "The running daemon reloaded: the client is active now.")
			default:
				fmt.Fprintln(out, "The daemon is not running; the client applies when the daemon next starts.")
			}
			printReachabilityHint(out, cfg)
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&canMessage, "can-message", nil,
		"agent names this client may message (starts a turn); globs allowed (repeatable)")
	cmd.Flags().StringSliceVar(&canPost, "can-post", nil,
		"agent names this client may post chat text to (no turn); globs allowed (repeatable)")
	return cmd
}

// describeScopes renders " (may message: a; may post to: b)" for the
// non-empty scopes.
func describeScopes(canMessage, canPost []string) string {
	var parts []string
	if len(canMessage) > 0 {
		parts = append(parts, "may message: "+strings.Join(canMessage, ", "))
	}
	if len(canPost) > 0 {
		parts = append(parts, "may post to: "+strings.Join(canPost, ", "))
	}
	return " (" + strings.Join(parts, "; ") + ")"
}

// printReachabilityHint names the two settings a container has to get past
// before its token is even consulted. Both failures return 403, the same
// status a scope denial returns, so an operator who misses this reads a
// Host-check rejection as a broken token.
func printReachabilityHint(out io.Writer, cfg *config.Config) {
	bind := cfg.WebBind()
	if bind == "127.0.0.1" || bind == "::1" {
		fmt.Fprintln(out)
		fmt.Fprintf(out, "Note: web.bind is %s, so only this machine can reach the daemon.\n", bind)
		fmt.Fprintln(out, "A container needs web.bind set to an address it can route to, and that")
		fmt.Fprintln(out, "host or IP listed in web.allowed_hosts — otherwise the Host check")
		fmt.Fprintln(out, "returns 403 before the token is looked at.")
		return
	}
	if len(cfg.Web.AllowedHosts) == 0 {
		fmt.Fprintln(out)
		fmt.Fprintf(out, "Note: web.bind is %s but web.allowed_hosts is empty; requests whose Host\n", bind)
		fmt.Fprintln(out, "header is not localhost:<port> will be refused with 403 before the token")
		fmt.Fprintln(out, "is looked at. Add the host or IP the client will connect to.")
	}
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
				fmt.Fprintln(cmd.OutOrStdout(), "No API clients configured.")
				return nil
			}
			names := make([]string, 0, len(cfg.APIClients))
			for name := range cfg.APIClients {
				names = append(names, name)
			}
			sort.Strings(names)

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "%-24s %-32s %-32s %s\n", "NAME", "CAN MESSAGE", "CAN POST", "TOKEN")
			for _, name := range names {
				status := "missing"
				if _, statErr := os.Stat(config.APIClientTokenPath(cfg.HomePath, name)); statErr == nil {
					status = "present"
				}
				client := cfg.APIClients[name]
				fmt.Fprintf(out, "%-24s %-32s %-32s %s\n", name, strings.Join(client.CanMessage, ", "), strings.Join(client.CanPost, ", "), status)
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
			reloaded, err := reloadIfRunning(cmd, cfg)
			if err != nil {
				return fmt.Errorf("client %q was removed but is not revoked: %w; run `leo service reload`", name, err)
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Removed API client %q and its token.\n", name)
			if reloaded {
				fmt.Fprintln(out, "The running daemon reloaded: the token is revoked.")
			} else {
				fmt.Fprintln(out, "The daemon is not running; the removal applies when the daemon next starts.")
			}
			return nil
		},
	}
}
