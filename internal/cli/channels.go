package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/blackpaw-studio/leo/internal/channels"
)

// telegramAPIBase is overridden in tests.
var telegramAPIBase = "https://api.telegram.org"

// telegramTokenLookupPaths lists where we'll search for a bot token, in
// priority order: first the Blackpaw Telegram plugin's .env, then the
// Anthropic official Telegram plugin's .env.
var telegramTokenLookupPaths = []string{
	"~/.claude/channels/blackpaw-telegram/.env",
	"~/.claude/channels/telegram/.env",
}

func newChannelsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "channels",
		Short: "Manage channel-plugin bootstrap (per-channel-type setup)",
		Long: `Per-channel-type bootstrap helpers. Channel plugins themselves are
installed and configured via Claude Code's plugin system; these commands
cover the small set of bootstrap concerns Leo can handle generically
(e.g. registering the universal slash-command list with each channel's API
so autocomplete works).`,
	}
	cmd.AddCommand(newChannelsRegisterCommandsCmd())
	return cmd
}

func newChannelsRegisterCommandsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "register-commands <type>",
		Short: "Register Leo's universal slash commands with a channel's API for autocomplete",
		Long: `Register the universal /clear, /compact, /stop, /tasks, /agent, /agents
commands with a channel's API so users see them in the native autocomplete
menu (Telegram's BotFather-style /-menu, Discord's slash-command picker, etc.).

Currently supported channel types: telegram.

This is a one-shot bootstrap call — re-running is idempotent (Telegram's
setMyCommands is replace-not-merge).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch strings.ToLower(args[0]) {
			case "telegram":
				return registerTelegramCommands(cmd.OutOrStdout())
			default:
				return fmt.Errorf("channel type %q not yet supported", args[0])
			}
		},
	}
}

func registerTelegramCommands(out io.Writer) error {
	token, source, err := resolveTelegramToken()
	if err != nil {
		return err
	}

	client := &http.Client{Timeout: 15 * time.Second}

	leoCmds := make([]telegramCommand, 0, len(channels.Canonical))
	for _, c := range channels.Canonical {
		leoCmds = append(leoCmds, telegramCommand{Command: c.Name, Description: c.Description})
	}

	// Telegram uses the most specific scope that matches a chat type.
	// Channel plugins (blackpaw-telegram, official telegram) typically
	// register their own commands at all_private_chats and/or
	// all_group_chats scope, which overrides the default scope. We need
	// to register at each specific scope — merging our commands with
	// whatever the plugin already set — so the Leo commands show up
	// everywhere.
	scopes := []map[string]string{
		{"type": "default"},
		{"type": "all_private_chats"},
		{"type": "all_group_chats"},
	}

	for _, scope := range scopes {
		existing, err := getTelegramCommands(client, token, scope)
		if err != nil {
			fmt.Fprintf(out, "  warning: could not fetch existing commands for scope %s: %v\n", scope["type"], err)
			existing = nil
		}
		merged := mergeCommands(existing, leoCmds)

		if err := setTelegramCommands(client, token, scope, merged); err != nil {
			return fmt.Errorf("set commands for scope %s: %w", scope["type"], err)
		}
		fmt.Fprintf(out, "  scope %s: %d commands\n", scope["type"], len(merged))
	}

	fmt.Fprintf(out, "Registered Leo commands with Telegram (token from %s).\n", source)
	return nil
}

type telegramCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// getTelegramCommands fetches the current command list at a given scope.
func getTelegramCommands(client *http.Client, token string, scope map[string]string) ([]telegramCommand, error) {
	body, err := json.Marshal(map[string]any{"scope": scope})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, telegramAPIBase+"/bot"+token+"/getMyCommands", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var result struct {
		OK     bool              `json:"ok"`
		Result []telegramCommand `json:"result"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	if !result.OK {
		return nil, fmt.Errorf("getMyCommands returned ok=false")
	}
	return result.Result, nil
}

// setTelegramCommands calls setMyCommands at a given scope.
func setTelegramCommands(client *http.Client, token string, scope map[string]string, cmds []telegramCommand) error {
	body, err := json.Marshal(map[string]any{"commands": cmds, "scope": scope})
	if err != nil {
		return fmt.Errorf("marshal commands: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, telegramAPIBase+"/bot"+token+"/setMyCommands", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("call Telegram API: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var result struct {
		OK          bool   `json:"ok"`
		Description string `json:"description,omitempty"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return fmt.Errorf("decode response (status %d): %w", resp.StatusCode, err)
	}
	if !result.OK {
		desc := result.Description
		if desc == "" {
			desc = fmt.Sprintf("status %d", resp.StatusCode)
		}
		return fmt.Errorf("telegram API: %s", desc)
	}
	return nil
}

// mergeCommands adds leoCmds into existing, replacing any with matching names
// and preserving non-Leo commands the plugin registered.
func mergeCommands(existing, leoCmds []telegramCommand) []telegramCommand {
	leoNames := make(map[string]telegramCommand, len(leoCmds))
	for _, c := range leoCmds {
		leoNames[c.Command] = c
	}

	// Start with existing, replacing any that Leo overrides.
	seen := make(map[string]bool, len(existing)+len(leoCmds))
	merged := make([]telegramCommand, 0, len(existing)+len(leoCmds))
	for _, c := range existing {
		if leo, ok := leoNames[c.Command]; ok {
			merged = append(merged, leo)
		} else {
			merged = append(merged, c)
		}
		seen[c.Command] = true
	}
	// Append any Leo commands not already present.
	for _, c := range leoCmds {
		if !seen[c.Command] {
			merged = append(merged, c)
		}
	}
	return merged
}

// resolveTelegramToken returns the bot token and a human-readable source
// description, searching env first then the known plugin .env files.
func resolveTelegramToken() (string, string, error) {
	if v := os.Getenv("TELEGRAM_BOT_TOKEN"); v != "" {
		return v, "TELEGRAM_BOT_TOKEN env", nil
	}
	for _, p := range telegramTokenLookupPaths {
		expanded, err := expandHome(p)
		if err != nil {
			continue
		}
		v, err := readEnvVar(expanded, "TELEGRAM_BOT_TOKEN")
		if err == nil && v != "" {
			return v, expanded, nil
		}
	}
	return "", "", fmt.Errorf("TELEGRAM_BOT_TOKEN not set and not found in %s", strings.Join(telegramTokenLookupPaths, ", "))
}

func expandHome(p string) (string, error) {
	if !strings.HasPrefix(p, "~") {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, strings.TrimPrefix(p, "~")), nil
}

// readEnvVar reads a KEY=value line from a dotenv-style file. Lightweight —
// no quoting, no exports, no interpolation — sufficient for the plugin
// .env files which are written by the plugin itself in a fixed format.
func readEnvVar(path, key string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	prefix := key + "="
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, prefix) {
			val := strings.TrimPrefix(line, prefix)
			// Strip optional surrounding quotes.
			val = strings.Trim(val, `"'`)
			return val, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", nil
}
