package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/env"
	"github.com/blackpaw-studio/leo/internal/leomcp"
	"github.com/blackpaw-studio/leo/internal/service"
	"github.com/blackpaw-studio/leo/internal/session"
	"github.com/spf13/cobra"
)

var supervised bool

func newServiceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:               "service [process-name]",
		Short:             "Start a persistent claude session",
		Long:              "Start a long-running claude session for a configured process. Defaults to the first enabled process.",
		Args:              cobra.MaximumNArgs(1),
		RunE:              runService,
		ValidArgsFunction: completeProcessNames,
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
	)

	return cmd
}

func runService(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	if supervised {
		claudePath, err := exec.LookPath("claude")
		if err != nil {
			return fmt.Errorf("claude not found: %w", err)
		}
		cfgPath, err := resolveConfigPath(cfg)
		if err != nil {
			return fmt.Errorf("resolving config path: %w", err)
		}

		// In supervised mode, start ALL enabled processes
		specs := buildAllProcessSpecs(cfg, claudePath)
		if len(specs) == 0 {
			return fmt.Errorf("no enabled processes in config")
		}
		info.Printf("Starting supervised mode (%d processes)...\n", len(specs))
		return service.RunSupervised(claudePath, specs, cfg.HomePath, cfgPath)
	}

	// Foreground mode: run a single process, exec replaces this process
	procName, proc, err := resolveProcess(cfg, args)
	if err != nil {
		return err
	}

	claudeArgs := buildProcessArgs(cfg, procName, proc)

	// Add session persistence
	store := session.NewStore(cfg.HomePath)
	sessionKey := "process:" + procName
	sid, found, getErr := store.Get(sessionKey)
	if getErr != nil {
		warn.Printf("  Could not read session store: %v\n", getErr)
	}
	if found {
		claudeArgs = append(claudeArgs, "--resume", sid)
	} else {
		sid = session.NewID()
		if err := store.Set(sessionKey, sid); err != nil {
			warn.Printf("  Could not store session ID: %v\n", err)
		}
		claudeArgs = append(claudeArgs, "--session-id", sid)
	}

	claudePath, err := exec.LookPath("claude")
	if err != nil {
		return fmt.Errorf("claude not found: %w", err)
	}

	info.Printf("Starting session (%s)...\n", procName)
	procEnv := processEnviron(proc)
	return syscall.Exec(claudePath, append([]string{"claude"}, claudeArgs...), procEnv)
}

// processEnviron augments the current environment with LEO_CHANNELS and
// LEO_DEV_CHANNELS (if any) and per-process env vars. Returned slice is safe
// to pass to syscall.Exec.
func processEnviron(proc config.ProcessConfig) []string {
	env := os.Environ()
	if len(proc.Channels) > 0 {
		env = append(env, "LEO_CHANNELS="+strings.Join(proc.Channels, ","))
	}
	if len(proc.DevChannels) > 0 {
		env = append(env, "LEO_DEV_CHANNELS="+strings.Join(proc.DevChannels, ","))
	}
	for k, v := range proc.Env {
		env = append(env, k+"="+v)
	}
	return env
}

// resolveProcess finds the target process by name or returns the first enabled process (sorted by name).
func resolveProcess(cfg *config.Config, args []string) (string, config.ProcessConfig, error) {
	if len(args) > 0 {
		name := args[0]
		proc, ok := cfg.Processes[name]
		if !ok {
			return "", config.ProcessConfig{}, fmt.Errorf("process %q not found in config", name)
		}
		if !proc.Enabled {
			return "", config.ProcessConfig{}, fmt.Errorf("process %q is disabled", name)
		}
		return name, proc, nil
	}

	// Find first enabled process, sorted by name for deterministic selection
	names := make([]string, 0, len(cfg.Processes))
	for name := range cfg.Processes {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		proc := cfg.Processes[name]
		if proc.Enabled {
			return name, proc, nil
		}
	}

	return "", config.ProcessConfig{}, fmt.Errorf("no enabled processes in config")
}

// buildAllProcessSpecs builds ProcessSpec for all enabled processes.
func buildAllProcessSpecs(cfg *config.Config, claudePath string) []service.ProcessSpec {
	var specs []service.ProcessSpec
	for name, proc := range cfg.Processes {
		if !proc.Enabled {
			continue
		}

		args := buildProcessArgs(cfg, name, proc)

		// Add session persistence
		store := session.NewStore(cfg.HomePath)
		sessionKey := "process:" + name
		sid, found, getErr := store.Get(sessionKey)
		if getErr != nil {
			warn.Printf("  [%s] Could not read session store: %v\n", name, getErr)
		}
		if found {
			args = append(args, "--resume", sid)
		} else {
			sid = session.NewID()
			if setErr := store.Set(sessionKey, sid); setErr != nil {
				warn.Printf("  [%s] Could not store session ID: %v\n", name, setErr)
			}
			args = append(args, "--session-id", sid)
		}

		specs = append(specs, service.ProcessSpec{
			Name:       name,
			ClaudeArgs: args,
			WorkDir:    cfg.ProcessWorkspace(proc),
			Env:        mergeChannelsIntoEnv(proc),
			WebPort:    strconv.Itoa(cfg.WebPort()),
		})
	}
	return specs
}

