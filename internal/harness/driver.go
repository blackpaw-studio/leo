package harness

import "context"

// SessionIDStore persists the harness-native session/thread ID across
// turns. Implementations are supplied by the caller (e.g. leo's session
// store); drivers only read and write through this seam.
type SessionIDStore interface {
	Get() string
	Set(id string)
	Clear()
}

// SessionHandle carries everything a SessionDriver needs to start or talk
// to one live session. Callers resolve every field before invoking the
// driver; drivers must not consult leo config directly.
type SessionHandle struct {
	Kind          Kind
	Name          string // logical process/agent/session name
	TmuxSession   string // tmux session name; routing key for driver state files
	Workspace     string
	HomePath      string
	Env           map[string]string // resolved spawn env for driver-spawned helper processes
	OpeningPrompt string            // delivered by Start for drivers that can't put it in argv
	IDs           SessionIDStore
}

// AttachSpec says how a caller attaches to a live session: every harness
// runs its TUI inside the leo tmux session, so attach is a tmux attach.
type AttachSpec struct {
	TmuxSession string
}

// SessionDriver is the per-harness contract for keeping a live interactive
// session and delivering messages to it. Every SupportsKind-gated
// interactive call site goes through a SessionDriver.
type SessionDriver interface {
	// Start arranges whatever the driver needs before the first Inject
	// (e.g. delivering an opening prompt). Called once per session launch.
	Start(ctx context.Context, h SessionHandle) error
	// Inject delivers one message. nil *Result = delivery is asynchronous
	// (claude: completion arrives via the Stop hook / conversation lives in
	// the pane). Non-nil = the turn ran to completion synchronously.
	Inject(ctx context.Context, h SessionHandle, msg string) (*Result, error)
	// Attach returns how a caller can view the live session.
	Attach(h SessionHandle) (AttachSpec, error)
}

// PaneCare is an optional SessionDriver capability, asserted by the
// supervisor for drivers whose captured pane content needs special key
// handling (e.g. dismissing dialogs).
type PaneCare interface {
	PaneKey(pane string) string // tmux key to send for a captured pane, or ""
}

// QuickExitAction tells the supervisor what to do with the stored session
// state after a driver-spawned process exits abnormally soon after launch.
type QuickExitAction int

const (
	// QuickExitClearSession clears the stored session id (today's default
	// branch).
	QuickExitClearSession QuickExitAction = iota
	// QuickExitRetryArgs relaunches with the returned args, keeping the
	// stored id.
	QuickExitRetryArgs
	// QuickExitClearAndNoResume clears the stored id AND marks the agent
	// no-resume.
	QuickExitClearAndNoResume
	// QuickExitNone keeps the args and stored id (e.g. a quick exit that
	// doesn't imply the resumed conversation itself is poisoned).
	QuickExitNone
)

// QuickExitRecovery is an optional SessionDriver capability for harnesses
// that need custom recovery behavior on a quick exit.
type QuickExitRecovery interface {
	RecoverQuickExit(args []string) ([]string, QuickExitAction)
}

// PreLauncher is an optional SessionDriver capability: PreLaunch runs before
// every tmux new-session spawn of this session (fresh and restart alike), in
// the supervisor's goroutine. It must be idempotent and fast — e.g. codex
// registers the workspace as trusted in ~/.codex/config.toml so the TUI
// never blocks on its trust dialog. Errors are logged, never fatal: a failed
// hook degrades to the TUI showing its dialog, which the operator can answer.
type PreLauncher interface {
	PreLaunch(h SessionHandle) error
}

// SessionArgsRefresher is an optional SessionDriver capability for harnesses
// that cannot pin a session id at launch (codex, opencode): the supervisor
// calls it before every spawn to rewrite the launch argv from the currently
// stored session id — adding resume tokens once a post-hoc-discovered id
// exists, and stripping stale ones when the store was cleared. storedID ==
// "" must return argv with no session tokens.
type SessionArgsRefresher interface {
	RefreshSessionArgs(args []string, storedID string) []string
}

// TurnAborter is an optional SessionDriver capability for harnesses that
// can cancel an in-flight injected turn.
type TurnAborter interface {
	AbortTurn(h SessionHandle) error // cancel the in-flight injected turn, if any
}
