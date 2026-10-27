package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/leotools"
	"gopkg.in/yaml.v3"
)

// Default configuration values used across the codebase.
const (
	DefaultModel    = "sonnet"
	DefaultMaxTurns = 15
)

// channelPattern validates channel plugin identifiers (e.g. "plugin:telegram@claude-plugins-official").
var channelPattern = regexp.MustCompile(`^[a-zA-Z0-9:@._-]+$`)

// envKeyPattern validates environment variable names.
var envKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// addDirRejectedChars lists characters that are forbidden in add_dirs entries.
// Entries are passed as argv to claude (`--add-dir <entry>`), so the deny list
// targets argv-level hazards, not shell metacharacters: NUL truncates argv
// syscalls, newline corrupts line-oriented logs, and the remaining chars are
// defense-in-depth against future callers that might concatenate paths into
// a shell string. The list over-catches slightly on purpose.
const addDirRejectedChars = ";&|$`\n\x00"

// NormalizeTrustedProxy parses an IP address or CIDR into its canonical prefix.
// IPv4-mapped IPv6 prefixes are converted to their IPv4 equivalent.
func NormalizeTrustedProxy(value string) (netip.Prefix, error) {
	if addr, err := netip.ParseAddr(value); err == nil {
		if addr.Zone() != "" {
			return netip.Prefix{}, fmt.Errorf("zoned addresses are not supported")
		}
		addr = addr.Unmap()
		return netip.PrefixFrom(addr, addr.BitLen()).Masked(), nil
	}

	prefix, err := netip.ParsePrefix(value)
	if err != nil {
		return netip.Prefix{}, err
	}
	addr := prefix.Addr()
	if addr.Zone() != "" {
		return netip.Prefix{}, fmt.Errorf("zoned addresses are not supported")
	}
	bits := prefix.Bits()
	if addr.Is4In6() {
		addr = addr.Unmap()
		bits -= 96
	}
	return netip.PrefixFrom(addr, bits).Masked(), nil
}

// ValidateAddDir returns a non-nil error if dir is an unsafe --add-dir value.
// Entries are passed as argv to claude, so this guards against argv-level
// hazards rather than shell-injection. Rejects:
//   - empty after trimming
//   - leading "-" (could be misread as a flag by a future argv parser)
//   - NUL (truncates argv syscalls) or newline (breaks line-oriented logs)
//   - `;&|$` and backtick (defense in depth against shell concatenation)
func ValidateAddDir(dir string) error {
	trimmed := strings.TrimSpace(dir)
	if trimmed == "" {
		return fmt.Errorf("add_dirs entry is empty")
	}
	if strings.HasPrefix(trimmed, "-") {
		return fmt.Errorf("add_dirs entry %q must not start with '-'", trimmed)
	}
	if i := strings.IndexAny(dir, addDirRejectedChars); i >= 0 {
		return fmt.Errorf("add_dirs entry %q contains forbidden character %q", trimmed, string(dir[i]))
	}
	return nil
}

type Config struct {
	Defaults DefaultsConfig `yaml:"defaults"`
	Web      WebConfig      `yaml:"web,omitempty"`
	Client   ClientConfig   `yaml:"client,omitempty"`
	// APIClients are external agents Leo does not supervise (e.g. an opencode
	// container) that hold a bearer token of their own. Distinct from
	// `client`, which configures this machine's CLI as an SSH client of a
	// remote leo host.
	APIClients map[string]APIClientConfig `yaml:"api_clients,omitempty"`
	Tasks      map[string]TaskConfig      `yaml:"tasks"`
	Templates  map[string]TemplateConfig  `yaml:"templates,omitempty"`
	// Environments are named, composable env maps independent of templates.
	// Templates, tasks, defaults and agent spawns reference them by name via
	// `environments:`; see environments.go for the merge and cascade rules.
	Environments map[string]map[string]string `yaml:"environments,omitempty"`
	Delegation   *DelegationConfig            `yaml:"delegation,omitempty"`
	// AppearanceRules are the ordered rules that give agents a color and/or
	// an emoji; see appearance.go.
	AppearanceRules AppearanceRuleList `yaml:"appearance_rules,omitempty"`
	// Chat configures Leo Chat (see chat.go).
	Chat ChatConfig `yaml:"chat,omitempty"`
	// Providers was removed with the harness abstraction. The field survives
	// only so Validate() can emit a precise removal error (yaml.v3 silently
	// ignores unknown keys).
	Providers map[string]any `yaml:"providers,omitempty"`

	// TemplatesFile optionally names a YAML file holding shared agent
	// templates (a single `templates:` key). Its templates merge beneath
	// the local ones at load; see shared_templates.go.
	TemplatesFile string `yaml:"templates_file,omitempty"`

	// Follow makes this host mirror a lead host's shared config; see
	// follow.go. Nil means today's behaviour: local config only.
	Follow *FollowConfig `yaml:"follow,omitempty"`

	// Set at load time from the config file path, not serialized.
	HomePath string `yaml:"-"`

	// shared records the loaded templates_file; nil when the key is unset.
	shared *sharedTemplates
	// follow records the lead layer; nil when follow is unset.
	follow *followLayer
}

// ClientConfig holds remote-host definitions used when leo is invoked as a
// client to manage agents on a different machine. Empty on servers.
type ClientConfig struct {
	DefaultHost string                `yaml:"default_host,omitempty"`
	Hosts       map[string]HostConfig `yaml:"hosts,omitempty"`
}

// HostConfig describes a remote leo server reachable over SSH.
type HostConfig struct {
	SSH     string   `yaml:"ssh"`                // e.g. "alice@leo.example.com"
	SSHArgs []string `yaml:"ssh_args,omitempty"` // extra args passed to ssh (e.g. ["-p", "2222"])
	// LeoPath overrides the remote leo binary path used when dispatching
	// `leo agent ...` over SSH. Defaults to DefaultRemoteLeoPath. Useful when
	// the remote's non-interactive shell does not have ~/.local/bin on PATH
	// (e.g. PATH export lives in .zshrc rather than .zshenv).
	LeoPath string `yaml:"leo_path,omitempty"`
	// TmuxPath overrides the remote tmux binary path for `agent attach` and
	// `agent logs --follow`. Defaults to DefaultRemoteTmuxPath. On macOS
	// homebrew, /opt/homebrew/bin is typically added in .zprofile which is
	// sourced for login shells but not for `ssh host cmd`, so bare `tmux`
	// may fail to resolve.
	TmuxPath string `yaml:"tmux_path,omitempty"`
}

// DefaultRemoteLeoPath is the default remote binary path used for SSH
// dispatch when HostConfig.LeoPath is unset. Matches install.sh's default
// install dir. The remote shell expands $HOME at execution time.
const DefaultRemoteLeoPath = "$HOME/.local/bin/leo"

