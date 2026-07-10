// Package harness defines the coding-agent-neutral contract leo uses to
// drive a coding agent CLI. Adapters (claude today; codex/opencode in later
// plans) translate a fully resolved LaunchSpec into binary-specific argv.
//
// Adapters must not consult leo config: every cascade (model, workspace,
// tool lists, merged system prompt, MCP paths) is resolved by the caller
// before the spec reaches an adapter.
package harness

// Kind identifies which leo primitive a launch belongs to. Adapters may
// emit different flags per kind (one-shot task runs vs interactive
// process/agent sessions).
type Kind string

const (
	KindProcess Kind = "process"
	KindAgent   Kind = "agent"
	KindTask    Kind = "task"
)

// SessionMode says how a launch relates to an existing session.
type SessionMode string

const (
	// SessionNone starts a fresh session; the harness picks the ID.
	SessionNone SessionMode = "none"
	// SessionResume continues an existing session by ID.
	SessionResume SessionMode = "resume"
	// SessionPinned starts a fresh session with a pre-issued ID.
	SessionPinned SessionMode = "pinned"
)

// SessionState carries the resolved session decision for a launch.
type SessionState struct {
	Mode SessionMode
	ID   string
}

// LaunchSpec is the harness-neutral description of one coding-agent launch.
type LaunchSpec struct {
	Kind        Kind
	Name        string // process/agent name; empty for tasks
	Model       string // fully resolved
	MaxTurns    int    // 0 = omit the flag (harness default)
	Workspace   string
	AddDirs     []string
	Channels    []string
	DevChannels []string
	Prompt      string // opening prompt (agents) or task prompt; empty for processes
	Session     SessionState
	Options     any // adapter-specific resolved options (e.g. claude.Options)
}

// Harness translates LaunchSpecs into concrete CLI invocations.
type Harness interface {
	// Name is the registry key (config `harness:` value in later plans).
	Name() string
	// Binary is the executable to look up / exec.
	Binary() string
	// Args renders the full argv (excluding argv[0]) for a launch.
	Args(spec LaunchSpec) ([]string, error)
	// SessionArgs renders just the session-selection flags, for callers
	// that append session state after a pre-built arg list.
	SessionArgs(s SessionState) []string

	// ValidateModel reports whether the model name is acceptable for this
	// harness. Empty string is always valid (harness default). The error
	// text is embedded verbatim in config validation output, so phrase it
	// as `%q is not valid (…)` with no leading field path.
	ValidateModel(model string) error

	// DecodeOptions strictly decodes a harness_options map into this
	// adapter's typed options struct. Unknown keys and mistyped values are
	// errors. Runtime-only fields (MCP paths, prefixes) are left zero for
	// the caller to fill.
	DecodeOptions(raw map[string]any) (any, error)

	// SupportsChannels reports whether channel plugins can load in this
	// harness. Only Claude Code hosts channel plugins; others message via
	// leo's MCP tools.
	SupportsChannels() bool
}

// FallbackString returns primary if non-empty, else fallback. Callers use
// it to resolve config cascades into a LaunchSpec.
func FallbackString(primary, fallback string) string {
	if primary != "" {
		return primary
	}
	return fallback
}

// FallbackSlice returns primary if non-empty, else fallback.
func FallbackSlice(primary, fallback []string) []string {
	if len(primary) > 0 {
		return primary
	}
	return fallback
}
