// Package harness defines the coding-agent-neutral contract leo uses to
// drive a coding agent CLI. Adapters (claude today; codex/opencode in later
// plans) translate a fully resolved LaunchSpec into binary-specific argv.
//
// Adapters must not consult leo config: every cascade (model, workspace,
// tool lists, merged system prompt, MCP paths) is resolved by the caller
// before the spec reaches an adapter.
package harness

import "io"

// Kind identifies which leo primitive a launch belongs to. Adapters may
// emit different flags per kind (one-shot task runs vs interactive
// process/agent sessions).
type Kind string

const (
	KindProcess Kind = "process"
	KindAgent   Kind = "agent"
	KindTask    Kind = "task"
	KindSession Kind = "session"
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

	// SystemContext is a Leo-injected, harness-neutral system-prompt
	// addendum (messaging + skill-tool awareness). Each adapter renders it
	// via its native channel (claude: --append-system-prompt; codex: -c
	// developer_instructions). Empty to omit. opencode cannot argv-inject
	// and ignores it.
	SystemContext string
}

// Result is the parsed outcome of a one-shot run's output stream.
type Result struct {
	SessionID string   // harness-native session/thread ID; empty if none seen
	Text      string   // final result text
	IsError   bool     // the stream carried a fatal error event/flag
	Errors    []string // error messages accumulated from the stream
}

// Harness translates LaunchSpecs into concrete CLI invocations.
type Harness interface {
	// Name is the registry key (also the config `harness:` value).
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

	// OptionsSchema describes this adapter's harness_options keys for web
	// form rendering, in render order. Must accept exactly the keys
	// DecodeOptions accepts (schematest.Run locks the two together).
	OptionsSchema() []OptionField

	// SupportsChannels reports whether channel plugins can load in this
	// harness. Only Claude Code hosts channel plugins; others message via
	// leo's MCP tools.
	SupportsChannels() bool

	// ParseEvents parses the harness's one-shot output stream (stdout, or
	// combined stdout+stderr) into a Result. Unparseable lines are skipped,
	// never fatal; EOF is end-of-turn. The error return is reserved for
	// reader failures, not stream content.
	ParseEvents(r io.Reader) (Result, error)

	// Env returns harness-specific extra process env for a launch (merged
	// into the spawn env by the caller; caller-provided env must win on
	// collision). Nil when the harness needs nothing.
	Env(spec LaunchSpec) (map[string]string, error)

	// SupportsKind reports whether this harness can run the given leo
	// primitive. Kinds outside this set must also fail loudly in Args().
	SupportsKind(k Kind) bool

	// Driver returns how leo keeps a live session for this harness and
	// talks to it. Nil while the harness supports no interactive kinds
	// (SupportsKind gates every call site).
	Driver() SessionDriver
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
