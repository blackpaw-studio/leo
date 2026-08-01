package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/env"
	claudeharness "github.com/blackpaw-studio/leo/internal/harness/claude"
	"github.com/blackpaw-studio/leo/internal/service"
	"github.com/blackpaw-studio/leo/internal/web"
	"github.com/spf13/cobra"
)

var supervised bool

func newServiceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Run the leo daemon (agent supervision + persistent tasks)",
		Long: `Run the leo daemon, which supervises every ephemeral agent in its own
tmux session with restart-on-crash semantics. Persistent tasks deliver their
prompts into agent tmux sessions, spawning/resuming the target agent on
demand. Subcommands (start/stop/restart/logs) manage the background daemon.`,
		Example: `  # Background daemon lifecycle
  leo service start
  leo service logs -f
  leo service stop`,
		RunE: runService,
	}

	cmd.Flags().BoolVar(&supervised, "supervised", false, "run in supervised mode with restart loop (used internally)")
	_ = cmd.Flags().MarkHidden("supervised")

	cmd.AddCommand(
		newServiceStartCmd(),
		newServiceStopCmd(),
		newServiceRestartCmd(),
		newServiceStatusCmd(),
		newServiceLogsCmd(),
		newServiceReloadCmd(),
		newServiceReparentCmd(),
	)

	return cmd
}

func runService(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	claudePath, err := exec.LookPath(claudeharness.Claude{}.Binary())
	if err != nil {
		return fmt.Errorf("claude not found: %w", err)
	}
	cfgPath, err := resolveConfigPath(cfg)
	if err != nil {
		return fmt.Errorf("resolving config path: %w", err)
	}

	// Resolve the agent bearer token once so every ephemeral agent gets
	// LEO_API_TOKEN exported uniformly. This is deliberately NOT the
	// operator's api.token: the agent token is rejected at /login and on the
	// browser UI, so a token that escapes an agent cannot be traded for a web
	// session. An error here is non-fatal for the daemon itself, but the MCP
	// server refuses to start without it, which is the behaviour we want
	// rather than silently starting a broken session.
	webToken, tokErr := web.EnsureAgentToken(cfg.StatePath())
	if tokErr != nil {
		warn.Printf("  web api token unavailable: %v — MCP server will refuse to start; slash commands will be unavailable\n", tokErr)
	}

	info.Printf("Starting supervised mode...\n")
	return service.RunSupervised(service.RunSupervisedOptions{
		ClaudePath: claudePath,
		HomePath:   cfg.HomePath,
		ConfigPath: cfgPath,
		WebToken:   webToken,
		Version:    Version,
	})
}

func newServiceStartCmd() *cobra.Command {
	var daemon bool

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start service in the background",
		Long: `Start the leo daemon (web UI, cron scheduler, and agent supervision).
It restores any previously running ephemeral agents, each in its own tmux
session with restart-on-crash. The daemon no longer launches config-declared
processes — agents are the only supervised primitive. The CLI stays
foreground-free so this is safe to call from shell scripts. Pass --daemon to
install the daemon as an OS service (launchd on macOS, systemd on Linux) so
it survives reboots.`,
		Example: `  # One-shot background start (this shell session)
  leo service start

  # Install as a persistent OS service
  leo service start --daemon`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			// Surface non-fatal config warnings before starting so
			// misconfigurations fail loudly instead of silently at first
			// task/process invocation.
			for _, msg := range startupWarnings(cfg) {
				warn.Printf("  %s\n", msg)
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
		Long: `Stop the background supervisor and tear down every supervised tmux
session. Per-process session IDs are preserved so a subsequent start will
resume where each process left off. Pass --daemon to also remove the
installed OS service (launchd/systemd).`,
		Example: `  leo service stop
  leo service stop --daemon`,
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

			if err := service.Stop(cfg.HomePath); err != nil {
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
			info.Printf("Logs: %s\n", service.LogPathFor(cfg.HomePath))
			return nil
		},
	}

	return cmd
}

func newServiceStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "status",
		Short:  "Show service status (alias for 'leo status')",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStatus(cmd.Context())
		},
	}
}

func newServiceLogsCmd() *cobra.Command {
	var tail int
	var follow bool

	cmd := &cobra.Command{
		Use:   "logs [name]",
		Short: "Show service logs",
		Long: `Tail the supervisor's aggregate log, or filter it for a single named
entry. The log is written by 'leo service start' to a file under the leo
home dir; this is the supervisor's own chatter (restart events, config
reloads), not the Claude session output itself.`,
		Example: `  leo service logs -n 200
  leo service logs -f
  leo service logs my-session -f`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			logPath := service.LogPathFor(cfg.HomePath)
			if _, err := os.Stat(logPath); err != nil {
				return fmt.Errorf("no log file at %s", logPath)
			}

			if len(args) > 0 {
				return grepLog(logPath, args[0], tail, follow)
			}

			tailArgs := []string{"-n", fmt.Sprintf("%d", tail)}
			if follow {
				tailArgs = append(tailArgs, "-f")
			}
			tailArgs = append(tailArgs, logPath)

			tailCmd := exec.Command("tail", tailArgs...)
			tailCmd.Stdout = os.Stdout
			tailCmd.Stderr = os.Stderr
			return tailCmd.Run()
		},
	}

	cmd.Flags().IntVarP(&tail, "tail", "n", 50, "number of lines to show")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "follow log output")

	return cmd
}

func newServiceReloadCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reload",
		Short: "Reload config without restarting",
		Long:  "Tell the daemon to reload leo.yaml and update task schedules.",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			if !daemon.IsRunning(cfg.HomePath) {
				return fmt.Errorf("daemon is not running")
			}

			resp, err := daemon.Send(cmd.Context(), cfg.HomePath, "POST", "/config/reload", nil)
			if err != nil {
				return fmt.Errorf("sending reload: %w", err)
			}
			if !resp.OK {
				return fmt.Errorf("reload failed: %s", resp.Error)
			}

			success.Println("Config reloaded.")
			return nil
		},
	}
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

	logPath := service.LogPathFor(cfg.HomePath)

	// Capture relevant environment variables for daemon mode
	environ := env.Capture()

	return service.ServiceConfig{
		LeoPath:    leoPath,
		ConfigPath: configPath,
		WorkDir:    cfg.HomePath,
		LogPath:    logPath,
		Env:        environ,
	}, nil
}

func resolveConfigPath(cfg *config.Config) (string, error) {
	if cfgFile != "" {
		return filepath.Abs(cfgFile)
	}
	return filepath.Abs(filepath.Join(cfg.HomePath, "leo.yaml"))
}
