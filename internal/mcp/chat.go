package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"path/filepath"
	"time"
)

// Leo Chat tool limits. The daemon re-validates every one of them; checking
// here too turns a bad call into a precise error before a long-poll starts.
const (
	maxChatAskOptions     = 12
	defaultChatAskTimeout = 300 * time.Second
	maxChatAskTimeout     = time.Hour
	maxChatHistoryLimit   = 200
	maxChatPostFiles      = 10
	// chatAskHTTPMargin lets the daemon answer a timed-out ask before the
	// HTTP client gives up on it.
	chatAskHTTPMargin = time.Minute
)

// withTaskRun marks the registry as serving a headless task run (LEO_TASK,
// LEO_RUN_ID), whose chat posts become task cards in the owner's thread.
func withTaskRun(task, runID string) registryOption {
	return func(r *registry) { r.task, r.runID = task, runID }
}

// chatToolRequest is the body of /api/agent/{name}/chat/*; see
// web.chatToolRequest.
type chatToolRequest struct {
	Text     string       `json:"text,omitempty"`
	Files    []string     `json:"files,omitempty"`
	Question string       `json:"question,omitempty"`
	Options  []chatOption `json:"options,omitempty"`
	TimeoutS float64      `json:"timeout_s,omitempty"`
	Limit    int          `json:"limit,omitempty"`
	Before   string       `json:"before,omitempty"`
	Filter   string       `json:"task_filter,omitempty"`
	Task     string       `json:"task,omitempty"`
	RunID    string       `json:"run_id,omitempty"`
}

type chatOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

const (
	chatPostDescription = "Post a message to the user's Leo Chat thread — the chat app on their phone and Mac. " +
		"Use it for results, questions you do not need answered right away, and files. text is Markdown; files are paths (absolute, or relative to your workspace) copied into the chat, 50 MB each. " +
		"Your reply to a chat message is captured automatically, so call this only for anything else; calling it during a turn suppresses that turn's automatic capture. " +
		"From a scheduled task run the post becomes a task card in the task owner's thread."
	chatAskDescription = "Ask the user a multiple-choice question in Leo Chat and wait for the answer. " +
		"options are 1–12 strings or {value, label} objects; timeout_s defaults to 300 (max 3600). " +
		"Returns {value, label, timed_out}; on timeout value is empty and timed_out is true."
	chatHistoryDescription = "Read your Leo Chat thread, newest first: limit (default 50, max 200), before (a message id, for older pages), task (only that task's cards)."
)

// registerChat adds the leo_chat_* tools. Dispatch subagents are refused in
// every handler: they inherit their orchestrator's process name and would
// otherwise write into its thread.
func (r *registry) registerChat(client *daemonClient, processName string, schema func(map[string]any, ...string) map[string]any) {
	refuse := func(tool string) error {
		return fmt.Errorf("%s is only available to supervised Leo agents and task runs, not to a leo_dispatch subagent (dispatch %s); report to your orchestrator instead", tool, r.dispatchID)
	}
	r.addContext(toolDef{
		Name:        "leo_chat_post",
		Description: chatPostDescription,
		InputSchema: schema(map[string]any{
			"text":  map[string]any{"type": "string", "description": "Message text (Markdown)."},
			"files": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": maxChatPostFiles, "description": "Files to attach."},
		}),
	}, func(ctx context.Context, args map[string]any) (string, error) {
		if r.dispatchID != "" {
			return "", refuse("leo_chat_post")
		}
		req, err := r.parseChatPost(args)
		if err != nil {
			return "", err
		}
		return client.chatCall(ctx, processName, "post", req, 0)
	})
	r.addContext(toolDef{
		Name:        "leo_chat_ask",
		Description: chatAskDescription,
		InputSchema: schema(map[string]any{
			"question": map[string]any{"type": "string"},
			"options": map[string]any{"type": "array", "minItems": 1, "maxItems": maxChatAskOptions, "items": map[string]any{"anyOf": []any{
				map[string]any{"type": "string"},
				map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string"}, "label": map[string]any{"type": "string"}}, "required": []string{"value"}},
			}}},
			"timeout_s": map[string]any{"type": "integer", "minimum": 1, "maximum": int(maxChatAskTimeout.Seconds()), "description": "Seconds to wait (default 300)."},
		}, "question", "options"),
	}, func(ctx context.Context, args map[string]any) (string, error) {
		if r.dispatchID != "" {
			return "", refuse("leo_chat_ask")
		}
		req, timeout, err := r.parseChatAsk(args)
		if err != nil {
			return "", err
		}
		return client.chatCall(ctx, processName, "ask", req, chatAskHTTPTimeout(timeout))
	})
	r.addContext(toolDef{
		Name:        "leo_chat_history",
		Description: chatHistoryDescription,
		InputSchema: schema(map[string]any{
			"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": maxChatHistoryLimit},
			"before": map[string]any{"type": "string", "description": "Message id; returns older messages."},
			"task":   map[string]any{"type": "string", "description": "Only this task's cards."},
		}),
	}, func(ctx context.Context, args map[string]any) (string, error) {
		if r.dispatchID != "" {
			return "", refuse("leo_chat_history")
		}
		req, err := r.parseChatHistory(args)
		if err != nil {
			return "", err
		}
		return client.chatCall(ctx, processName, "history", req, 0)
	})
}

