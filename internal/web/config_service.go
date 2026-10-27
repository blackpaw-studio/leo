package web

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/consult"
	claudeharness "github.com/blackpaw-studio/leo/internal/harness/claude"
	"github.com/blackpaw-studio/leo/internal/history"
)

// AgentRestarter bounces every live agent in place (agent.Manager.RestartAll).
type AgentRestarter interface {
	RestartAll() agent.RestartResult
}

// ConfigServiceOptions are the collaborators a ConfigService needs. The funcs
// are late-bound because the daemon builds the service in daemon.New, before
// the agent manager, process table and log path exist; each may return a zero
// value (nil restarter, empty path) and the service degrades accordingly.
type ConfigServiceOptions struct {
	ConfigPath string
	// Writer is the process-wide leo.yaml write lock; nil gets a private one.
	Writer   *config.Writer
	Reloader ConfigReloader
	// LeoPath is the leo binary `leo run` and `leo service restart` exec.
	LeoPath string
	// LogPath resolves the service log path ("" disables the log tail).
	LogPath func() string
	// Agents resolves the restarter behind Restart All Agents (nil result: the
	// agent service is unavailable).
	Agents func() AgentRestarter
	// Processes resolves the supervised-process table for service status.
	Processes func() ProcessStateProvider
	// Version resolves the leo build version.
	Version func() string
	// StartedAt and PID identify this daemon in service status.
	StartedAt time.Time
	PID       int
	// Follower resolves the daemon's follow loop (nil result: none runs).
	Follower func() FollowSyncer
}

// ConfigService owns everything a leo.yaml write needs: the config path, the
// shared write lock, the reloader, and the two "restart needed" flags that the
// web banners, the daemon socket and the JSON config API all share. The HTML
// handlers and the JSON handlers are two adapters over its cores, so a save
// validates, persists and reloads identically through either.
//
// web.Server embeds it, so the fields and helpers below are also reachable as
// s.configPath, s.loadConfig() and so on from the HTML handlers.
type ConfigService struct {
	configPath   string
	configWriter *config.Writer
	reloader     ConfigReloader
	leoPath      string

	// serviceRestartNeeded is set when a Web UI config save (port/bind/
	// enabled) changes settings that only take effect when the daemon's TCP
	// listener is rebuilt at boot. Cleared by a service restart.
	serviceRestartNeeded atomic.Bool
	// agentsRestartNeeded is set when a Defaults, Template or Environment save
	// changes settings a *running* agent won't see until it is individually
	// restarted (config reload already makes new spawns and task runs pick it
	// up). Cleared by a restart-all batch with no failures.
	agentsRestartNeeded atomic.Bool

	// serviceLogPath is the absolute path to the service log tailed by the log
	// viewers. Computed by service.LogPathFor(homePath) — internal/web cannot
	// import internal/service (import cycle: service -> daemon -> web) — and
	// threaded in through ConfigServiceOptions. Empty renders a "not
	// configured" message instead of guessing a path.
	serviceLogPath string
	logPath        func() string

	agents   func() AgentRestarter
	follower func() FollowSyncer
	// now is the clock follow status ages are measured against.
	now       func() time.Time
	processes func() ProcessStateProvider
	version   func() string
	startedAt time.Time
	pid       int

	// recentConsults lists role-routed consult records for the delegation
	// document; set by web.New once the consult dispatcher exists.
	recentConsults atomic.Pointer[func() []consult.Record]

	// restartInFlight is set while a `leo service restart` this service started
	// is still running; a second restart request is refused until it ends.
	restartInFlight atomic.Bool
	// maxLogBytes caps a run-log read; 0 means maxTaskLogBytes.
	maxLogBytes int64

	// execCommand is the testability seam for exec.Command.
	execCommand func(name string, args ...string) *exec.Cmd

	// agentMu guards the on-demand, 60s-TTL cache of claude sub-agent names
	// used to populate dropdowns without shelling out on every render.
	agentMu       sync.Mutex
	agentCache    []string
	agentsFetched time.Time
	// fetchAgentListFn is invoked outside agentMu to refresh agentCache.
	// Defaults to fetchAgentList; tests replace it to control timing without
	// shelling out to a real `claude` binary.
	fetchAgentListFn func() []string
}

// NewConfigService builds the shared config service.
func NewConfigService(opts ConfigServiceOptions) *ConfigService {
	writer := opts.Writer
	if writer == nil {
		writer = config.NewWriter()
	}
	c := &ConfigService{
		configPath:   opts.ConfigPath,
		configWriter: writer,
		reloader:     opts.Reloader,
		leoPath:      opts.LeoPath,
		logPath:      opts.LogPath,
		agents:       opts.Agents,
		processes:    opts.Processes,
		version:      opts.Version,
		startedAt:    opts.StartedAt,
		pid:          opts.PID,
		follower:     opts.Follower,
		now:          time.Now,
		execCommand:  exec.Command,
	}
	c.fetchAgentListFn = c.fetchAgentList
	return c
}

