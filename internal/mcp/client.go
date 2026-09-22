// Package mcp implements the leo MCP server: a stdio JSON-RPC 2.0 server
// that wraps Leo's daemon HTTP API as MCP tools. It is launched by the
// supervised Claude process via --mcp-config, giving every channel plugin
// a uniform slash-command surface.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/consult"
)

// maxDaemonResponseBytes caps how much response body the MCP client will read
// from the local daemon. The daemon is trusted, so this is a safety net against
// a runaway handler rather than an adversarial boundary.
const maxDaemonResponseBytes = 10 << 20

// consultHTTPTimeout is slightly longer than the daemon's own consultant
// deadline so the daemon can return its structured timeout error. Derived
// from that deadline rather than restated, so the two can't drift apart.
const consultHTTPTimeout = consult.RunTimeout + time.Minute

// daemonClient calls the Leo daemon's TCP HTTP API on 127.0.0.1.
type daemonClient struct {
	baseURL string
	token   string // API bearer token; empty disables the Authorization header
	http    *http.Client
}

func newDaemonClient(port, token string) *daemonClient {
	return &daemonClient{
		baseURL: "http://127.0.0.1:" + port,
		token:   token,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// setAuth sets the Authorization: Bearer header when the client has a token.
// Kept as a helper so every request path (including raw-response callers like
// interrupt) goes through a single code path.
func (c *daemonClient) setAuth(req *http.Request) {
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
}

// apiEnvelope mirrors web.apiResponse — kept local to avoid an import cycle
// (internal/web depends on more than we want to drag into the MCP server).
type apiEnvelope struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error string          `json:"error,omitempty"`
}

func (c *daemonClient) do(method, path string, body any) (json.RawMessage, error) {
	return c.doContext(context.Background(), method, path, body)
}

func (c *daemonClient) doContext(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	var reqBody io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request: %w", err)
		}
		reqBody = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reqBody)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c.setAuth(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call daemon: %w", err)
	}
	defer resp.Body.Close()

	// Cap the response body to a generous 10 MiB. The daemon is local and
	// trusted, so this is belt-and-suspenders against a runaway handler —
	// list responses scale with the number of agents/tasks.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxDaemonResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	var env apiEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("decode response (status %d): %w", resp.StatusCode, err)
	}
	if !env.OK || env.Error != "" {
		if env.Error != "" {
			return nil, errors.New(env.Error)
		}
		return nil, fmt.Errorf("daemon returned status %d", resp.StatusCode)
	}
	return env.Data, nil
}

func (c *daemonClient) sendKeys(agentName string, keys []string) error {
	_, err := c.do(http.MethodPost, "/web/agent/"+agentName+"/send", map[string]any{"keys": keys})
	return err
}