// DefaultRemoteTmuxPath is the default remote tmux command used when
// HostConfig.TmuxPath is unset. Bare "tmux" works on most Linux hosts
// where tmux lives in /usr/bin (always on the default non-interactive
// shell PATH). macOS remotes with homebrew typically need to set
// tmux_path: /opt/homebrew/bin/tmux explicitly.
const DefaultRemoteTmuxPath = "tmux"

// RemoteLeoPath returns LeoPath if set, otherwise DefaultRemoteLeoPath.
func (h HostConfig) RemoteLeoPath() string {
	if h.LeoPath != "" {
		return h.LeoPath
	}
	return DefaultRemoteLeoPath
}

// RemoteTmuxPath returns TmuxPath if set, otherwise DefaultRemoteTmuxPath.
// The returned value is a command string that will be appended with tmux
// arguments on the remote (ssh joins its command args with spaces before
// handing off to the remote shell).
func (h HostConfig) RemoteTmuxPath() string {
	if h.TmuxPath != "" {
		return h.TmuxPath
	}
	return DefaultRemoteTmuxPath
}

type WebConfig struct {
	Enabled      bool     `yaml:"enabled"`
	Port         int      `yaml:"port,omitempty"`
	Bind         string   `yaml:"bind,omitempty"`
	AllowedHosts []string `yaml:"allowed_hosts,omitempty"`
	// TrustedProxies entries are IPs or CIDRs; requests whose peer address
	// matches may authenticate via the Remote-User header.
	TrustedProxies []string `yaml:"trusted_proxies,omitempty"`
	// PublicURLs are the origins a paired device (the iOS app) should reach
	// this daemon at, e.g. a Cloudflare Tunnel hostname. `leo pair`
	// advertises them, falling back to the listener's LAN addresses, and the
	// web listener accepts their exact host:port in Host/Origin.
	PublicURLs []string `yaml:"public_urls,omitempty"`
}

// WebPort returns the effective web UI port (default 8370).
func (c *Config) WebPort() int {
	if c.Web.Port > 0 {
		return c.Web.Port
	}
	return 8370
}

// WebBind returns the effective web UI bind address (default "127.0.0.1",
// loopback-only). Operators who need LAN access must opt in explicitly by
// setting web.bind. Non-loopback binds are gated by the bearer-token
// authentication layer (see internal/web/auth.go) and the allowed_hosts
// allowlist, but still expose the dashboard to anyone with the token.
func (c *Config) WebBind() string {
	if c.Web.Bind != "" {
		return c.Web.Bind
	}
	return "127.0.0.1"
}

// ValidatePublicURL checks a web.public_urls entry: an absolute http(s)
// origin with no path (beyond "/"), query, fragment or credentials.
func ValidatePublicURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Hostname() == "" {
		return errors.New("must be an absolute http(s) URL")
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.User != nil {
		return errors.New("must not have a path, query, fragment or credentials")
	}
	return nil
}

// IsLoopbackBind reports whether addr is a loopback IP address (127.0.0.0/8
// or ::1). Returns false for any non-IP string, including "localhost" — the
// config schema rejects non-IP bind values at validate time, so the happy
// path into this helper always carries a parseable IP.
func IsLoopbackBind(addr string) bool {
	ip := net.ParseIP(addr)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}

type DefaultsConfig struct {
	Model    string `yaml:"model"`
	MaxTurns int    `yaml:"max_turns"`
	// IdleSuspendAfter, when set to a Go duration (e.g. "24h", "30m"), is the
	// global default idle interval after which an ephemeral agent is suspended
	// (process + tmux killed, conversation preserved for auto-resume). Empty
	// disables idle-suspend. Overridable per template and per spawn.
	IdleSuspendAfter string `yaml:"idle_suspend_after,omitempty"`
	// Harness selects the coding-agent adapter (e.g. "claude"). Empty means
	// DefaultHarnessName.
	Harness        string         `yaml:"harness,omitempty"`
	HarnessOptions map[string]any `yaml:"harness_options,omitempty"`
	Dispatch       DispatchConfig `yaml:"dispatch,omitempty"`
	// Environments is the default ordered list of named environments applied
	// when a template, task or spawn does not set its own.
	Environments []string `yaml:"environments,omitempty"`
	// Push chooses which attention transitions push to paired iOS devices.
	// Unset states push.
	Push PushConfig `yaml:"push,omitempty"`
}

type DispatchConfig struct {
	Viewer DispatchViewerConfig `yaml:"viewer,omitempty"`
	// ApprovalTimeout is how long a dispatched claude's permission prompt
	// waits for the orchestrator's decision before falling back to the
	// prompt in its pane, as a Go duration. Empty means 30m.
	ApprovalTimeout string `yaml:"approval_timeout,omitempty"`
	// MaxConcurrent caps the dispatch turns running at once; further new
	// dispatches and follow-ups queue FIFO until a slot frees. Unset means
	// DefaultDispatchMaxConcurrent, 0 means unlimited.
	MaxConcurrent *int `yaml:"max_concurrent,omitempty"`
	// AdoptTimeout is how long a live interactive dispatch found after a
	// daemon restart may take to show bridge or pane activity before it is
	// settled as "daemon restarted", as a Go duration. Empty means 2m.
	AdoptTimeout string `yaml:"adopt_timeout,omitempty"`
}

// DefaultDispatchMaxConcurrent is the slot cap when max_concurrent is unset.
const DefaultDispatchMaxConcurrent = 6

// DispatchMaxConcurrent is the effective slot cap (0 = unlimited).
func (c *Config) DispatchMaxConcurrent() int {
	if v := c.Defaults.Dispatch.MaxConcurrent; v != nil {
		return *v
	}
	return DefaultDispatchMaxConcurrent
}

const (
	defaultDispatchApprovalTimeout = 30 * time.Minute
	maxDispatchApprovalTimeout     = 24 * time.Hour
)

// DispatchApprovalTimeout is the parsed approval_timeout; Validate rejects
// an unusable value, which otherwise reads as the default.
func (c *Config) DispatchApprovalTimeout() time.Duration {
	if d, err := parseApprovalTimeout(c.Defaults.Dispatch.ApprovalTimeout); err == nil && d > 0 {
		return d
	}
	return defaultDispatchApprovalTimeout
}

func parseApprovalTimeout(raw string) (time.Duration, error) {
	return parseBoundedDuration(raw, defaultDispatchApprovalTimeout, maxDispatchApprovalTimeout)
}

// DefaultDispatchAdoptTimeout is adopt_timeout when it is unset.
const DefaultDispatchAdoptTimeout = 2 * time.Minute

const maxDispatchAdoptTimeout = time.Hour

// DispatchAdoptTimeout is the parsed adopt_timeout; Validate rejects an
// unusable value, which otherwise reads as the default.
func (c *Config) DispatchAdoptTimeout() time.Duration {
	if d, err := parseAdoptTimeout(c.Defaults.Dispatch.AdoptTimeout); err == nil && d > 0 {
		return d
	}
	return DefaultDispatchAdoptTimeout
}

func parseAdoptTimeout(raw string) (time.Duration, error) {
	return parseBoundedDuration(raw, DefaultDispatchAdoptTimeout, maxDispatchAdoptTimeout)
}

