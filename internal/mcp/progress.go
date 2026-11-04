package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
)

const progressDescription = "Advertise progress of a long-running task (a build, a deploy, a test sweep) so the user sees it next to you in Leo. " +
	"id is your own stable key for the task (a-z, 0-9, . _ -; up to 64); the first call for an id needs title (up to 80 characters). " +
	"Later calls change only what you pass: percent (0-100) for a determinate bar, or indeterminate: true when you cannot say how far along it is; " +
	"status is a short line (up to 120), e.g. \"Compiling 412/980\"; eta_seconds is how long you expect it to take from now. " +
	"End the task with state: succeeded, failed or cancelled; a failed task stays visible until the user dismisses it. " +
	"You may have at most 8 running tasks, and a task you stop updating is marked stale after 15 minutes. " +
	"Returns the task as stored."

// progressStates are the task states leo_progress accepts.
var progressStates = []any{"running", "succeeded", "failed", "cancelled"}

// withAttentionToken gives the registry this launch's attention token
// (LEO_ATTENTION_TOKEN), the only identity leo_progress reports under.
func withAttentionToken(token string) registryOption {
	return func(r *registry) { r.attentionToken = token }
}

// registerProgress adds leo_progress. The request carries the launch token
// and never an agent name: the daemon decides whose task it is from the
// token alone, so no argument can address another agent.
func (r *registry) registerProgress(client *daemonClient, schema func(map[string]any, ...string) map[string]any) {
	r.addContext(toolDef{
		Name:        "leo_progress",
		Description: progressDescription,
		InputSchema: schema(map[string]any{
			"id":            map[string]any{"type": "string", "description": "Your key for this task."},
			"title":         map[string]any{"type": "string", "description": "Task title; required on the first call for an id."},
			"percent":       map[string]any{"type": "number", "minimum": 0, "maximum": 100},
			"indeterminate": map[string]any{"type": "boolean", "description": "Show an indeterminate bar instead of a percentage. Not with percent."},
			"status":        map[string]any{"type": "string", "description": "Short status line."},
			"eta_seconds":   map[string]any{"type": "number", "minimum": 0, "description": "Expected seconds remaining."},
			"state":         map[string]any{"type": "string", "enum": progressStates, "description": "running (default), or the task's end state."},
		}, "id"),
	}, func(ctx context.Context, args map[string]any) (string, error) {
		if r.dispatchID != "" {
			return "", fmt.Errorf("leo_progress is only available to supervised Leo agents, not to a leo_dispatch subagent (dispatch %s); report to your orchestrator instead", r.dispatchID)
		}
		if r.attentionToken == "" {
			return "", fmt.Errorf("leo_progress needs this agent's launch token (LEO_ATTENTION_TOKEN), which is not set: only agents Leo launched with hooks can report progress")
		}
		req, err := parseProgressArgs(args)
		if err != nil {
			return "", err
		}
		req["token"] = r.attentionToken
		data, err := client.doContext(ctx, http.MethodPost, "/api/agent/progress", req)
		if err != nil {
			return "", err
		}
		if !json.Valid(data) {
			return "", fmt.Errorf("unexpected progress response: %s", data)
		}
		return string(data), nil
	})
}

// parseProgressArgs validates leo_progress's arguments into the request
// body (minus the token). Only the arguments the model passed are sent, so
// the daemon's omitted-keeps-value upsert applies.
func parseProgressArgs(args map[string]any) (map[string]any, error) {
	id, err := stringArg(args, "id")
	if err != nil {
		return nil, err
	}
	req := map[string]any{"id": id}
	for _, key := range []string{"title", "status"} {
		if _, ok := args[key]; !ok {
			continue
		}
		s, err := optionalStringArg(args, key)
		if err != nil {
			return nil, err
		}
		req[key] = s
	}
	for _, key := range []string{"percent", "eta_seconds"} {
		v, ok := args[key]
		if !ok {
			continue
		}
		n, ok := v.(float64)
		if !ok || math.IsNaN(n) || math.IsInf(n, 0) || (key == "eta_seconds" && n < 0) {
			return nil, fmt.Errorf("argument %q must be a non-negative number", key)
		}
		req[key] = n
	}
	if v, ok := args["indeterminate"]; ok {
		b, ok := v.(bool)
		if !ok {
			return nil, fmt.Errorf("argument %q must be a boolean", "indeterminate")
		}
		req["indeterminate"] = b
	}
	if b, _ := req["indeterminate"].(bool); b {
		if _, has := req["percent"]; has {
			return nil, fmt.Errorf("percent and indeterminate are mutually exclusive")
		}
	}
	if v, ok := args["state"]; ok {
		s, ok := v.(string)
		if !ok || !containsAny(progressStates, s) {
			return nil, fmt.Errorf("argument %q must be one of running, succeeded, failed, cancelled", "state")
		}
		req["state"] = s
	}
	return req, nil
}

func containsAny(values []any, s string) bool {
	for _, v := range values {
		if v == s {
			return true
		}
	}
	return false
}

// reportsProgress reports whether this server runs in an agent that can call
// leo_progress: one Leo launched (it holds a launch token) and not a dispatch
// run, which inherits its caller's token but cannot report.
func (r *registry) reportsProgress() bool {
	return r.attentionToken != "" && r.dispatchID == ""
}
