package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/service"
	"github.com/blackpaw-studio/leo/internal/session"
	"github.com/blackpaw-studio/leo/internal/telegram"
	"github.com/spf13/cobra"
)

var supervised bool

func newServiceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Start the persistent claude session",
		Long:  "Start a long-running claude session with Telegram channel plugin and optional Remote Control.",
		RunE:  runService,
	}

	cmd.Flags().BoolVar(&supervised, "supervised", false, "run in supervised mode with restart loop (used internally)")
	_ = cmd.Flags().MarkHidden("supervised")

	cmd.AddCommand(
		newServiceStartCmd(),
		newServiceStopCmd(),
		newServiceRestartCmd(),
		newServiceStatusCmd(),
	)

	return cmd
}

func runService(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	// Seed topic cache before the plugin starts consuming getUpdates
	if cfg.Telegram.GroupID != "" {
		seedTopicCache(cfg)
	}

	// Ensure the telegram plugin's .env has the correct bot token from
	// leo.yaml. The agent can accidentally overwrite this file, so we
	// re-sync on every startup.
	if cfg.Telegram.BotToken != "" {
		syncPluginEnv(cfg.Telegram.BotToken)
	}

	claudeArgs := buildClaudeArgs(cfg)

	// Add session persistence
	store := session.NewStore(cfg.Agent.Workspace)
	sid, found, getErr := store.Get("service:dm")
	if getErr != nil {
		warn.Printf("  Could not read session store: %v\n", getErr)
	}
	if found {
		claudeArgs = append(claudeArgs, "--resume", sid)
	} else {
		sid = session.NewID()
		if err := store.Set("service:dm", sid); err != nil {
			warn.Printf("  Could not store session ID: %v\n", err)
		}
		claudeArgs = append(claudeArgs, "--session-id", sid)
	}

	if supervised {
		// Supervised/daemon mode: launch claude in interactive mode via
		// script(1) PTY wrapper. Plugins (telegram) only load in interactive
		// mode. The open stdin pipe keeps the session alive.
		claudePath, err := exec.LookPath("claude")
		if err != nil {
			return fmt.Errorf("claude not found: %w", err)
		}
		info.Println("Starting supervised session...")
		cfgPath, err := resolveConfigPath(cfg)
		if err != nil {
			return fmt.Errorf("resolving config path: %w", err)
		}
		return service.RunSupervised(claudePath, claudeArgs, cfg.Agent.Workspace, cfgPath)
	}

	// Foreground mode: exec replaces this process
	claudePath, err := exec.LookPath("claude")
	if err != nil {
		return fmt.Errorf("claude not found: %w", err)
	}

	info.Println("Starting session...")
	return syscall.Exec(claudePath, append([]string{"claude"}, claudeArgs...), os.Environ())
}

func buildClaudeArgs(cfg *config.Config) []string {
	claudeArgs := []string{
		"--channels", "plugin:telegram@claude-plugins-official",
		"--add-dir", cfg.Agent.Workspace,
	}

	if cfg.Defaults.RemoteControl {
		claudeArgs = append(claudeArgs, "--remote-control", "Leo")
	}

	if cfg.Defaults.BypassPermissions {
		claudeArgs = append(claudeArgs, "--dangerously-skip-permissions")
	}

	mcpConfig := cfg.MCPConfigPath()
	if hasMCPServers(mcpConfig) {
		claudeArgs = append(claudeArgs, "--mcp-config", mcpConfig)
	}

	return claudeArgs
}

func newServiceStartCmd() *cobra.Command {
	var daemon bool

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start service in the background",
		Long:  "Start a background session with auto-restart. Use --daemon to install as an OS service.",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			sc, err := buildServiceConfig(cfg)
			if err != nil {
				return err
			}

			if daemon {
				fmt.Println("Installing daemon...")
				if err := service.InstallDaemon(sc); err != nil {
					return fmt.Errorf("installing daemon: %w", err)
				}
				// Verify it's running
				status, _ := service.DaemonStatus()
				success.Printf("Daemon installed (%s).\n", status)
				info.Printf("Logs: %s\n", sc.LogPath)
				info.Println("Note: run 'leo service start --daemon' again if you update environment variables.")
				return nil
			}

			if err := service.Start(sc); err != nil {
				return err
			}
			success.Println("Service started.")
			info.Printf("Logs: %s\n", sc.LogPath)
			return nil
		},
	}

	cmd.Flags().BoolVar(&daemon, "daemon", false, "install as OS service (launchd/systemd)")

	return cmd
}