// parseBoundedDuration parses a Go duration in (0, limit]; empty means def.
func parseBoundedDuration(raw string, def, limit time.Duration) (time.Duration, error) {
	if raw == "" {
		return def, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, err
	}
	if d <= 0 || d > limit {
		return 0, fmt.Errorf("must be positive and at most %s", limit)
	}
	return d, nil
}

type DispatchViewerConfig struct {
	Placement      string `yaml:"placement,omitempty"`
	MaxPanes       *int   `yaml:"max_panes,omitempty"`
	MainPaneHeight *int   `yaml:"main_pane_height,omitempty"`
}

// IsDispatchViewerPlacement reports whether p names a viewer placement: pane
// (a split in the caller's window), window (a window in the caller's
// session) or background (a detached window in the leo-dispatch session).
func IsDispatchViewerPlacement(p string) bool {
	return p == "pane" || p == "window" || p == "background"
}

func (c *Config) DispatchViewerPlacement() string {
	if c.Defaults.Dispatch.Viewer.Placement != "" {
		return c.Defaults.Dispatch.Viewer.Placement
	}
	return "pane"
}
func (c *Config) DispatchViewerMaxPanes() int {
	if v := c.Defaults.Dispatch.Viewer.MaxPanes; v != nil {
		return *v
	}
	return 3
}
func (c *Config) DispatchViewerMainPaneHeight() int {
	if v := c.Defaults.Dispatch.Viewer.MainPaneHeight; v != nil {
		return *v
	}
	return 60
}

type TaskConfig struct {
	Workspace      string            `yaml:"workspace,omitempty"`
	Schedule       string            `yaml:"schedule"`
	Timezone       string            `yaml:"timezone,omitempty"`
	PromptFile     string            `yaml:"prompt_file"`
	Model          string            `yaml:"model,omitempty"`
	MaxTurns       int               `yaml:"max_turns,omitempty"`
	Enabled        bool              `yaml:"enabled"`
	Silent         bool              `yaml:"silent,omitempty"`
	Timeout        string            `yaml:"timeout,omitempty"`        // e.g. "30m", "1h" — default 30m
	Retries        int               `yaml:"retries,omitempty"`        // number of retry attempts on failure, default 0
	Channels       []string          `yaml:"channels,omitempty"`       // channel plugin IDs for NotifyOnFail's channel notification
	DevChannels    []string          `yaml:"dev_channels,omitempty"`   // loaded via --dangerously-load-development-channels
	NotifyOnFail   bool              `yaml:"notify_on_fail,omitempty"` // failure card in Leo Chat; also notifies configured channels
	Env            map[string]string `yaml:"env,omitempty"`
	Environments   []string          `yaml:"environments,omitempty"` // named environments, merged left to right
	Runtime        string            `yaml:"runtime,omitempty"`      // "oneshot" (default) | "persistent"
	Template       string            `yaml:"template,omitempty"`
	QueueMax       int               `yaml:"queue_max,omitempty"` // 0 → use default (5)
	Harness        string            `yaml:"harness,omitempty"`
	HarnessOptions map[string]any    `yaml:"harness_options,omitempty"`
	// Owner is the agent whose Leo Chat thread receives this task's output
	// (leo_chat_post from a run). Validated as a name only; resolved when a
	// message is posted. Empty defaults: a persistent task's own agent,
	// else the agent whose workspace is the task's, else assistant.
	Owner string `yaml:"owner,omitempty"`
}

// TemplateConfig defines a reusable blueprint for spawning ephemeral agents.
// Workspace is the base directory — repos are cloned as subdirectories.
type TemplateConfig struct {
	Workspace   string            `yaml:"workspace,omitempty"`
	Channels    []string          `yaml:"channels,omitempty"`
	DevChannels []string          `yaml:"dev_channels,omitempty"` // loaded via --dangerously-load-development-channels
	Model       string            `yaml:"model,omitempty"`
	MaxTurns    int               `yaml:"max_turns,omitempty"`
	MCPConfig   string            `yaml:"mcp_config,omitempty"`
	AddDirs     []string          `yaml:"add_dirs,omitempty"`
	Env         map[string]string `yaml:"env,omitempty"`
	// Environments are the template's default named environments, merged left
	// to right beneath Env; a spawn's own list replaces them.
	Environments []string `yaml:"environments,omitempty"`
	// IdleSuspendAfter overrides defaults.idle_suspend_after for agents spawned
	// from this template. A Go duration ("24h"); empty inherits the default.
	IdleSuspendAfter string         `yaml:"idle_suspend_after,omitempty"`
	Harness          string         `yaml:"harness,omitempty"`
	HarnessOptions   map[string]any `yaml:"harness_options,omitempty"`
	// Isolation runs dispatches and consults of this template from a
	// managed Git worktree at the caller's committed HEAD ("worktree"),
	// exactly as the leo_dispatch isolation argument does. Empty runs in
	// the caller's tree; a dispatch's own isolation argument wins.
	Isolation string `yaml:"isolation,omitempty"`
	// Permissions constrains the leo MCP tool surface agents spawned from
	// this template see, and which agents/templates they may message, spawn,
	// or consult. The zero value is unrestricted — exactly the behavior
	// templates had before this field existed. Enforced inside the agent's
	// own MCP server, so it is a guardrail rather than a security boundary;
	// see docs/configuration/permissions.md.
	Permissions leotools.Permissions `yaml:"permissions,omitempty"`
	// Push overrides defaults.push per state for agents spawned from this
	// template; an unset state inherits.
	Push PushConfig `yaml:"push,omitempty"`
}

// IsClientOnly reports whether the config is a client-only install: one
// that defines remote hosts but no local processes, tasks, or templates.
// Used to skip workspace/daemon-related operations that don't apply to
// machines that only dispatch commands to remote leo servers.
func (c *Config) IsClientOnly() bool {
	return len(c.Client.Hosts) > 0 &&
		len(c.Tasks) == 0 &&
		len(c.Templates) == 0
}

// DefaultWorkspace returns the default workspace path (HomePath/workspace).
func (c *Config) DefaultWorkspace() string {
	if c.HomePath == "" {
		return ""
	}
	return filepath.Join(c.HomePath, "workspace")
}

// StatePath returns the path to the state directory.
func (c *Config) StatePath() string {
	return filepath.Join(c.HomePath, "state")
}

// ResolveIdleSuspend returns the effective idle-suspend interval for an agent
// spawned from tmpl, with an optional per-spawn override. Cascade:
// override → template → defaults. An empty, unparseable, or non-positive value
// at the winning level means idle-suspend is disabled (returns 0).
func (c *Config) ResolveIdleSuspend(tmpl TemplateConfig, override string) time.Duration {
	raw := c.Defaults.IdleSuspendAfter
	if tmpl.IdleSuspendAfter != "" {
		raw = tmpl.IdleSuspendAfter
	}
	if override != "" {
		raw = override
	}
	if raw == "" {
		return 0
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 0
	}
	return d
}

