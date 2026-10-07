package bridge

import "encoding/json"

// Attention states an EventAttention reports.
const (
	AttentionNeedsInput = "needs_input"
	AttentionCleared    = "cleared"
)

// Attention kinds: what the agent is waiting on.
const (
	AttentionPermission  = "permission"
	AttentionQuestion    = "question"
	AttentionElicitation = "elicitation"
)

// Compaction phases and triggers an EventCompact reports.
const (
	CompactStarted   = "started"
	CompactCompleted = "completed"
	CompactFailed    = "failed"

	CompactManual = "manual"
	CompactAuto   = "auto"
)

var (
	attentionStates = map[string]bool{AttentionNeedsInput: true, AttentionCleared: true}
	attentionKinds  = map[string]bool{AttentionPermission: true, AttentionQuestion: true, AttentionElicitation: true}
	compactPhases   = map[string]bool{CompactStarted: true, CompactCompleted: true, CompactFailed: true}
	compactTriggers = map[string]bool{CompactManual: true, CompactAuto: true}
)

// ActivityReport is the main loop's current tool call; both fields empty
// once it resolves. Summary is mod-side display text, untrusted: consumers
// clamp it before publishing.
type ActivityReport struct {
	Tool    string
	Summary string
}

// AttentionReport says the agent is (or no longer is) blocked on the user.
// Kind is required for needs_input and optional for cleared.
type AttentionReport struct {
	State   string
	Kind    string
	Tool    string
	Summary string
}

// SubagentsReport is how many native background subagents are running.
type SubagentsReport struct {
	Running int
}

// CompactReport is one phase of a conversation compaction. Error is only
// valid on a failed phase.
type CompactReport struct {
	Phase   string
	Trigger string
	Error   string
}

// parseObservePayload fills r's payload for an observe event name; other
// names are left alone. Key membership is already checked by eventOnlyKeys.
func parseObservePayload(fields map[string]json.RawMessage, r *Report) error {
	var err error
	switch r.Name {
	case EventActivity:
		r.Activity, err = parseActivity(fields)
	case EventAttention:
		r.Attention, err = parseAttention(fields)
	case EventSubagents:
		r.Subagents, err = parseSubagents(fields)
	case EventCompact:
		r.Compact, err = parseCompact(fields)
	}
	return err
}

func parseActivity(fields map[string]json.RawMessage) (*ActivityReport, error) {
	tool, err := stringField(fields, "tool", false)
	if err != nil {
		return nil, err
	}
	summary, err := stringField(fields, "summary", false)
	if err != nil {
		return nil, err
	}
	return &ActivityReport{Tool: tool, Summary: summary}, nil
}

func parseAttention(fields map[string]json.RawMessage) (*AttentionReport, error) {
	state, err := enumField(fields, "state", attentionStates, true)
	if err != nil {
		return nil, err
	}
	kind, err := enumField(fields, "kind", attentionKinds, state == AttentionNeedsInput)
	if err != nil {
		return nil, err
	}
	activity, err := parseActivity(fields)
	if err != nil {
		return nil, err
	}
	return &AttentionReport{State: state, Kind: kind, Tool: activity.Tool, Summary: activity.Summary}, nil
}

func parseSubagents(fields map[string]json.RawMessage) (*SubagentsReport, error) {
	raw, present := fields["running"]
	var running int
	if !present || isNull(raw) || json.Unmarshal(raw, &running) != nil || running < 0 {
		return nil, invalidReport("subagents requires running, a count of zero or more")
	}
	return &SubagentsReport{Running: running}, nil
}

func parseCompact(fields map[string]json.RawMessage) (*CompactReport, error) {
	phase, err := enumField(fields, "phase", compactPhases, true)
	if err != nil {
		return nil, err
	}
	trigger, err := enumField(fields, "trigger", compactTriggers, true)
	if err != nil {
		return nil, err
	}
	msg, err := stringField(fields, "error", false)
	if err != nil {
		return nil, err
	}
	if _, present := fields["error"]; present && phase != CompactFailed {
		return nil, invalidReport("compact error is only valid on a failed phase")
	}
	return &CompactReport{Phase: phase, Trigger: trigger, Error: msg}, nil
}

// enumField reads key as a string from allowed. An optional key may be
// absent ("") but must be one of allowed if present.
func enumField(fields map[string]json.RawMessage, key string, allowed map[string]bool, required bool) (string, error) {
	s, err := stringField(fields, key, required)
	if err != nil {
		return "", err
	}
	if _, present := fields[key]; present && !allowed[s] {
		return "", invalidReport("unknown %s %q", key, s)
	}
	return s, nil
}