// mergeChannelsIntoEnv returns a new env map combining the process's declared
// env vars with injected LEO_CHANNELS / LEO_DEV_CHANNELS entries (if any are
// configured). The supervisor exports these before launching claude in the
// tmux session.
func mergeChannelsIntoEnv(proc config.ProcessConfig) map[string]string {
	merged := make(map[string]string, len(proc.Env)+2)
	for k, v := range proc.Env {
		merged[k] = v
	}
	if len(proc.Channels) > 0 {
		merged["LEO_CHANNELS"] = strings.Join(proc.Channels, ",")
	}
	if len(proc.DevChannels) > 0 {
		merged["LEO_DEV_CHANNELS"] = strings.Join(proc.DevChannels, ",")
	}
	return merged
}

// buildProcessArgs builds claude CLI args for a named process.
func buildProcessArgs(cfg *config.Config, name string, proc config.ProcessConfig) []string {
	var claudeArgs []string

	// Model
	model := cfg.ProcessModel(proc)
	claudeArgs = append(claudeArgs, "--model", model)

	for _, ch := range proc.Channels {
		claudeArgs = append(claudeArgs, "--channels", ch)
	}
	for _, ch := range proc.DevChannels {
		claudeArgs = append(claudeArgs, "--dangerously-load-development-channels", ch)
	}

	ws := cfg.ProcessWorkspace(proc)
	claudeArgs = append(claudeArgs, "--add-dir", ws)

	for _, dir := range proc.AddDirs {
		claudeArgs = append(claudeArgs, "--add-dir", dir)
	}

	if cfg.ProcessRemoteControl(proc) {
		claudeArgs = append(claudeArgs, "--remote-control", "--remote-control-session-name-prefix", name)
	}

	// Permission mode: process > defaults > bypass_permissions legacy
	permMode := proc.PermissionMode
	if permMode == "" {
		permMode = cfg.Defaults.PermissionMode
	}
	if permMode != "" {
		claudeArgs = append(claudeArgs, "--permission-mode", permMode)
	} else if cfg.ProcessBypassPermissions(proc) {
		claudeArgs = append(claudeArgs, "--dangerously-skip-permissions")
	}

	mcpConfig := cfg.ProcessMCPConfigPath(proc)
	if config.HasMCPServers(mcpConfig) {
		claudeArgs = append(claudeArgs, "--mcp-config", mcpConfig)
	}
	// Always layer in the Leo-managed MCP server (when the daemon's TCP
	// listener is enabled) so every supervised Claude gets the universal
	// channel slash-commands: /clear, /compact, /stop, /tasks, /agent, /agents.
	claudeArgs = leomcp.AppendArg(claudeArgs, cfg)

	if proc.Agent != "" {
		claudeArgs = append(claudeArgs, "--agent", proc.Agent)
	}

	// Allowed tools: process overrides defaults
	allowedTools := proc.AllowedTools
	if len(allowedTools) == 0 {
		allowedTools = cfg.Defaults.AllowedTools
	}
	if len(allowedTools) > 0 {
		claudeArgs = append(claudeArgs, "--allowed-tools", strings.Join(allowedTools, ","))
	}

	// Disallowed tools: process overrides defaults
	disallowedTools := proc.DisallowedTools
	if len(disallowedTools) == 0 {
		disallowedTools = cfg.Defaults.DisallowedTools
	}
	if len(disallowedTools) > 0 {
		claudeArgs = append(claudeArgs, "--disallowed-tools", strings.Join(disallowedTools, ","))
	}

	// System prompt: process overrides defaults
	appendPrompt := proc.AppendSystemPrompt
	if appendPrompt == "" {
		appendPrompt = cfg.Defaults.AppendSystemPrompt
	}
	if appendPrompt != "" {
		claudeArgs = append(claudeArgs, "--append-system-prompt", appendPrompt)
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
			return runStatus()
		},
	}
}

func newServiceLogsCmd() *cobra.Command {
	var tail int
	var follow bool

	cmd := &cobra.Command{
		Use:               "logs [process-name]",
		Short:             "Show service or process logs",
		Long:              "Show the main service log, or filter for a specific process by name.",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeProcessNames,
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

			resp, err := daemon.Send(cfg.HomePath, "POST", "/config/reload", nil)
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

func completeProcessNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	cfg, err := loadConfig()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var names []string
	for name, proc := range cfg.Processes {
		if proc.Enabled {
			names = append(names, name)
		}
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}