// TaskWorkspace returns the effective workspace for a task.
func (c *Config) TaskWorkspace(t TaskConfig) string {
	if t.Workspace != "" {
		return t.Workspace
	}
	return c.DefaultWorkspace()
}

// TaskModel resolves the model for a task: task → defaults → built-in.
// The defaults/built-in fall-through only applies when the task runs the
// same harness as defaults — model names are harness-specific, so a claude
// default like "opus" must not leak into a codex task. Empty means the
// harness picks its own default model.
func (c *Config) TaskModel(t TaskConfig) string {
	if t.Model != "" {
		return t.Model
	}
	if c.TaskHarness(t) != c.DefaultsHarness() {
		return ""
	}
	if c.Defaults.Model != "" {
		return c.Defaults.Model
	}
	return DefaultModel
}

// TemplateModel resolves the model for a template: template → defaults → built-in.
func (c *Config) TemplateModel(t TemplateConfig) string {
	if t.Model != "" {
		return t.Model
	}
	if c.Defaults.Model != "" {
		return c.Defaults.Model
	}
	return DefaultModel
}

// TaskMaxTurns returns the effective max turns for a task.
func (c *Config) TaskMaxTurns(t TaskConfig) int {
	if t.MaxTurns > 0 {
		return t.MaxTurns
	}
	if c.Defaults.MaxTurns > 0 {
		return c.Defaults.MaxTurns
	}
	return DefaultMaxTurns
}

// TemplateMaxTurns returns the effective max turns for a template.
func (c *Config) TemplateMaxTurns(t TemplateConfig) int {
	if t.MaxTurns > 0 {
		return t.MaxTurns
	}
	if c.Defaults.MaxTurns > 0 {
		return c.Defaults.MaxTurns
	}
	return DefaultMaxTurns
}

// TaskMCPConfigPath returns the MCP config path for a task.
func (c *Config) TaskMCPConfigPath(t TaskConfig) string {
	ws := c.TaskWorkspace(t)
	return filepath.Join(ws, "config", "mcp-servers.json")
}

// TaskTimeout returns the effective timeout duration for a task.
func (c *Config) TaskTimeout(t TaskConfig) time.Duration {
	if t.Timeout != "" {
		if d, err := time.ParseDuration(t.Timeout); err == nil {
			return d
		}
	}
	return 30 * time.Minute
}

// validateDefaults checks the defaults block. Every message starts with
// "defaults.<key>", which the follow layer relies on to drop a bad lead key.
func (c *Config) validateDefaults(hr harnessResolver) []string {
	var errs []string
	if p := c.DispatchViewerPlacement(); !IsDispatchViewerPlacement(p) {
		errs = append(errs, fmt.Sprintf("defaults.dispatch.viewer.placement %q must be pane, window or background", p))
	}
	if n := c.DispatchViewerMaxPanes(); n < 1 || n > 6 {
		errs = append(errs, "defaults.dispatch.viewer.max_panes must be between 1 and 6")
	}
	if n := c.DispatchViewerMainPaneHeight(); n < 20 || n > 90 {
		errs = append(errs, "defaults.dispatch.viewer.main_pane_height must be between 20 and 90")
	}
	if n := c.DispatchMaxConcurrent(); n < 0 {
		errs = append(errs, fmt.Sprintf("defaults.dispatch.max_concurrent %d must be 0 (unlimited) or positive", n))
	}
	if _, err := parseApprovalTimeout(c.Defaults.Dispatch.ApprovalTimeout); err != nil {
		errs = append(errs, fmt.Sprintf("defaults.dispatch.approval_timeout %q: %v", c.Defaults.Dispatch.ApprovalTimeout, err))
	}
	if _, err := parseAdoptTimeout(c.Defaults.Dispatch.AdoptTimeout); err != nil {
		errs = append(errs, fmt.Sprintf("defaults.dispatch.adopt_timeout %q: %v", c.Defaults.Dispatch.AdoptTimeout, err))
	}
	if hr.defaultsErr != nil {
		errs = append(errs, fmt.Sprintf("defaults.harness %q is not a registered harness (available: %s)", c.DefaultsHarness(), hr.available))
	}
	if hr.defaults != nil {
		if c.Defaults.Model != "" {
			if err := hr.defaults.ValidateModel(c.Defaults.Model); err != nil {
				errs = append(errs, fmt.Sprintf("defaults.model %v", err))
			}
		}
		if _, err := hr.defaults.DecodeOptions(c.Defaults.HarnessOptions); err != nil {
			errs = append(errs, fmt.Sprintf("defaults.harness_options: %v", err))
		}
	}
	if c.Defaults.MaxTurns < 0 {
		errs = append(errs, "defaults.max_turns must not be negative")
	}
	if c.Defaults.IdleSuspendAfter != "" {
		if d, err := time.ParseDuration(c.Defaults.IdleSuspendAfter); err != nil || d <= 0 {
			errs = append(errs, fmt.Sprintf("defaults.idle_suspend_after %q must be a positive duration", c.Defaults.IdleSuspendAfter))
		}
	}

	errs = append(errs, c.validateEnvironmentList("defaults.environments", c.Defaults.Environments)...)
	return errs
}

// harnessResolver resolves the adapter a scope runs for validation, so a bad
// name is reported once: at defaults.harness for the default, and at a
// scope only when it explicitly names an unregistered harness.
type harnessResolver struct {
	defaults    harness.Harness
	defaultsErr error
	available   string
}

func newHarnessResolver(defaultsName string) harnessResolver {
	h, err := harness.Get(defaultsName)
	return harnessResolver{defaults: h, defaultsErr: err, available: strings.Join(harness.Names(), ", ")}
}

// resolve returns the scope's adapter, nil when it has none; msg reports an
// explicitly named unregistered harness.
func (r harnessResolver) resolve(scope, explicit string) (h harness.Harness, msg string) {
	if explicit == "" {
		if r.defaultsErr != nil {
			return nil, ""
		}
		return r.defaults, ""
	}
	h, err := harness.Get(explicit)
	if err != nil {
		return nil, fmt.Sprintf("%s.harness %q is not a registered harness (available: %s)", scope, explicit, r.available)
	}
	return h, ""
}

// knownTemplates is the template map existence checks run against: the
// effective templates plus any invalid lead template, which counts as known
// so it does not cascade into the entries that name it.
func (c *Config) knownTemplates() map[string]TemplateConfig {
	invalid := c.InvalidEntries(KindTemplate)
	if len(invalid) == 0 {
		return c.Templates
	}
	known := maps.Clone(c.Templates)
	if known == nil {
		known = map[string]TemplateConfig{}
	}
	for name := range invalid {
		known[name] = TemplateConfig{}
	}
	return known
}