// SetRecentConsults wires the role-routed consult records the delegation
// document lists; it is safe to call while requests are being served.
func (c *ConfigService) SetRecentConsults(fn func() []consult.Record) {
	c.recentConsults.Store(&fn)
}

// RestartFlags reports which restarts the saved config is waiting on.
func (c *ConfigService) RestartFlags() RestartRequired {
	return RestartRequired{Service: c.serviceRestartNeeded.Load(), Agents: c.agentsRestartNeeded.Load()}
}

// ClearAgentsRestartNeeded drops the agents-restart flag; the daemon calls it
// after its own restart-all route bounced every agent cleanly.
func (c *ConfigService) ClearAgentsRestartNeeded() { c.agentsRestartNeeded.Store(false) }

// RestartRequired is the restart_required block of a write response.
type RestartRequired struct {
	Service bool `json:"service"`
	Agents  bool `json:"agents"`
}

// loadConfig loads the current config from disk.
func (c *ConfigService) loadConfig() (*config.Config, error) {
	return config.Load(c.configPath)
}

// loadHistory loads the task history store.
func (c *ConfigService) loadHistory(cfg *config.Config) *history.Store {
	return history.NewStore(cfg.HomePath)
}

// lockConfigWrite takes the config write lock and returns its release, for
// handlers that load, mutate, and save leo.yaml:
//
//	defer s.lockConfigWrite()()
func (c *ConfigService) lockConfigWrite() func() {
	return c.configWriter.Lock()
}

// validateAndSave validates the config and saves it. Returns an error message
// for the user, or empty on success.
func (c *ConfigService) validateAndSave(cfg *config.Config) string {
	if err := cfg.Validate(); err != nil {
		return err.Error()
	}
	if err := config.Save(c.configPath, cfg); err != nil {
		return fmt.Sprintf("Failed to save: %v", err)
	}
	return ""
}

// reloadConfigOrWarn invokes the in-process scheduler reloader. It returns an
// empty string on success or when no reloader is configured. On failure it
// logs the error and returns a human-readable warning that callers should
// surface so operators notice that the saved config didn't actually take
// effect in the scheduler.
func (c *ConfigService) reloadConfigOrWarn() string {
	if c.reloader == nil {
		return ""
	}
	if err := c.reloader.ReloadConfig(); err != nil {
		msg := fmt.Sprintf("scheduler reload failed: %v", err)
		log.Printf("web: %s", msg)
		return msg
	}
	return ""
}

// agentList returns the claude sub-agent names, refreshing at most once per
// minute. fetchAgentList shells out to `claude agents`, which can take up to
// 10s, so the shell-out itself must never run while agentMu is held.
//
// The caller that finds the cache stale claims the refresh by stamping
// agentsFetched immediately and releasing the lock before shelling out.
// Any concurrent caller that arrives during that window sees a fresh
// timestamp, so it returns the previous (stale) cache right away instead of
// blocking on the in-flight fetch or starting a second one.
func (c *ConfigService) agentList() []string {
	c.agentMu.Lock()
	if time.Since(c.agentsFetched) <= time.Minute {
		cache := c.agentCache
		c.agentMu.Unlock()
		return cache
	}
	c.agentsFetched = time.Now()
	c.agentMu.Unlock()

	fresh := c.fetchAgentListFn()

	c.agentMu.Lock()
	if len(fresh) > 0 {
		c.agentCache = fresh
	}
	// agentsFetched was already stamped before the shell-out (above), so a
	// nil/empty result from a transient failure still resets the TTL clock
	// — it just keeps the previous (still-valid) cache instead of clobbering
	// it with nothing, and won't hammer the failing command every call.
	cache := c.agentCache
	c.agentMu.Unlock()

	return cache
}

// fetchAgentList runs `claude agents` and parses the agent names.
func (c *ConfigService) fetchAgentList() []string {
	claudePath, err := exec.LookPath(claudeharness.Claude{}.Binary())
	if err != nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, claudePath, "agents").Output()
	if err != nil {
		return nil
	}

	var agents []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, " · ") {
			name := strings.TrimSpace(strings.SplitN(line, " · ", 2)[0])
			if name != "" {
				agents = append(agents, name)
			}
		}
	}
	return agents
}

// newPrivateConfigService builds the service web.New falls back to when the
// caller (a test, an embedder) did not inject one.
func newPrivateConfigService(configPath string, processes ProcessStateProvider, reloader ConfigReloader, agentSvc AgentService, opts Options) *ConfigService {
	svc := NewConfigService(ConfigServiceOptions{
		ConfigPath: configPath,
		Writer:     opts.ConfigWriter,
		Reloader:   reloader,
		LeoPath:    opts.LeoMCP.Executable(),
		Processes:  func() ProcessStateProvider { return processes },
	})
	svc.serviceLogPath = opts.LogPath
	if agentSvc != nil {
		svc.agents = func() AgentRestarter { return agentSvc }
	}
	return svc
}

// logFilePath is the service log path: the one wired at construction, else the
// late-bound one.
func (c *ConfigService) logFilePath() string {
	if c.serviceLogPath != "" {
		return c.serviceLogPath
	}
	if c.logPath != nil {
		return c.logPath()
	}
	return ""
}
