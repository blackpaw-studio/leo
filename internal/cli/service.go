package cli

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/env"
	"github.com/blackpaw-studio/leo/internal/harness"
	claudeharness "github.com/blackpaw-studio/leo/internal/harness/claude"
	codexharness "github.com/blackpaw-studio/leo/internal/harness/codex"
	opencodeharness "github.com/blackpaw-studio/leo/internal/harness/opencode"
	"github.com/blackpaw-studio/leo/internal/leomcp"
	"github.com/blackpaw-studio/leo/internal/service"
	"github.com/blackpaw-studio/leo/internal/session"
	"github.com/blackpaw-studio/leo/internal/web"
	"github.com/spf13/cobra"
)

var supervised bool

func newServiceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service [process-name]",
		Short: "Start a persistent claude session",
		Long: `Start a long-running Claude session for a configured process. With no
argument, runs the first enabled process (alphabetically) in the foreground.
Subcommands (start/stop/restart/logs) manage the background supervisor
daemon, which runs every enabled process in its own tmux session with
restart-on-crash semantics.`,
		Example: `  # Run a specific process in the foreground
  leo service my-bot

  # Background daemon lifecycle
  leo service start
  leo service logs -f
  leo service stop`,
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
		claudePath, err := exec.LookPath(claudeharness.Claude{}.Binary())
		if err != nil {
			return fmt.Errorf("claude not found: %w", err)
		}
		cfgPath, err := resolveConfigPath(cfg)
		if err != nil {
			return fmt.Errorf("resolving config path: %w", err)
		}

		// Resolve the API bearer token once so every supervised process and
		// every ephemeral agent gets LEO_API_TOKEN exported uniformly. An
		// error here is non-fatal for the daemon itself, but the MCP server
		// refuses to start without it, which is the behaviour we want
		// rather than silently starting a broken session.
		webToken, tokErr := web.EnsureAPIToken(cfg.StatePath())
		if tokErr != nil {
			warn.Printf("  web api token unavailable: %v — MCP server will refuse to start; slash commands will be unavailable\n", tokErr)
		}

		// In supervised mode, start ALL enabled processes AND boot persistent
		// task sessions. The guard counts both: a home with only persistent
		// tasks (no enabled processes) is still something to supervise.
		//
		// buildAllProcessSpecs and SessionSpecsFromConfig are each called
		// exactly once here so the resulting specs are threaded straight
		// into RunSupervised rather than re-derived.
		specs := buildAllProcessSpecs(cfg, claudePath, webToken)
		procCount := len(specs)
		sessionSpecs, sErr := service.SessionSpecsFromConfig(cfg)
		if sErr != nil {
			warn.Printf("  session specs: %v\n", sErr)
			sessionSpecs = nil
		}
		sessionCount := len(sessionSpecs)
		if procCount == 0 && sessionCount == 0 {
			return fmt.Errorf("no enabled processes or persistent task sessions in config")
		}
		info.Printf("Starting supervised mode (%d process(es), %d session(s))...\n", procCount, sessionCount)
		return service.RunSupervised(claudePath, specs, sessionSpecs, cfg.HomePath, cfgPath, webToken)
	}

	// Foreground mode: run a single process, exec replaces this process
	procName, proc, err := resolveProcess(cfg, args)
	if err != nil {
		return err
	}

	// Foreground mode has no daemon API token to offer a non-claude LeoMCP
	// bridge, so pass "" — processLeoMCPEnv gates off cleanly (matches
	// today's process env, which never sets LEO_API_TOKEN in this path).
	claudeArgs := buildProcessArgs(cfg, procName, proc, "")

	// Add session persistence. This is claude-specific (--session-id/--resume
	// selection via claude's own jsonl transcripts); non-claude harnesses
	// keep whatever session state their own driver manages.
	if cfg.ProcessHarness(proc) == "" || cfg.ProcessHarness(proc) == "claude" {
		store := session.NewStore(cfg.HomePath)
		sessionKey := "process:" + procName
		claudeArgs = append(claudeArgs,
			claudeharness.Claude{}.SessionArgs(
				resolveSessionState(store, sessionKey, cfg.ProcessWorkspace(proc), cfg.ProcessStaleResume(proc), ""),
			)...,
		)
	}

	claudePath, err := exec.LookPath(claudeharness.Claude{}.Binary())
	if err != nil {
		return fmt.Errorf("claude not found: %w", err)
	}

	info.Printf("Starting session (%s)...\n", procName)
	procEnv := processEnviron(proc)
	return syscall.Exec(claudePath, append([]string{claudeharness.Claude{}.Binary()}, claudeArgs...), procEnv)
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
// webToken is the daemon's API bearer token; pass the empty string when it
// could not be resolved (the supervisor will log a warning and the MCP
// server inside each process will fail fast on missing LEO_API_TOKEN).
func buildAllProcessSpecs(cfg *config.Config, claudePath, webToken string) []service.ProcessSpec {
	var specs []service.ProcessSpec
	for name, proc := range cfg.Processes {
		if !proc.Enabled {
			continue
		}

		harnessName := cfg.ProcessHarness(proc)
		args := buildProcessArgs(cfg, name, proc, webToken)

		// Add session persistence. This is claude-specific (--session-id/
		// --resume selection via claude's own jsonl transcripts); non-claude
		// harnesses keep whatever session state their own driver manages.
		if harnessName == "" || harnessName == "claude" {
			store := session.NewStore(cfg.HomePath)
			sessionKey := "process:" + name
			args = append(args,
				claudeharness.Claude{}.SessionArgs(
					resolveSessionState(store, sessionKey, cfg.ProcessWorkspace(proc), cfg.ProcessStaleResume(proc), "["+name+"] "),
				)...,
			)
		}

		procEnv := mergeChannelsIntoEnv(proc)

		specs = append(specs, service.ProcessSpec{
			Name:       name,
			ClaudeArgs: args,
			WorkDir:    cfg.ProcessWorkspace(proc),
			Env:        procEnv,
			WebPort:    strconv.Itoa(cfg.WebPort()),
			WebToken:   webToken,
			StateDir:   cfg.StatePath(),
			Harness:    harnessName,
			Kind:       harness.KindProcess,
		})
	}
	return specs
}