// validateTemplate checks one template; messages start with "templates.<name>".
func (c *Config) validateTemplate(name string, tmpl TemplateConfig, hr harnessResolver) []string {
	var errs []string
	h, msg := hr.resolve("templates."+name, tmpl.Harness)
	if msg != "" {
		errs = append(errs, msg)
	}
	if h != nil {
		if tmpl.Model != "" {
			if err := h.ValidateModel(tmpl.Model); err != nil {
				errs = append(errs, fmt.Sprintf("templates.%s.model %v", name, err))
			}
		}
		if _, err := h.DecodeOptions(c.TemplateHarnessOptions(tmpl)); err != nil {
			errs = append(errs, fmt.Sprintf("templates.%s.harness_options: %v", name, err))
		}
		if !h.SupportsChannels() && (len(tmpl.Channels) > 0 || len(tmpl.DevChannels) > 0) {
			errs = append(errs, fmt.Sprintf("templates.%s.channels: the %s harness does not support channel plugins; use leo's MCP tools for messaging", name, h.Name()))
		}
		if !h.SupportsKind(harness.KindAgent) {
			errs = append(errs, fmt.Sprintf("templates.%s.harness: the %s harness cannot run ephemeral agents yet (only scheduled tasks) — see docs/configuration/harnesses.md", name, h.Name()))
		}
	}
	if tmpl.MaxTurns < 0 {
		errs = append(errs, fmt.Sprintf("templates.%s.max_turns must not be negative", name))
	}
	for i, ch := range tmpl.Channels {
		if !channelPattern.MatchString(ch) {
			errs = append(errs, fmt.Sprintf("templates.%s.channels[%d] %q contains invalid characters", name, i, ch))
		}
	}
	for i, ch := range tmpl.DevChannels {
		if !channelPattern.MatchString(ch) {
			errs = append(errs, fmt.Sprintf("templates.%s.dev_channels[%d] %q contains invalid characters", name, i, ch))
		}
	}
	for k := range tmpl.Env {
		if !envKeyPattern.MatchString(k) {
			errs = append(errs, fmt.Sprintf("templates.%s.env key %q is not a valid environment variable name", name, k))
		}
	}
	for i, dir := range tmpl.AddDirs {
		if err := ValidateAddDir(dir); err != nil {
			errs = append(errs, fmt.Sprintf("templates.%s.add_dirs[%d]: %v", name, i, err))
		}
	}
	if tmpl.IdleSuspendAfter != "" {
		if d, err := time.ParseDuration(tmpl.IdleSuspendAfter); err != nil || d <= 0 {
			errs = append(errs, fmt.Sprintf("templates.%s.idle_suspend_after %q must be a positive duration", name, tmpl.IdleSuspendAfter))
		}
	}
	if tmpl.Isolation != "" && tmpl.Isolation != "worktree" {
		errs = append(errs, fmt.Sprintf("templates.%s.isolation %q must be empty or \"worktree\"", name, tmpl.Isolation))
	}
	errs = append(errs, validatePermissions(name, tmpl.Permissions, c.knownTemplates(), c.persistedClientTool)...)
	return errs
}

// Validate checks the config for required fields and valid values.
func (c *Config) Validate() error {
	hr := newHarnessResolver(c.DefaultsHarness())
	errs := c.validateDefaults(hr)
	errs = append(errs, c.validateChat()...)
	resolveHarness := func(scope, explicit string) (harness.Harness, bool) {
		h, msg := hr.resolve(scope, explicit)
		if msg != "" {
			errs = append(errs, msg)
		}
		return h, h != nil
	}
	if len(c.Providers) > 0 {
		errs = append(errs, "providers: this section has been removed — see docs/configuration/harnesses.md")
	}

	errs = append(errs, validateAPIClients(c.APIClients)...)

	if c.Web.Port != 0 && (c.Web.Port < 1 || c.Web.Port > 65535) {
		errs = append(errs, fmt.Sprintf("web.port %d is out of range (1-65535)", c.Web.Port))
	}
	bindValid := c.Web.Bind == "" || net.ParseIP(c.Web.Bind) != nil
	if c.Web.Bind != "" && !bindValid {
		errs = append(errs, fmt.Sprintf("web.bind %q is not a valid IP address", c.Web.Bind))
	}
	for i, h := range c.Web.AllowedHosts {
		if strings.TrimSpace(h) == "" {
			errs = append(errs, fmt.Sprintf("web.allowed_hosts[%d] must not be empty", i))
			continue
		}
		if net.ParseIP(h) == nil && strings.ContainsRune(h, ':') {
			errs = append(errs, fmt.Sprintf("web.allowed_hosts[%d] %q must not include a port", i, h))
			continue
		}
		if !isValidHostOrIP(h) {
			errs = append(errs, fmt.Sprintf("web.allowed_hosts[%d] %q is not a valid hostname or IP", i, h))
		}
	}
	for i, proxy := range c.Web.TrustedProxies {
		prefix, err := NormalizeTrustedProxy(proxy)
		if err != nil {
			if err.Error() == "zoned addresses are not supported" {
				errs = append(errs, fmt.Sprintf("web.trusted_proxies[%d] %q: %v", i, proxy, err))
			} else {
				errs = append(errs, fmt.Sprintf("web.trusted_proxies[%d] %q is not a valid IP or CIDR", i, proxy))
			}
		} else if prefix.Bits() == 0 {
			errs = append(errs, fmt.Sprintf("web.trusted_proxies[%d] %q must not match every address", i, proxy))
		}
	}
	for i, raw := range c.Web.PublicURLs {
		if err := ValidatePublicURL(raw); err != nil {
			errs = append(errs, fmt.Sprintf("web.public_urls[%d] %q %v", i, raw, err))
		}
	}
	if c.Web.Enabled && c.Web.Bind != "" && bindValid && !IsLoopbackBind(c.Web.Bind) && len(c.Web.AllowedHosts) == 0 {
		errs = append(errs, "web.allowed_hosts must be set when web.bind is not a loopback address")
	}

	for name, tmpl := range c.Templates {
		errs = append(errs, c.validateTemplate(name, tmpl, hr)...)
	}

	for name, task := range c.Tasks {
		if task.Schedule == "" {
			errs = append(errs, fmt.Sprintf("tasks.%s.schedule is required", name))
		} else if err := validateCronExpr(task.Schedule); err != nil {
			errs = append(errs, fmt.Sprintf("tasks.%s.schedule: %v", name, err))
		}
		if task.PromptFile == "" {
			errs = append(errs, fmt.Sprintf("tasks.%s.prompt_file is required", name))
		}
		if h, ok := resolveHarness("tasks."+name, task.Harness); ok {
			if task.Model != "" {
				if err := h.ValidateModel(task.Model); err != nil {
					errs = append(errs, fmt.Sprintf("tasks.%s.model %v", name, err))
				}
			}
			if _, err := h.DecodeOptions(c.TaskHarnessOptions(task)); err != nil {
				errs = append(errs, fmt.Sprintf("tasks.%s.harness_options: %v", name, err))
			}
			if !h.SupportsChannels() && (len(task.Channels) > 0 || len(task.DevChannels) > 0) {
				errs = append(errs, fmt.Sprintf("tasks.%s.channels: the %s harness does not support channel plugins; use leo's MCP tools for messaging", name, h.Name()))
			}
			if task.Runtime == "persistent" && !h.SupportsKind(harness.KindAgent) {
				errs = append(errs, fmt.Sprintf("tasks.%s.harness: the %s harness cannot run persistent tasks yet (persistent tasks deliver into agents) — see docs/configuration/harnesses.md", name, h.Name()))
			}
		}
		if task.MaxTurns < 0 {
			errs = append(errs, fmt.Sprintf("tasks.%s.max_turns must not be negative", name))
		}
		if task.Timeout != "" {
			if _, err := time.ParseDuration(task.Timeout); err != nil {
				errs = append(errs, fmt.Sprintf("tasks.%s.timeout %q is not a valid duration", name, task.Timeout))
			}
		}
		if task.Timezone != "" {
			if _, err := time.LoadLocation(task.Timezone); err != nil {
				errs = append(errs, fmt.Sprintf("tasks.%s.timezone %q is not a valid IANA zone: %v", name, task.Timezone, err))
			}
		}
		if task.Retries < 0 {
			errs = append(errs, fmt.Sprintf("tasks.%s.retries must not be negative", name))
		}
		for i, ch := range task.Channels {
			if !channelPattern.MatchString(ch) {
				errs = append(errs, fmt.Sprintf("tasks.%s.channels[%d] %q contains invalid characters", name, i, ch))
			}
		}
		for i, ch := range task.DevChannels {
			if !channelPattern.MatchString(ch) {
				errs = append(errs, fmt.Sprintf("tasks.%s.dev_channels[%d] %q contains invalid characters", name, i, ch))
			}
		}
		for k := range task.Env {
			if !envKeyPattern.MatchString(k) {
				errs = append(errs, fmt.Sprintf("tasks.%s.env key %q is not a valid environment variable name", name, k))
			}
		}
		if task.Runtime != "" && task.Runtime != "oneshot" && task.Runtime != "persistent" {
			errs = append(errs, fmt.Sprintf("tasks.%s.runtime %q is not valid (use \"oneshot\" or \"persistent\")", name, task.Runtime))
		}
		if task.Runtime != "persistent" && task.Template != "" {
			errs = append(errs, fmt.Sprintf("tasks.%s.template is only valid when runtime: persistent", name))
		}
		if task.Runtime == "persistent" && task.Template != "" {
			_, tmpl, _, err := c.ResolveTaskTarget(name)
			var invalid *InvalidEntryError
			switch {
			case errors.As(err, &invalid):
				// An isolated lead template: the task fails with its reason at run time.
			case err != nil:
				errs = append(errs, err.Error())
			default:
				if missing, ok := channelSubset(task.Channels, tmpl.Channels); !ok {
					errs = append(errs, fmt.Sprintf("tasks.%s: channel %q is not in templates.%s.channels (task.channels must be a subset)", name, missing, task.Template))
				}
				if missing, ok := channelSubset(task.DevChannels, tmpl.DevChannels); !ok {
					errs = append(errs, fmt.Sprintf("tasks.%s: dev_channel %q is not in templates.%s.dev_channels (task.dev_channels must be a subset)", name, missing, task.Template))
				}
			}
		}
	}

	errs = append(errs, c.validateDelegation()...)
	errs = append(errs, c.validateEnvironments()...)
	errs = append(errs, c.validateFollow()...)

	if len(errs) > 0 {
		return &ValidationErrors{Messages: errs}
	}
	return nil
}

