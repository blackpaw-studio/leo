package harness

import "context"

// DriveStyle says how a SessionDriver keeps a session alive between
// injected messages.
type DriveStyle string

const (
	// DriveTmux means a resident process is supervised in a leo tmux
	// session; Inject pastes into the live pane.
	DriveTmux DriveStyle = "tmux"
	// DriveTurns means there is no resident process; each Inject spawns a
	// one-shot turn.
	DriveTurns DriveStyle = "turns"
)

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
	TurnArgs      []string          // DriveTurns: rendered per-turn argv prefix (from Args())
	OpeningPrompt string            // delivered by Start for drivers that can't put it in argv
	IDs           SessionIDStore
}

// AttachSpec describes how a caller can attach to a live session for
// interactive viewing.
type AttachSpec struct {
	Argv        []string // exec locally, or run verbatim on the remote host over ssh; nil = no live attach
	HistoryPath string   // when Argv is nil: file whose tail is the recent turn history
}

// SessionDriver is the per-harness contract for keeping a live interactive
// session and delivering messages to it. Every SupportsKind-gated
// interactive call site goes through a SessionDriver.
type SessionDriver interface {
	// Style reports how this driver keeps a session alive between turns.
	Style() DriveStyle
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
	// QuickExitNone keeps the args and stored id (e.g. opencode serve
	// crash does not mean a poisoned conversation).
	QuickExitNone
)

// QuickExitRecovery is an optional SessionDriver capability for harnesses
// that need custom recovery behavior on a quick exit.
type QuickExitRecovery interface {
	RecoverQuickExit(args []string) ([]string, QuickExitAction)
}

// TurnAborter is an optional SessionDriver capability for harnesses that
// can cancel an in-flight injected turn.
type TurnAborter interface {
	AbortTurn(h SessionHandle) error // cancel the in-flight injected turn, if any
}
