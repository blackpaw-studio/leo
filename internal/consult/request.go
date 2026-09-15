package consult

import "time"

const preamble = "You are a one-off consultant: another agent is asking for your independent opinion. Analyze and answer directly and completely in your final message. Do not modify any files or take actions beyond reading. The question follows."

type Request struct {
	Template string
	Model    string
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
	Window  string `json:"window,omitempty"`
}

func requestKind(req Request) string {
	if req.Kind != "" {
		return req.Kind
	}
	return "dispatch"
}

func requestPrompt(req Request) string {
	if req.Preamble {
		return preamble + "\n\n" + req.Prompt
	}
	return req.Prompt
}