// ValidationErrors is Validate's failure: every problem found, one message
// per entry, each starting with the dotted path of the field it concerns
// (e.g. "templates.coder.model ..."). Error() renders them as one block.
type ValidationErrors struct {
	Messages []string
}

func (e *ValidationErrors) Error() string {
	return fmt.Sprintf("config validation failed:\n  - %s", strings.Join(e.Messages, "\n  - "))
}

// Load reads and parses a leo.yaml config file.
func Load(path string) (*Config, error) {
	cfg, _, err := LoadWithRevision(path)
	return cfg, err
}

// RevisionOf is the revision of leo.yaml content: "sha256:" plus the hex
// digest of its bytes. The JSON config API hands it out as the ETag and
// requires it back as If-Match on every write.
func RevisionOf(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// revisionOfFiles is the revision of leo.yaml plus its shared templates file:
// "sha256:" plus the digest of both files' digests, so a change to either one
// changes it. A config without a shared file keeps RevisionOf's plain digest.
func revisionOfFiles(leo, shared []byte) string {
	l, s := sha256.Sum256(leo), sha256.Sum256(shared)
	sum := sha256.Sum256(append(l[:], s[:]...))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// LoadWithRevision is Load plus the revision of the exact bytes it parsed, so
// a caller that compares revisions under the config write lock cannot race a
// separate read of the file.
func LoadWithRevision(path string) (*Config, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("reading config: %w", err)
	}
	return loadData(path, data, true)
}

// loadUnexpanded loads path like LoadWithRevision but leaves every ~ as
// written, for the follow export, which must copy values verbatim.
func loadUnexpanded(path string) (*Config, error) {
	data, err := os.ReadFile(path) //nolint:gosec // the operator's own leo.yaml
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}
	cfg, _, err := loadData(path, data, false)
	return cfg, err
}

// loadData parses leo.yaml content and merges its templates_file; expand
// says whether ~ in path fields is expanded.
func loadData(path string, data []byte, expand bool) (*Config, string, error) {
	cfg, err := parseWith(path, data, expand)
	if err != nil {
		return nil, "", err
	}
	rev := RevisionOf(data)
	var sharedBytes []byte
	if cfg.TemplatesFile != "" {
		if err := cfg.loadSharedTemplates(data, expand); err != nil {
			return nil, "", err
		}
		sharedBytes = cfg.shared.sharedBytes
		rev = revisionOfFiles(data, sharedBytes)
	}
	if expand && cfg.Follow != nil {
		cfg.applyFollowLayer(data)
		if fc := cfg.follow.cache; fc != nil {
			rev = revisionWithFollow(data, sharedBytes, fc.Digest)
		}
	}
	return cfg, rev, nil
}

// revisionWithFollow is a follower's revision: "sha256:" plus the digest of
// leo.yaml's digest, the shared file's (empty when none) and the lead
// sections' digest, so a pull that changes what the follower uses changes
// it, while a status write or a re-stamped cache does not.
func revisionWithFollow(leo, shared []byte, leadDigest string) string {
	l, s := sha256.Sum256(leo), sha256.Sum256(shared)
	buf := append(append(l[:], s[:]...), leadDigest...)
	sum := sha256.Sum256(buf)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func parse(path string, data []byte) (*Config, error) {
	return parseWith(path, data, true)
}

func parseWith(path string, data []byte, expand bool) (*Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	// Set HomePath from the config file's directory
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolving config path: %w", err)
	}
	cfg.HomePath = filepath.Dir(absPath)

	// Expand ~ in all workspace and path fields
	if expand {
		cfg.expandPaths()
	}

	return &cfg, nil
}

