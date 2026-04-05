package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/service"
	"github.com/spf13/cobra"
)

var supervised bool

func newChatCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "chat",
		Short: "Start interactive Telegram session",
		Long:  "Start a long-running claude session with Telegram channel plugin for inbound messages.",
		RunE:  runChat,
	}

	cmd.Flags().BoolVar(&supervised, "supervised", false, "run in supervised mode with restart loop (used internally)")
	_ = cmd.Flags().MarkHidden("supervised")

	cmd.AddCommand(
		newChatStartCmd(),
		newChatStopCmd(),
		newChatRestartCmd(),
		newChatStatusCmd(),
	)

	return cmd
}

func runChat(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	claudeArgs := buildClaudeArgs(cfg)

	if supervised {
		claudePath, err := exec.LookPath("claude")
		if err != nil {
			return fmt.Errorf("claude not found: %w", err)
		}
		info.Printf("Starting supervised session for agent %q...\n", cfg.Agent.Name)
		return service.RunSupervised(claudePath, claudeArgs, cfg.Agent.Workspace)
	}

	// Foreground mode: exec replaces this process
	claudePath, err := exec.LookPath("claude")
	if err != nil {
		return fmt.Errorf("claude not found: %w", err)
	}

	info.Printf("Starting interactive session for agent %q...\n", cfg.Agent.Name)
	return syscall.Exec(claudePath, append([]string{"claude"}, claudeArgs...), os.Environ())
}

func buildClaudeArgs(cfg *config.Config) []string {
	claudeArgs := []string{
		"--agent", cfg.Agent.Name,
		"--add-dir", cfg.Agent.Workspace,
	}

	mcpConfig := cfg.MCPConfigPath()
	if _, err := os.Stat(mcpConfig); err == nil {
		claudeArgs = append(claudeArgs, "--mcp-config", mcpConfig)
	}

	return claudeArgs
}

func newChatStartCmd() *cobra.Command {
	var daemon bool

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start chat session in the background",
		Long:  "Start a background chat session with auto-restart. Use --daemon to install as an OS service.",
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
				fmt.Printf("Installing daemon for agent %q...\n", cfg.Agent.Name)
				if err := service.InstallDaemon(sc); err != nil {
					return fmt.Errorf("installing daemon: %w", err)
				}
				// Verify it's running
				status, _ := service.DaemonStatus(cfg.Agent.Name)
				success.Printf("Daemon installed for agent %q (%s).\n", cfg.Agent.Name, status)
				info.Printf("Logs: %s\n", sc.LogPath)
				info.Println("Note: run 'leo chat start --daemon' again if you update environment variables.")
				return nil
			}

			if err := service.Start(sc); err != nil {
				return err
			}
			success.Printf("Chat session started for agent %q.\n", cfg.Agent.Name)
			info.Printf("Logs: %s\n", sc.LogPath)
			return nil
		},
	}

	cmd.Flags().BoolVar(&daemon, "daemon", false, "install as OS service (launchd/systemd)")

	return cmd
}

func newChatStopCmd() *cobra.Command {
	var daemon bool

	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop background chat session",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			if daemon {
				if err := service.RemoveDaemon(cfg.Agent.Name); err != nil {
					return fmt.Errorf("removing daemon: %w", err)
				}
				success.Printf("Daemon removed for agent %q.\n", cfg.Agent.Name)
				return nil
			}

			if err := service.Stop(cfg.Agent.Name, cfg.Agent.Workspace); err != nil {
				return err
			}
			success.Printf("Chat session stopped for agent %q.\n", cfg.Agent.Name)
			return nil
		},
	}

	cmd.Flags().BoolVar(&daemon, "daemon", false, "remove OS service (launchd/systemd)")

	return cmd
}

func newChatRestartCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "restart",
		Short: "Restart chat daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			fmt.Printf("Restarting chat daemon for agent %q...\n", cfg.Agent.Name)
			if err := service.RestartDaemon(cfg.Agent.Name); err != nil {
				return fmt.Errorf("restarting daemon: %w", err)
			}

			status, _ := service.DaemonStatus(cfg.Agent.Name)
			success.Printf("Daemon restarted (%s).\n", status)
			info.Printf("Logs: %s\n", service.LogPathFor(cfg.Agent.Workspace))
			return nil
		},
	}

	return cmd
}

func newChatStatusCmd() *cobra.Command {
	var daemon bool

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show chat session status",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			if daemon {
				status, err := service.DaemonStatus(cfg.Agent.Name)
				if err != nil {
					return err
				}
				fmt.Printf("Daemon: %s\n", status)
				return nil
			}

			status, err := service.Status(cfg.Agent.Name, cfg.Agent.Workspace)
			if err != nil {
				return err
			}
			fmt.Printf("Chat: %s\n", status)
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
		AgentName:  cfg.Agent.Name,
		LeoPath:    leoPath,
		ConfigPath: configPath,
		WorkDir:    cfg.Agent.Workspace,
		LogPath:    logPath,
		Env:        env,
	}, nil
}

func resolveConfigPath(cfg *config.Config) (string, error) {
	if cfgFile != "" {
		return filepath.Abs(cfgFile)
	}
	return filepath.Abs(filepath.Join(cfg.Agent.Workspace, "leo.yaml"))
}
