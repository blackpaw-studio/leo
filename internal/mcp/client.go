// Package mcp implements the leo MCP server: a stdio JSON-RPC 2.0 server
// that wraps Leo's daemon HTTP API as MCP tools. It is launched by the
// supervised Claude process via --mcp-config, giving every channel plugin
// a uniform slash-command surface.
package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// maxDaemonResponseBytes caps how much response body the MCP client will read
// from the local daemon. The daemon is trusted, so this is a safety net against
// a runaway handler rather than an adversarial boundary.
const maxDaemonResponseBytes = 10 << 20

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
	var reqBody io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request: %w", err)
		}
		reqBody = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, c.baseURL+path, reqBody)
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

func (c *daemonClient) sendKeys(processName string, keys []string) error {
	_, err := c.do(http.MethodPost, "/web/process/"+processName+"/send", map[string]any{"keys": keys})
	return err
}

func (c *daemonClient) interrupt(processName string) error {
	// Interrupt currently returns an HTML flash, not the apiEnvelope. We
	// don't need its body — accept any 2xx as success.
	req, err := http.NewRequest(http.MethodPost, c.baseURL+"/web/process/"+processName+"/interrupt", nil)
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

func (c *daemonClient) stopAgent(name string) (json.RawMessage, error) {
	return c.do(http.MethodPost, "/api/agent/stop", map[string]string{"name": name})
}