// expandPaths expands ~ in workspace and path fields.
func (c *Config) expandPaths() {
	for name, task := range c.Tasks {
		task.Workspace = expandHome(task.Workspace)
		c.Tasks[name] = task
	}
	expandTemplates(c.Templates)
}

// Save writes the config to a YAML file atomically: readers see either the
// old file or the new one, never a partial write, and the result is 0600
// even when the file previously had broader permissions. If path is a
// symlink, Save writes through it: the link survives and its target is
// replaced (see resolveWriteTarget).
func Save(path string, cfg *Config) error {
	if err := cfg.CheckLeadOwnership(); err != nil {
		return err
	}
	cfg, err := cfg.localView()
	if err != nil {
		return err
	}
	if cfg.shared != nil {
		return saveSplit(path, cfg)
	}
	if cfg.follow != nil {
		return saveFollower(path, cfg)
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}
	if err := writeConfigFile(path, data, false); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}
	return nil
}

// writeConfigFile atomically writes data at path, creating its directory.
func writeConfigFile(path string, data []byte, keepMode bool) error {
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}
	return writeFileAtomicWith(path, data, keepMode)
}

// defaultHomeFn is a testability seam for DefaultHome.
var defaultHomeFn = defaultHomeImpl

// DefaultHome returns the default Leo home path (~/.leo).
func DefaultHome() string {
	return defaultHomeFn()
}

func defaultHomeImpl() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".leo")
}

// FindConfig searches for leo.yaml. Priority:
// 1. Walk up from dir looking for leo.yaml
// 2. Fall back to ~/.leo/leo.yaml
func FindConfig(dir string) (string, error) {
	if dir == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("getting working directory: %w", err)
		}
	}

	// Walk up from dir looking for leo.yaml
	for d := dir; ; {
		path := filepath.Join(d, "leo.yaml")
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}

		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}

	// Fall back to default home
	if home := DefaultHome(); home != "" {
		path := filepath.Join(home, "leo.yaml")
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}

	return "", ErrConfigNotFound
}

// validateCronExpr checks that a cron expression has 5 fields with valid ranges.
func validateCronExpr(expr string) error {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return fmt.Errorf("expected 5 fields, got %d", len(fields))
	}

	names := []string{"minute", "hour", "day-of-month", "month", "day-of-week"}
	maxVals := []int{59, 23, 31, 12, 7}

	for i, field := range fields {
		if field == "*" {
			continue
		}
		// Handle step values like */5 or 1-5/2
		parts := strings.SplitN(field, "/", 2)
		if len(parts) == 2 {
			if _, err := strconv.Atoi(parts[1]); err != nil {
				return fmt.Errorf("%s: invalid step %q", names[i], parts[1])
			}
			field = parts[0]
			if field == "*" {
				continue
			}
		}
		// Handle lists like 1,5,10
		for _, item := range strings.Split(field, ",") {
			// Handle ranges like 1-5
			rangeParts := strings.SplitN(item, "-", 2)
			for _, p := range rangeParts {
				n, err := strconv.Atoi(p)
				if err != nil {
					return fmt.Errorf("%s: %q is not a valid number", names[i], p)
				}
				if n < 0 || n > maxVals[i] {
					return fmt.Errorf("%s: %d is out of range (0-%d)", names[i], n, maxVals[i])
				}
			}
		}
	}
	return nil
}

// HasMCPServers returns true if the MCP config file exists and contains
// at least one server entry.
func HasMCPServers(path string) bool {
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

func expandHome(path string) string {
	if path == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return home
	}
	if len(path) > 1 && path[:2] == "~/" {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, path[2:])
	}
	return path
}

// ListPromptFiles returns all .md files in the workspace, as paths relative
// to the workspace root. Symlinks that resolve outside the workspace (or to a
// directory) are left out. Returns nil (not an error) if the workspace doesn't
// exist.
func ListPromptFiles(workspace string) ([]string, error) {
	absWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		return nil, fmt.Errorf("resolving workspace path: %w", err)
	}
	realWorkspace, err := filepath.EvalSymlinks(absWorkspace)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("resolving workspace path: %w", err)
	}
	root, err := os.OpenRoot(realWorkspace)
	if err != nil {
		return nil, fmt.Errorf("opening workspace: %w", err)
	}
	defer func() { _ = root.Close() }()
	var files []string
	err = fs.WalkDir(root.FS(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if d.IsDir() || !strings.HasSuffix(strings.ToLower(d.Name()), ".md") {
			return nil
		}
		rel := filepath.FromSlash(path)
		// Skip symlinks that leave the workspace (or point at a directory).
		_, resolvedRel, err := resolveInWorkspace(absWorkspace, filepath.Join(absWorkspace, rel))
		if err != nil {
			return nil
		}
		if target, err := root.Stat(resolvedRel); err != nil || target.IsDir() {
			return nil
		}
		files = append(files, rel)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("listing workspace files: %w", err)
	}
	return files, nil
}

// ResolvePromptPath resolves a prompt file path within a task workspace and
// validates that it does not escape the workspace via path traversal.
// Returns the absolute path to the prompt file, or an error.
func ResolvePromptPath(workspace, promptFile string) (string, error) {
	if promptFile == "" {
		return "", fmt.Errorf("prompt_file is empty")
	}
	promptPath := filepath.Join(workspace, promptFile)
	absPrompt, err := filepath.Abs(promptPath)
	if err != nil {
		return "", fmt.Errorf("resolving prompt path: %w", err)
	}
	absWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		return "", fmt.Errorf("resolving workspace path: %w", err)
	}
	if !strings.HasPrefix(absPrompt, absWorkspace+string(filepath.Separator)) {
		return "", fmt.Errorf("prompt file %q escapes workspace", promptFile)
	}
	return absPrompt, nil
}

// resolveInWorkspace maps an absolute path under workspace to its
// symlink-free location relative to the workspace's real directory. A path
// whose symlinks lead outside the workspace is refused; one that leads to
// another file inside it is followed. The deepest existing ancestor is
// resolved and the not-yet-created remainder re-appended.
func resolveInWorkspace(absWorkspace, absPath string) (realWorkspace, rel string, err error) {
	realWorkspace, err = filepath.EvalSymlinks(absWorkspace)
	if err != nil {
		return "", "", err
	}
	var rest []string
	resolved := ""
	for cur := absPath; ; cur = filepath.Dir(cur) {
		if resolved, err = filepath.EvalSymlinks(cur); err == nil {
			break
		}
		if filepath.Dir(cur) == cur {
			return "", "", fmt.Errorf("resolving prompt path: %w", err)
		}
		rest = append(rest, filepath.Base(cur))
	}
	for i := len(rest) - 1; i >= 0; i-- {
		resolved = filepath.Join(resolved, rest[i])
	}
	rel, ok := relativeByIdentity(realWorkspace, resolved)
	if !ok {
		return "", "", fmt.Errorf("prompt file %q escapes workspace via symlink", absPath)
	}
	return realWorkspace, rel, nil
}

