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

	"github.com/blackpaw-studio/leo/internal/leomcp"
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
	// chatAskCallWindow is the longest one leo_chat_ask call waits: its
	// HTTP deadline stays under leo's MCP tool ceiling, past which the
	// harness kills the call. A longer ask comes back pending and is
	// resumed by message_id; its own deadline lives in the daemon.
	chatAskCallWindow = leomcp.ToolTimeout - 2*chatAskHTTPMargin
)

// withTaskRun marks the registry as serving a headless task run (LEO_TASK,
// LEO_RUN_ID), whose chat posts become task cards in the owner's thread.
func withTaskRun(task, runID string) registryOption {
	return func(r *registry) { r.task, r.runID = task, runID }
}

// chatToolRequest is the body of /api/agent/{name}/chat/*; see
// web.chatToolRequest.
type chatToolRequest struct {
	Text      string       `json:"text,omitempty"`
	Files     []string     `json:"files,omitempty"`
	Question  string       `json:"question,omitempty"`
	Options   []chatOption `json:"options,omitempty"`
	Input     *chatInput   `json:"input,omitempty"`
	TimeoutS  float64      `json:"timeout_s,omitempty"`
	WaitS     float64      `json:"wait_s,omitempty"`
	MessageID string       `json:"message_id,omitempty"`
	Limit     int          `json:"limit,omitempty"`
	Before    string       `json:"before,omitempty"`
	Filter    string       `json:"task_filter,omitempty"`
	Task      string       `json:"task,omitempty"`
	RunID     string       `json:"run_id,omitempty"`
}

type chatOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// chatInput is leo_chat_ask's input: how the question is answered.
type chatInput struct {
	Type        string       `json:"type"`
	Options     []chatOption `json:"options,omitempty"`
	AllowText   bool         `json:"allow_text,omitempty"`
	Min         int          `json:"min,omitempty"`
	Max         int          `json:"max,omitempty"`
	Placeholder string       `json:"placeholder,omitempty"`
}

var chatInputTypes = map[string]bool{"buttons": true, "radio": true, "checkboxes": true, "text": true}

