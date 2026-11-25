package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/blackpaw-studio/leo/internal/brief"
)

const briefDescription = "Keep a short brief of your current work for the user. Replaces the whole brief; call with no arguments to clear it. " +
	"Update only when goal, status/next step, decisions or open items change."

// briefToolName is the tool registerBrief adds.
const briefToolName = "leo_brief"

// withBrief sets whether leo_brief is offered at launch (LEO_BRIEF=1, the
// daemon's brief.enabled when the agent started). A brief_config_changed
// event moves it afterwards.
func withBrief(on bool) registryOption {
	return func(r *registry) { r.briefOn.Store(on) }
}

// registerBrief adds leo_brief. The request carries the launch token and
// never an agent name: the daemon decides whose brief it is from the token
// alone. The tool is listed only while briefs are on (see visibleDefs), and
// the handler refuses while they are off, for a harness that listed it
// earlier and never re-lists.
func (r *registry) registerBrief(client *daemonClient, schema func(map[string]any, ...string) map[string]any) {
	line := map[string]any{"type": "string", "maxLength": brief.MaxLineRunes}
	list := map[string]any{"type": "array", "maxItems": brief.MaxItems, "items": line}
	r.addContext(toolDef{
		Name:        briefToolName,
		Description: briefDescription,
		InputSchema: schema(map[string]any{"goal": line, "status": line, "decisions": list, "open": list}),
	}, func(ctx context.Context, args map[string]any) (string, error) {
		if r.dispatchID != "" {
			return "", fmt.Errorf("leo_brief is only available to supervised Leo agents, not to a leo_dispatch subagent (dispatch %s)", r.dispatchID)
		}
		if r.attentionToken == "" {
			return "", fmt.Errorf("leo_brief needs this agent's launch token (LEO_ATTENTION_TOKEN), which is not set: only agents Leo launched with hooks can keep a brief")
		}
		if !r.briefOn.Load() {
			return "", fmt.Errorf("agent briefs are off on this machine (brief.enabled in leo.yaml)")
		}
		in, err := parseBriefArgs(args)
		if err != nil {
			return "", err
		}
		data, err := client.doContext(ctx, http.MethodPost, "/api/agent/brief", map[string]any{"token": r.attentionToken, "brief": in})
		if err != nil {
			return "", err
		}
		if !json.Valid(data) {
			return "", fmt.Errorf("unexpected brief response: %s", data)
		}
		if string(data) == "null" {
			return "Brief cleared.", nil
		}
		return string(data), nil
	})
}

// parseBriefArgs checks leo_brief's argument types into the brief object to
// send. Lengths and counts are the daemon's to judge: it answers with the
// field and the limit.
func parseBriefArgs(args map[string]any) (map[string]any, error) {
	out := map[string]any{}
	for _, key := range []string{"goal", "status"} {
		v, ok := args[key]
		if !ok {
			continue
		}
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("argument %q must be a string", key)
		}
		out[key] = s
	}
	for _, key := range []string{"decisions", "open"} {
		v, ok := args[key]
		if !ok {
			continue
		}
		raw, ok := v.([]any)
		if !ok {
			return nil, fmt.Errorf("argument %q must be an array of strings", key)
		}
		items := make([]string, 0, len(raw))
		for _, item := range raw {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("argument %q must be an array of strings", key)
			}
			items = append(items, s)
		}
		out[key] = items
	}
	return out, nil
}

// reportsBrief reports whether this server runs in an agent that can call
// leo_brief: a launch with a token, not a dispatch run.
func (r *registry) reportsBrief() bool {
	return r.attentionToken != "" && r.dispatchID == ""
}

// visibleDefs is the registered tools minus leo_brief wherever it is not
// offered: while briefs are off, and in a server that could not call it.
func (r *registry) visibleDefs() []toolDef {
	if r.briefOn.Load() && r.reportsBrief() {
		return r.defs
	}
	out := make([]toolDef, 0, len(r.defs))
	for _, d := range r.defs {
		if d.Name != briefToolName {
			out = append(out, d)
		}
	}
	return out
}

// setBrief records the daemon's brief switch and, when it changed and the
// session can be told, notifies the harness to re-list its tools.
func (r *registry) setBrief(enabled bool) {
	if r.briefOn.Swap(enabled) == enabled || !r.reportsBrief() {
		return
	}
	r.watch.mu.Lock()
	notify := r.watch.notify
	r.watch.mu.Unlock()
	if notify != nil {
		notify(listChangedMethod)
	}
}