// relativeByIdentity returns path relative to the workspace by finding the
// ancestor of path that is the same directory as workspace (os.SameFile), so
// a path spelled with different capitalization on a case-insensitive
// filesystem still counts as inside. ok is false when no ancestor matches.
func relativeByIdentity(workspace, path string) (rel string, ok bool) {
	wsInfo, err := os.Stat(workspace)
	if err != nil {
		return "", false
	}
	tail := ""
	for cur := path; ; cur = filepath.Dir(cur) {
		if info, err := os.Stat(cur); err == nil && os.SameFile(wsInfo, info) {
			if tail == "" {
				return ".", true
			}
			return tail, true
		}
		if filepath.Dir(cur) == cur {
			return "", false
		}
		tail = filepath.Join(filepath.Base(cur), tail)
	}
}

// openPromptRoot resolves promptFile lexically and through symlinks, then
// opens the workspace as an os.Root so the final open also refuses symlinks
// and ".." that leave it (closing the window between resolve and open). The
// returned rel is the prompt's symlink-free path relative to the workspace.
func openPromptRoot(workspace, promptFile string) (root *os.Root, rel string, err error) {
	absPath, err := ResolvePromptPath(workspace, promptFile)
	if err != nil {
		return nil, "", err
	}
	absWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		return nil, "", fmt.Errorf("resolving workspace path: %w", err)
	}
	realWorkspace, rel, err := resolveInWorkspace(absWorkspace, absPath)
	if err != nil {
		return nil, "", err
	}
	root, err = os.OpenRoot(realWorkspace)
	if err != nil {
		return nil, "", err
	}
	return root, rel, nil
}

// ReadPromptBytes reads a prompt file's raw bytes, confined to the workspace:
// a symlink (or symlinked directory) pointing outside it is refused, one
// pointing elsewhere inside it is followed. A missing file or workspace
// satisfies errors.Is(err, fs.ErrNotExist).
func ReadPromptBytes(workspace, promptFile string) ([]byte, error) {
	root, rel, err := openPromptRoot(workspace, promptFile)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	return root.ReadFile(rel)
}

// ReadPromptFile reads a task's prompt file content. Returns empty string
// and nil error if the file does not exist (new file case).
func ReadPromptFile(workspace, promptFile string) (string, error) {
	data, err := ReadPromptBytes(workspace, promptFile)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("reading prompt file: %w", err)
	}
	return string(data), nil
}

// WritePromptFile writes content to a task's prompt file, creating
// parent directories as needed. The write is confined to the workspace the
// same way ReadPromptBytes is. Creating a file through a dangling absolute
// symlink is refused even when its target would be inside the workspace.
func WritePromptFile(workspace, promptFile, content string) error {
	if _, err := ResolvePromptPath(workspace, promptFile); err != nil {
		return err
	}
	if err := os.MkdirAll(workspace, 0750); err != nil {
		return fmt.Errorf("creating prompt file directory: %w", err)
	}
	root, rel, err := openPromptRoot(workspace, promptFile)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if err := root.MkdirAll(filepath.Dir(rel), 0750); err != nil {
		return fmt.Errorf("creating prompt file directory: %w", err)
	}
	f, err := root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("writing prompt file: %w", err)
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		return fmt.Errorf("writing prompt file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("writing prompt file: %w", err)
	}
	return nil
}

// isValidHostOrIP reports whether h is a valid RFC 1123 hostname or a
// parseable IP address. The caller must have already verified h contains
// no colon (port separator).
func isValidHostOrIP(h string) bool {
	if net.ParseIP(h) != nil {
		return true
	}
	if len(h) == 0 || len(h) > 253 {
		return false
	}
	for _, label := range strings.Split(h, ".") {
		if len(label) == 0 || len(label) > 63 {
			return false
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			ok := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-'
			if !ok {
				return false
			}
		}
	}
	return true
}

// persistedClientTool reports whether name is a client tool an app has
// registered with this daemon, from the names the relay persists in the state
// directory. It is what lets deny_tools name a tool no app has connected to
// provide yet this boot.
func (c *Config) persistedClientTool(name string) bool {
	if c.HomePath == "" {
		return false
	}
	for _, n := range leotools.ReadClientToolNames(c.StatePath()) {
		if n == name {
			return true
		}
	}
	return false
}

// validatePermissions checks one template's permissions block and returns the
// error strings it produces (empty when valid).
//
// deny_tools entries must name real tools: a typo there would silently leave
// the tool available, which is the opposite of what the operator asked for.
// can_spawn/can_consult entries name templates, so literal entries are checked
// against the defined set; glob patterns are accepted unchecked since they are
// meant to match names that may not exist yet. can_message is deliberately not
// validated — agent names are generated at spawn time.
//
// extraKnown, when non-nil, names further tools deny_tools accepts: the client
// tools apps have registered, which are not in leotools.Names.
func validatePermissions(name string, p leotools.Permissions, templates map[string]TemplateConfig, extraKnown func(string) bool) []string {
	var errs []string

	for i, tool := range p.DenyTools {
		switch {
		case tool == leotools.SkillTool:
			errs = append(errs, fmt.Sprintf(
				"templates.%s.permissions.deny_tools[%d]: %s cannot be denied — every agent's system context tells it to call %s to operate Leo",
				name, i, leotools.SkillTool, leotools.SkillTool))
		case !leotools.IsKnownTool(tool) && (extraKnown == nil || !extraKnown(tool)):
			errs = append(errs, fmt.Sprintf(
				"templates.%s.permissions.deny_tools[%d] %q is not a leo tool; valid tools: %s",
				name, i, tool, strings.Join(leotools.Names, ", ")))
		}
	}

	// Patterns are checked on every list, including can_message: a malformed
	// glob is accepted by the matcher and then silently matches nothing but
	// its own literal, which is a tighter restriction than the operator wrote
	// and is reported nowhere at runtime.
	checkPatterns := func(field string, list []string) {
		for i, entry := range list {
			if leotools.HasGlob(entry) && !leotools.ValidPattern(entry) {
				errs = append(errs, fmt.Sprintf(
					"templates.%s.permissions.%s[%d] %q is not a valid glob pattern",
					name, field, i, entry))
			}
		}
	}
	checkPatterns("can_message", p.CanMessage)
	checkPatterns("can_spawn", p.CanSpawn)
	checkPatterns("can_consult", p.CanConsult)

	checkTemplates := func(field string, list []string) {
		for i, entry := range list {
			if leotools.HasGlob(entry) {
				continue
			}
			if _, ok := templates[entry]; !ok {
				errs = append(errs, fmt.Sprintf(
					"templates.%s.permissions.%s[%d] %q is not a defined template",
					name, field, i, entry))
			}
		}
	}
	checkTemplates("can_spawn", p.CanSpawn)
	checkTemplates("can_consult", p.CanConsult)

	return errs
}