const (
	chatPostDescription = "Post a message to the user's Leo Chat thread — the chat app on their phone and Mac. " +
		"Use it for results, questions you do not need answered right away, and files. text is Markdown; files are paths (absolute, or relative to your workspace) copied into the chat, 50 MB each. " +
		"Your reply to a chat message is captured automatically, so call this only for anything else; calling it during a turn suppresses that turn's automatic capture. " +
		"From a scheduled task run the post becomes a task card in the task owner's thread."
	chatAskDescription = "Ask the user a question in Leo Chat and wait for the answer. " +
		"input says how it is answered: {type: buttons|radio|checkboxes|text, options, allow_text, min, max, placeholder}. " +
		"buttons submit on tap; radio picks one; checkboxes pick min–max (default 0 to all); text takes free text. " +
		"options (1–12 strings or {value, label} objects) are required for buttons/radio/checkboxes and forbidden for text; allow_text adds a free-text answer to a choice type. " +
		"Omitting input and giving top-level options asks buttons. To ask about a file, leo_chat_post it first, then ask. timeout_s defaults to 300 (max 3600). " +
		"Returns {values, labels, text, timed_out, message_id}; on timeout values is empty and timed_out is true. " +
		"One call waits at most 30 minutes: if the question is still open then, it returns pending: true — call leo_chat_ask again with only that message_id to keep waiting."
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
			"message_id": map[string]any{"type": "string", "description": "Resume a pending ask (from an earlier call's result) instead of asking."},
			"question":   map[string]any{"type": "string"},
			"options":    chatOptionsSchema("Shorthand for input {type: buttons, options}. Not with input."),
			"input": map[string]any{"type": "object", "description": "How the question is answered.", "properties": map[string]any{
				"type":        map[string]any{"type": "string", "enum": []string{"buttons", "radio", "checkboxes", "text"}},
				"options":     chatOptionsSchema("Choices (buttons, radio, checkboxes); not for text."),
				"allow_text":  map[string]any{"type": "boolean", "description": "Choice types: also accept a free-text answer."},
				"min":         map[string]any{"type": "integer", "minimum": 0, "description": "Checkboxes: fewest choices (default 0)."},
				"max":         map[string]any{"type": "integer", "minimum": 1, "description": "Checkboxes: most choices (default all)."},
				"placeholder": map[string]any{"type": "string", "description": "Hint in the text field."},
			}, "required": []string{"type"}},
			"timeout_s": map[string]any{"type": "integer", "minimum": 1, "maximum": int(maxChatAskTimeout.Seconds()), "description": "Seconds the question stays open (default 300)."},
		}),
	}, func(ctx context.Context, args map[string]any) (string, error) {
		if r.dispatchID != "" {
			return "", refuse("leo_chat_ask")
		}
		req, err := r.parseChatAsk(args)
		if err != nil {
			return "", err
		}
		wait := time.Duration(req.WaitS) * time.Second
		return client.chatCall(ctx, processName, "ask", req, chatAskHTTPTimeout(wait))
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

func chatOptionsSchema(description string) map[string]any {
	return map[string]any{"type": "array", "minItems": 1, "maxItems": maxChatAskOptions, "description": description, "items": map[string]any{"anyOf": []any{
		map[string]any{"type": "string"},
		map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string"}, "label": map[string]any{"type": "string"}}, "required": []string{"value"}},
	}}}
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

// parseChatAsk builds a new ask, or with message_id the resume of a pending
// one. Either way the call waits at most chatAskCallWindow.
func (r *registry) parseChatAsk(args map[string]any) (chatToolRequest, error) {
	req := chatToolRequest{Task: r.task, RunID: r.runID, WaitS: chatAskCallWindow.Seconds()}
	id, err := optionalStringArg(args, "message_id")
	if err != nil {
		return req, err
	}
	if id != "" {
		if _, ok := args["question"]; ok {
			return req, fmt.Errorf("resume an ask with message_id alone")
		}
		for _, key := range []string{"options", "input"} {
			if _, ok := args[key]; ok {
				return req, fmt.Errorf("resume an ask with message_id alone")
			}
		}
		req.MessageID = id
		return req, nil
	}
	question, err := stringArg(args, "question")
	if err != nil {
		return req, err
	}
	req.Question = question
	if err := parseChatAskInput(args, &req); err != nil {
		return req, err
	}
	timeout := defaultChatAskTimeout
	if v, ok := args["timeout_s"]; ok {
		n, ok := v.(float64)
		if !ok || n != math.Trunc(n) || n < 1 || n > maxChatAskTimeout.Seconds() {
			return req, fmt.Errorf("timeout_s must be an integer from 1 to %d", int(maxChatAskTimeout.Seconds()))
		}
		timeout = time.Duration(n) * time.Second
	}
	req.TimeoutS = timeout.Seconds()
	req.WaitS = min(timeout, chatAskCallWindow).Seconds()
	return req, nil
}

// parseChatAskInput fills req's input from args: the input object, or the
// top-level options shorthand for buttons (the daemon maps it).
func parseChatAskInput(args map[string]any, req *chatToolRequest) error {
	rawInput, hasInput := args["input"]
	_, hasOptions := args["options"]
	switch {
	case hasInput && hasOptions:
		return fmt.Errorf("give input or options, not both")
	case hasInput:
		in, err := parseChatInput(rawInput)
		if err != nil {
			return fmt.Errorf("input: %w", err)
		}
		req.Input = &in
		return nil
	}
	options, err := parseChatOptions(args["options"])
	if err != nil {
		return fmt.Errorf("options: %w", err)
	}
	req.Options = options
	return nil
}

func parseChatInput(raw any) (chatInput, error) {
	obj, ok := raw.(map[string]any)
	if !ok {
		return chatInput{}, fmt.Errorf("must be an object")
	}
	var in chatInput
	var err error
	if in.Type, err = optionalStringArg(obj, "type"); err != nil {
		return in, err
	}
	if !chatInputTypes[in.Type] {
		return in, fmt.Errorf("type must be buttons, radio, checkboxes or text")
	}
	if in.Placeholder, err = optionalStringArg(obj, "placeholder"); err != nil {
		return in, err
	}
	if v, ok := obj["allow_text"]; ok {
		if in.AllowText, ok = v.(bool); !ok {
			return in, fmt.Errorf("allow_text must be a boolean")
		}
	}
	if in.Min, err = optionalIntArg(obj, "min"); err != nil {
		return in, err
	}
	if in.Max, err = optionalIntArg(obj, "max"); err != nil {
		return in, err
	}
	if _, ok := obj["options"]; ok {
		if in.Options, err = parseChatOptions(obj["options"]); err != nil {
			return in, fmt.Errorf("options: %w", err)
		}
	}
	return in, nil
}

// parseChatOptions parses 1 to maxChatAskOptions strings or {value, label}.
func parseChatOptions(raw any) ([]chatOption, error) {
	items, _ := raw.([]any)
	if len(items) == 0 || len(items) > maxChatAskOptions {
		return nil, fmt.Errorf("must hold 1 to %d choices", maxChatAskOptions)
	}
	options := make([]chatOption, 0, len(items))
	for i, item := range items {
		opt, err := parseChatOption(item)
		if err != nil {
			return nil, fmt.Errorf("[%d]: %w", i, err)
		}
		options = append(options, opt)
	}
	return options, nil
}

// optionalIntArg returns args[key] when present, requiring an integer.
func optionalIntArg(args map[string]any, key string) (int, error) {
	v, ok := args[key]
	if !ok {
		return 0, nil
	}
	n, ok := v.(float64)
	if !ok || n != math.Trunc(n) || n < 0 || n > maxChatAskOptions {
		return 0, fmt.Errorf("%s must be an integer from 0 to %d", key, maxChatAskOptions)
	}
	return int(n), nil
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