// resolveSessionState resolves the session decision (resume / pinned fresh)
// for a claude invocation. Preference order:
//
//  1. Newest *.jsonl in claude's project directory for this workspace. This
//     matches what /resume inside claude would show at the top of its list
//     and correctly handles sessions created via /clear that Leo's own store
//     never saw.
//  2. Stored session ID from Leo's state — honored as-is so claude can reuse
//     the pre-issued ID when no jsonl has been written yet.
//  3. Fresh session ID minted by Leo and pinned via --session-id.
//
// On a successful step-1 pick that disagrees with the stored ID, the store is
// updated so subsequent restarts, web UI, and `leo agent list` stay in sync
// with what claude is actually running.
//
// logPrefix is prepended to warnings (e.g. "[myproc] " for supervised mode,
// empty for the single-process foreground path).
func resolveSessionState(store *session.Store, sessionKey, workspace string, maxAge time.Duration, logPrefix string) harness.SessionState {
	storedID, _, getErr := store.Get(sessionKey)
	if getErr != nil {
		warn.Printf("  %sCould not read session store: %v\n", logPrefix, getErr)
	}

	latestID, _, latestErr := session.LatestSession(workspace, maxAge)
	if latestErr != nil {
		warn.Printf("  %sCould not inspect claude project directory: %v\n", logPrefix, latestErr)
	}

	switch {
	case latestID != "":
		if latestID != storedID {
			if err := store.Set(sessionKey, latestID); err != nil {
				warn.Printf("  %sCould not update session ID: %v\n", logPrefix, err)
			}
		}
		return harness.SessionState{Mode: harness.SessionResume, ID: latestID}
	case storedID != "":
		return harness.SessionState{Mode: harness.SessionResume, ID: storedID}
	default:
		sid := session.NewID()
		if err := store.Set(sessionKey, sid); err != nil {
			warn.Printf("  %sCould not store session ID: %v\n", logPrefix, err)
		}
		return harness.SessionState{Mode: harness.SessionPinned, ID: sid}
	}
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

// processLeoMCPEnv gates whether a non-claude LeoMCP bridge should be wired
// in for a supervised process, mirroring run/runner.go's leoMCPEnv gate but
// sourced from the webToken already resolved by the caller (supervised mode
// resolves it once via web.EnsureAPIToken; the single-process foreground
// path has none, so it passes ""). LEO_PROCESS_NAME is the bare process
// name (see buildClaudeShellCmd, which exports the same three vars into the
// supervised tmux shell).
func processLeoMCPEnv(cfg *config.Config, name, webToken string) (map[string]string, bool) {
	if cfg == nil || !cfg.Web.Enabled || webToken == "" {
		return nil, false
	}
	return map[string]string{
		"LEO_PROCESS_NAME": name,
		"LEO_WEB_PORT":     strconv.Itoa(cfg.WebPort()),
		"LEO_API_TOKEN":    webToken,
	}, true
}

// resolveProcessLaunch resolves the config cascade for a named process into a
// harness.Harness + fully-populated harness.LaunchSpec, stopping just short of
// calling h.Args(spec). Split out from buildProcessArgs so tests can assert
// on spec.Options (e.g. a codex/opencode LeoMCP bridge) without needing
// Args() to succeed — codex/opencode still refuse KindProcess launches until
// their session drivers land.
func resolveProcessLaunch(cfg *config.Config, name string, proc config.ProcessConfig, webToken string) (harness.Harness, harness.LaunchSpec, error) {
	h, err := harness.Get(cfg.ProcessHarness(proc))
	if err != nil {
		return nil, harness.LaunchSpec{}, fmt.Errorf("resolving harness: %w", err)
	}
	decoded, err := h.DecodeOptions(cfg.ProcessHarnessOptions(proc))
	if err != nil {
		return nil, harness.LaunchSpec{}, fmt.Errorf("decoding harness options: %w", err)
	}

	leoEnv, leoMCPOK := processLeoMCPEnv(cfg, name, webToken)

	spec := harness.LaunchSpec{
		Kind:        harness.KindProcess,
		Name:        name,
		Model:       cfg.ProcessModel(proc),
		Workspace:   cfg.ProcessWorkspace(proc),
		AddDirs:     proc.AddDirs,
		Channels:    proc.Channels,
		DevChannels: proc.DevChannels,
	}

	switch opts := decoded.(type) {
	case claudeharness.Options:
		mcpConfig := ""
		if p := cfg.ProcessMCPConfigPath(proc); config.HasMCPServers(p) {
			mcpConfig = p
		}
		opts.RemoteControlPrefix = name
		opts.AppendSystemPrompt = leomcp.MergeSystemPrompt(cfg, opts.AppendSystemPrompt)
		opts.MCPConfigPath = mcpConfig
		opts.LeoMCPArgs = leomcp.AppendArg(nil, cfg)
		spec.Options = opts
	case codexharness.Options:
		if leoMCPOK {
			opts.LeoMCP = &codexharness.LeoMCPBridge{
				Command:      "leo",
				Args:         []string{"mcp-server"},
				EnvVars:      []string{"LEO_PROCESS_NAME", "LEO_WEB_PORT", "LEO_API_TOKEN"},
				ApprovalMode: "approve",
			}
		}
		spec.Options = opts
	case opencodeharness.Options:
		if leoMCPOK {
			opts.LeoMCP = &opencodeharness.LeoMCPBridge{
				Command: []string{"leo", "mcp-server"},
				Env:     leoEnv,
			}
		}
		state, err := opencodeharness.EnsureServerState(cfg.HomePath, agent.SessionName(name), spec.Model)
		if err != nil {
			return h, harness.LaunchSpec{}, fmt.Errorf("provisioning opencode server state: %w", err)
		}
		opts.ServerPort = state.Port
		opts.ServerPassword = state.Password
		spec.Options = opts
	default:
		return h, harness.LaunchSpec{}, fmt.Errorf("harness %q returned unsupported options type %T", h.Name(), decoded)
	}

	return h, spec, nil
}

// buildProcessArgs builds CLI args for a named process by resolving the
// config cascade into a harness.LaunchSpec. webToken is the daemon's API
// bearer token (empty in the single-process foreground path, which has none
// to offer); see processLeoMCPEnv.
func buildProcessArgs(cfg *config.Config, name string, proc config.ProcessConfig, webToken string) []string {
	h, spec, err := resolveProcessLaunch(cfg, name, proc, webToken)
	if err != nil {
		log.Printf("[%s] %v", name, err)
		return nil
	}
	args, err := h.Args(spec)
	if err != nil {
		log.Printf("[%s] building %s args: %v", name, h.Name(), err)
		return nil
	}
	return args
}

func newServiceStartCmd() *cobra.Command {
	var daemon bool

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start service in the background",
		Long: `Start the background supervisor, which launches every enabled process
in its own tmux session with restart-on-crash. The CLI stays foreground-free
so this is safe to call from shell scripts. Pass --daemon to install the
supervisor as an OS service (launchd on macOS, systemd on Linux) so it
survives reboots.`,
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
		Use:   "logs [process-name]",
		Short: "Show service or process logs",
		Long: `Tail the supervisor's aggregate log, or filter it for a single process.
The log is written by 'leo service start' to a file under the leo home dir;
this is the supervisor's own chatter (restart events, config reloads), not
the Claude process output itself. For per-process Claude output, use
'leo process logs <name>' which reads the tmux pane directly.`,
		Example: `  leo service logs -n 200
  leo service logs -f
  leo service logs my-bot -f`,
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