func (c *daemonClient) interrupt(agentName string) error {
	// Interrupt currently returns an HTML flash, not the apiEnvelope. We
	// don't need its body — accept any 2xx as success.
	req, err := http.NewRequest(http.MethodPost, c.baseURL+"/web/agent/"+agentName+"/interrupt", nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	c.setAuth(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("call daemon: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("interrupt returned status %d", resp.StatusCode)
	}
	return nil
}

func (c *daemonClient) listTasks() (json.RawMessage, error) {
	return c.do(http.MethodGet, "/api/task/list", nil)
}

func (c *daemonClient) runTask(name string) (json.RawMessage, error) {
	return c.do(http.MethodPost, "/api/task/"+name+"/run", nil)
}

func (c *daemonClient) toggleTask(name string) (json.RawMessage, error) {
	return c.do(http.MethodPost, "/api/task/"+name+"/toggle", nil)
}

func (c *daemonClient) listTemplates() (json.RawMessage, error) {
	return c.do(http.MethodGet, "/api/template/list", nil)
}

func (c *daemonClient) spawnAgent(template, repo, name string) (json.RawMessage, error) {
	body := map[string]string{"template": template, "repo": repo}
	if name != "" {
		body["name"] = name
	}
	return c.do(http.MethodPost, "/api/agent/spawn", body)
}

func (c *daemonClient) listAgents() (json.RawMessage, error) {
	return c.do(http.MethodGet, "/api/agent/list", nil)
}

// sendMessage delivers text to target. from is the sending process's own
// name, sent as a structural field so the daemon can report agent-to-agent
// activity on the observability API without inspecting the message body; it
// is self-asserted and must not be treated as an authenticated identity.
// Empty from is omitted, leaving the daemon to report an unknown sender.
func (c *daemonClient) sendMessage(target, from, text string) error {
	body := map[string]any{"text": text}
	if from != "" {
		body["from"] = from
	}
	_, err := c.do(http.MethodPost, "/web/agent/"+target+"/message", body)
	return err
}

func (c *daemonClient) stopAgent(name string) (json.RawMessage, error) {
	return c.do(http.MethodPost, "/api/agent/stop", map[string]string{"name": name})
}

// consult runs a one-off consultant via the daemon and waits for its answer.
func (c *daemonClient) consult(ctx context.Context, from, template, model, prompt string) (json.RawMessage, error) {
	body := map[string]string{"from": from, "template": template, "prompt": prompt}
	if model != "" {
		body["model"] = model
	}
	client := *c
	client.http = &http.Client{Timeout: consultHTTPTimeout}
	return client.doContext(ctx, http.MethodPost, "/api/consult", body)
}

func (c *daemonClient) dispatch(ctx context.Context, request consult.Request) (consult.Started, error) {
	body := map[string]any{"from": request.Caller, "prompt": request.Prompt, "cwd": request.Cwd}
	if request.Template != "" && request.Role == "" {
		body["template"] = request.Template
	}
	if request.Role != "" {
		body["role"] = request.Role
	}
	if request.Template != "" && request.Role != "" {
		body["expect_template"] = request.Template
	}
	if request.Model != "" {
		body["model"] = request.Model
	}
	if request.Effort != "" {
		body["effort"] = request.Effort
	}
	if request.Name != "" {
		body["name"] = request.Name
	}
	if request.Timeout > 0 {
		body["timeout_seconds"] = request.Timeout.Seconds()
	}
	if request.Mode != "" {
		body["mode"] = request.Mode
	}
	if request.Notify != nil {
		body["notify"] = *request.Notify
	}
	if request.Isolation != "" {
		body["isolation"] = request.Isolation
	}
	if request.CallerPaneID != "" {
		body["caller_pane_id"] = request.CallerPaneID
	}
	raw, err := c.doContext(ctx, http.MethodPost, "/api/dispatch", body)
	if err != nil {
		return consult.Started{}, err
	}
	var started consult.Started
	if err := json.Unmarshal(raw, &started); err != nil {
		return consult.Started{}, fmt.Errorf("decode dispatch: %w", err)
	}
	return started, nil
}

func (c *daemonClient) resolveRole(ctx context.Context, role string) (config.Resolution, error) {
	raw, err := c.doContext(ctx, http.MethodGet, "/api/delegation/resolve?role="+url.QueryEscape(role), nil)
	if err != nil {
		return config.Resolution{}, err
	}
	var resolved config.Resolution
	if err := json.Unmarshal(raw, &resolved); err != nil {
		return config.Resolution{}, fmt.Errorf("decode delegation role: %w", err)
	}
	return resolved, nil
}

func (c *daemonClient) delegationStatus(ctx context.Context) (string, error) {
	raw, err := c.doContext(ctx, http.MethodGet, "/api/delegation", nil)
	if err != nil {
		return "", err
	}
	var status string
	if err := json.Unmarshal(raw, &status); err != nil {
		return "", fmt.Errorf("decode delegation status: %w", err)
	}
	return status, nil
}

func (c *daemonClient) sendDispatch(ctx context.Context, id, message string) (consult.SendResult, error) {
	raw, err := c.doContext(ctx, http.MethodPost, "/api/dispatch/"+url.PathEscape(id)+"/send", map[string]string{"message": message})
	if err != nil {
		return consult.SendResult{}, err
	}
	var result consult.SendResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return result, fmt.Errorf("decode dispatch send: %w", err)
	}
	return result, nil
}

func (c *daemonClient) getDispatch(ctx context.Context, id string) (consult.Record, error) {
	raw, err := c.doContext(ctx, http.MethodGet, "/api/dispatch/"+url.PathEscape(id), nil)
	if err != nil {
		return consult.Record{}, err
	}
	var record consult.Record
	if err := json.Unmarshal(raw, &record); err != nil {
		return consult.Record{}, fmt.Errorf("decode dispatch: %w", err)
	}
	return record, nil
}

func (c *daemonClient) dispatchOutput(ctx context.Context, id string, tail int) (consult.Output, error) {
	raw, err := c.doContext(ctx, http.MethodGet, "/api/dispatch/"+url.PathEscape(id)+"/output?tail="+fmt.Sprintf("%d", tail), nil)
	if err != nil {
		return consult.Output{}, err
	}
	var output consult.Output
	if err := json.Unmarshal(raw, &output); err != nil {
		return consult.Output{}, fmt.Errorf("decode dispatch output: %w", err)
	}
	return output, nil
}

func (c *daemonClient) waitDispatch(ctx context.Context, ids []string, timeout time.Duration) ([]consult.Entry, error) {
	query := url.Values{}
	for _, id := range ids {
		query.Add("id", id)
	}
	query.Set("timeout", fmt.Sprintf("%g", timeout.Seconds()))
	client := *c
	client.http = &http.Client{Timeout: dispatchWaitHTTPTimeout(timeout)}
	raw, err := client.doContext(ctx, http.MethodGet, "/api/dispatch/wait?"+query.Encode(), nil)
	if err != nil {
		return nil, err
	}
	var entries []consult.Entry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("decode dispatch wait: %w", err)
	}
	return entries, nil
}

func dispatchWaitHTTPTimeout(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		return consultHTTPTimeout
	}
	return timeout + time.Minute
}

func (c *daemonClient) cancelDispatch(ctx context.Context, id string) (consult.Record, error) {
	raw, err := c.doContext(ctx, http.MethodPost, "/api/dispatch/"+url.PathEscape(id)+"/cancel", nil)
	if err != nil {
		return consult.Record{}, err
	}
	var record consult.Record
	if err := json.Unmarshal(raw, &record); err != nil {
		return consult.Record{}, fmt.Errorf("decode dispatch: %w", err)
	}
	return record, nil
}

func (c *daemonClient) releaseDispatch(ctx context.Context, id string) (consult.Record, error) {
	raw, err := c.doContext(ctx, http.MethodPost, "/api/dispatch/"+url.PathEscape(id)+"/release", nil)
	if err != nil {
		return consult.Record{}, err
	}
	var record consult.Record
	if err := json.Unmarshal(raw, &record); err != nil {
		return consult.Record{}, fmt.Errorf("decode dispatch: %w", err)
	}
	return record, nil
}