func newServiceStopCmd() *cobra.Command {
	var daemon bool

	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop background service",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			if daemon {
				if err := service.RemoveDaemon(); err != nil {
					return fmt.Errorf("removing daemon: %w", err)
				}
				success.Println("Daemon removed.")
				return nil
			}

			if err := service.Stop(cfg.Agent.Workspace); err != nil {
				return err
			}
			success.Println("Service stopped.")
			return nil
		},
	}

	cmd.Flags().BoolVar(&daemon, "daemon", false, "remove OS service (launchd/systemd)")

	return cmd
}

func newServiceRestartCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "restart",
		Short: "Restart daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			fmt.Println("Restarting daemon...")
			if err := service.RestartDaemon(); err != nil {
				return fmt.Errorf("restarting daemon: %w", err)
			}

			status, _ := service.DaemonStatus()
			success.Printf("Daemon restarted (%s).\n", status)
			info.Printf("Logs: %s\n", service.LogPathFor(cfg.Agent.Workspace))
			return nil
		},
	}

	return cmd
}

func newServiceStatusCmd() *cobra.Command {
	var daemon bool

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show service status",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			if daemon {
				status, err := service.DaemonStatus()
				if err != nil {
					return err
				}
				fmt.Printf("Daemon: %s\n", status)
				return nil
			}

			status, err := service.Status(cfg.Agent.Workspace)
			if err != nil {
				return err
			}
			fmt.Printf("Service: %s\n", status)
			return nil
		},
	}

	cmd.Flags().BoolVar(&daemon, "daemon", false, "check OS service status (launchd/systemd)")

	return cmd
}

func buildServiceConfig(cfg *config.Config) (service.ServiceConfig, error) {
	leoPath, err := leoExecutablePath()
	if err != nil {
		return service.ServiceConfig{}, fmt.Errorf("finding leo binary: %w", err)
	}

	configPath, err := resolveConfigPath(cfg)
	if err != nil {
		return service.ServiceConfig{}, err
	}

	logPath := service.LogPathFor(cfg.Agent.Workspace)

	// Capture relevant environment variables for daemon mode
	env := make(map[string]string)
	for _, key := range []string{
		"ANTHROPIC_API_KEY",
		"CLAUDE_CODE_ENTRYPOINT",
		"HOME",
		"PATH",
		"SHELL",
		"USER",
		"TELEGRAM_BOT_TOKEN",
	} {
		if v := os.Getenv(key); v != "" {
			env[key] = v
		}
	}

	return service.ServiceConfig{
		LeoPath:    leoPath,
		ConfigPath: configPath,
		WorkDir:    cfg.Agent.Workspace,
		LogPath:    logPath,
		Env:        env,
	}, nil
}

// seedTopicCache discovers forum topics via getUpdates (before the plugin
// starts consuming them) and writes the result to state/topics.json.
// This is best-effort — failures are silently ignored.
func seedTopicCache(cfg *config.Config) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	topics, err := telegram.FetchTopics(ctx, cfg.Telegram.BotToken, cfg.Telegram.GroupID)
	if err != nil || len(topics) == 0 {
		return
	}

	stateDir := filepath.Join(cfg.Agent.Workspace, "state")
	if err := os.MkdirAll(stateDir, 0750); err != nil {
		warn.Printf("  Could not create state directory: %v\n", err)
		return
	}

	if err := telegram.WriteTopicCache(filepath.Join(stateDir, "topics.json"), topics); err != nil {
		warn.Printf("  Could not cache topics: %v\n", err)
	}
}

func resolveConfigPath(cfg *config.Config) (string, error) {
	if cfgFile != "" {
		return filepath.Abs(cfgFile)
	}
	return filepath.Abs(filepath.Join(cfg.Agent.Workspace, "leo.yaml"))
}

// syncPluginEnv ensures the telegram plugin's .env file has the correct
// bot token from leo.yaml. Preserves other keys in the file. Creates
// the file and parent directories if they don't exist.
func syncPluginEnv(botToken string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	envDir := filepath.Join(home, ".claude", "channels", "telegram")
	envFile := filepath.Join(envDir, ".env")

	// Read existing env to preserve other keys
	lines := []string{}
	if data, err := os.ReadFile(envFile); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if line == "" || strings.HasPrefix(line, "TELEGRAM_BOT_TOKEN=") {
				continue
			}
			lines = append(lines, line)
		}
	}

	// Prepend the bot token
	lines = append([]string{"TELEGRAM_BOT_TOKEN=" + botToken}, lines...)

	if err := os.MkdirAll(envDir, 0750); err != nil {
		return
	}
	_ = os.WriteFile(envFile, []byte(strings.Join(lines, "\n")+"\n"), 0600)
}

// hasMCPServers returns true if the MCP config file exists and contains
// at least one server entry. An empty or malformed file returns false
// to avoid passing an invalid config to claude.
func hasMCPServers(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var cfg struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return false
	}
	return len(cfg.MCPServers) > 0
}
