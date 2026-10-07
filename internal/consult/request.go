package consult

import (
	"fmt"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
)

const preamble = "You are a one-off consultant: another agent is asking for your independent opinion. Analyze and answer directly and completely in your final message. Do not modify any files or take actions beyond reading. The question follows."

const dispatchPreamble = "You are a subagent dispatched by an orchestrator. Do not spawn agents or run your own code review; the orchestrator reviews your work. When the orchestrator has finished with you, it releases this pane."

type Request struct {
	Template string
	Model    string
	Effort   string
	Role     string
	Profile  string
	Prompt   string
	Cwd      string
	Name     string
	Kind     string
	// Timeout caps this run. Zero means unlimited for dispatches and falls
	// back to RunTimeout for consults.
	Timeout  time.Duration
	Preamble bool
	// Caller names the process that asked, for the consult record. Optional.
	Caller          string
	Mode            Mode
	Notify          *bool
	Isolation       string
	CallerPaneID    string
	CallerHarness   string
	CallerSessionID string
	CallerWindowID  string
	// CallerBridgeKey is the bridge key of the claude that asked
	// ($LEO_BRIDGE_AGENT), so its mod can be shown the dispatch. Optional.
	CallerBridgeKey string
	// CallerBridgeLaunch is the launch id (bridge.LaunchID) of that
	// claude's launch, which the key alone does not name: a recreated
	// agent takes its predecessor's key again. Optional.
	CallerBridgeLaunch string
}

type Result struct {
	// ID identifies the consult's record and event stream, so a caller can
	// point at it after the fact (`leo consult watch <id>`).
	ID      string `json:"id"`
	Harness string `json:"harness"`
	Model   string `json:"model"`
	Text    string `json:"text"`
}

type Started struct {
	ID      string `json:"id"`
	Harness string `json:"harness"`
	Model   string `json:"model"`
	Cwd     string `json:"cwd"`
	// Placement, Pane, and Window describe where an interactive dispatch's
	// TUI actually landed: Placement is "split" (a pane in the caller's own
	// tmux window) or "window" (a separate tmux window, the fallback), Pane
	// is the tmux pane id, and Window is the pane/window label.
	Placement string `json:"placement,omitempty"`
	Pane      string `json:"pane,omitempty"`
	Window    string `json:"window,omitempty"`
	// Queued reports an interactive dispatch accepted while every
	// concurrency slot was taken: it has no pane yet and launches, placed as
	// usual, once a slot frees.
	Queued bool `json:"queued,omitempty"`
}

func requestKind(req Request) string {
	if req.Kind != "" {
		return req.Kind
	}
	return "dispatch"
}

// worktreeNotice tells a worktree-isolated run where it is and what it
// cannot see. req.Cwd is the worktree path by the time a prompt is built.
const worktreeNotice = "You are running in a throwaway Git worktree at %s, checked out at the caller's committed HEAD: uncommitted changes in the caller's tree are not visible here, and anything you write stays in this worktree."

// withTemplateIsolation fills an unset request isolation from the
// template's, so the dispatch argument wins whenever it is given.
func withTemplateIsolation(cfg *config.Config, req Request) Request {
	if req.Isolation == "" {
		req.Isolation = cfg.Templates[req.Template].Isolation
	}
	return req
}

func requestPrompt(req Request) string {
	lead := dispatchPreamble
	if req.Preamble {
		lead = preamble
	}
	if req.Isolation == "worktree" {
		lead += " " + fmt.Sprintf(worktreeNotice, req.Cwd)
	}
	if req.Preamble {
		return lead + "\n\n" + req.Prompt
	}
	return lead + " " + req.Prompt
}