func (r *registry) parseChatPost(args map[string]any) (chatToolRequest, error) {
	req := chatToolRequest{Task: r.task, RunID: r.runID}
	text, err := optionalStringArg(args, "text")
	if err != nil {
		return req, err
	}
	req.Text = text
	if raw, ok := args["files"]; ok {
		files, err := stringListArg(map[string]any{"files": raw}, "files")
		if err != nil {
			return req, err
		}
		if len(files) > maxChatPostFiles {
			return req, fmt.Errorf("at most %d files", maxChatPostFiles)
		}
		for _, f := range files {
			abs, err := filepath.Abs(f)
			if err != nil {
				return req, fmt.Errorf("resolving %q: %w", f, err)
			}
			req.Files = append(req.Files, abs)
		}
	}
	if req.Text == "" && len(req.Files) == 0 {
		return req, fmt.Errorf("leo_chat_post needs text or files")
	}
	return req, nil
}

func (r *registry) parseChatAsk(args map[string]any) (chatToolRequest, time.Duration, error) {
	req := chatToolRequest{Task: r.task, RunID: r.runID}
	question, err := stringArg(args, "question")
	if err != nil {
		return req, 0, err
	}
	req.Question = question
	raw, _ := args["options"].([]any)
	if len(raw) == 0 || len(raw) > maxChatAskOptions {
		return req, 0, fmt.Errorf("options must hold 1 to %d choices", maxChatAskOptions)
	}
	for i, item := range raw {
		opt, err := parseChatOption(item)
		if err != nil {
			return req, 0, fmt.Errorf("options[%d]: %w", i, err)
		}
		req.Options = append(req.Options, opt)
	}
	timeout := defaultChatAskTimeout
	if v, ok := args["timeout_s"]; ok {
		n, ok := v.(float64)
		if !ok || n != math.Trunc(n) || n < 1 || n > maxChatAskTimeout.Seconds() {
			return req, 0, fmt.Errorf("timeout_s must be an integer from 1 to %d", int(maxChatAskTimeout.Seconds()))
		}
		timeout = time.Duration(n) * time.Second
	}
	req.TimeoutS = timeout.Seconds()
	return req, timeout, nil
}

func parseChatOption(item any) (chatOption, error) {
	switch v := item.(type) {
	case string:
		if v == "" {
			return chatOption{}, fmt.Errorf("must not be empty")
		}
		return chatOption{Value: v, Label: v}, nil
	case map[string]any:
		value, _ := v["value"].(string)
		label, _ := v["label"].(string)
		if value == "" {
			return chatOption{}, fmt.Errorf("value must be a non-empty string")
		}
		if label == "" {
			label = value
		}
		return chatOption{Value: value, Label: label}, nil
	default:
		return chatOption{}, fmt.Errorf("must be a string or {value, label}")
	}
}

func (r *registry) parseChatHistory(args map[string]any) (chatToolRequest, error) {
	req := chatToolRequest{Task: r.task, RunID: r.runID}
	if v, ok := args["limit"]; ok {
		n, ok := v.(float64)
		if !ok || n != math.Trunc(n) || n < 1 || n > maxChatHistoryLimit {
			return req, fmt.Errorf("limit must be an integer from 1 to %d", maxChatHistoryLimit)
		}
		req.Limit = int(n)
	}
	var err error
	if req.Before, err = optionalStringArg(args, "before"); err != nil {
		return req, err
	}
	if req.Filter, err = optionalStringArg(args, "task"); err != nil {
		return req, err
	}
	return req, nil
}

// optionalStringArg returns args[key] when present, requiring a string.
func optionalStringArg(args map[string]any, key string) (string, error) {
	v, ok := args[key]
	if !ok {
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("argument %q must be a string", key)
	}
	return s, nil
}

// chatAskHTTPTimeout bounds an ask's long-poll: the ask's own timeout plus
// room for the daemon to report it.
func chatAskHTTPTimeout(ask time.Duration) time.Duration { return ask + chatAskHTTPMargin }

// chatCall posts a chat tool request and returns the daemon's data verbatim.
// httpTimeout > 0 replaces the client's default deadline (for asks).
func (c *daemonClient) chatCall(ctx context.Context, agentName, verb string, req chatToolRequest, httpTimeout time.Duration) (string, error) {
	client := *c
	if httpTimeout > 0 {
		client.http = &http.Client{Timeout: httpTimeout}
	}
	data, err := client.doContext(ctx, http.MethodPost, "/api/agent/"+url.PathEscape(agentName)+"/chat/"+verb, req)
	if err != nil {
		return "", err
	}
	if !json.Valid(data) {
		return "", fmt.Errorf("unexpected chat %s response: %s", verb, data)
	}
	return string(data), nil
}
