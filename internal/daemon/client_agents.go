package daemon

import (
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/blackpaw-studio/leo/internal/agent"
)

// AgentSpawn sends POST /agents/spawn to the daemon and returns the new record.
func AgentSpawn(workDir string, req AgentSpawnRequest) (agent.Record, error) {
	resp, err := Send(workDir, "POST", "/agents/spawn", req)
	if err != nil {
		return agent.Record{}, err
	}
	if !resp.OK {
		return agent.Record{}, fmt.Errorf("%s", resp.Error)
	}
	var rec agent.Record
	if err := json.Unmarshal(resp.Data, &rec); err != nil {
		return agent.Record{}, fmt.Errorf("decoding spawn response: %w", err)
	}
	return rec, nil
}

// AgentList sends GET /agents/list to the daemon.
func AgentList(workDir string) ([]agent.Record, error) {
	resp, err := Send(workDir, "GET", "/agents/list", nil)
	if err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, fmt.Errorf("%s", resp.Error)
	}
	var records []agent.Record
	if err := json.Unmarshal(resp.Data, &records); err != nil {
		return nil, fmt.Errorf("decoding list response: %w", err)
	}
	return records, nil
}

// AgentStop sends POST /agents/{name}/stop to the daemon.
func AgentStop(workDir, name string) error {
	resp, err := Send(workDir, "POST", "/agents/"+url.PathEscape(name)+"/stop", nil)
	if err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("%s", resp.Error)
	}
	return nil
}

// AgentLogs sends GET /agents/{name}/logs?lines=N to the daemon.
// Pass lines<=0 to request the default tail.
func AgentLogs(workDir, name string, lines int) (string, error) {
	path := "/agents/" + url.PathEscape(name) + "/logs"
	if lines > 0 {
		path += fmt.Sprintf("?lines=%d", lines)
	}
	resp, err := Send(workDir, "GET", path, nil)
	if err != nil {
		return "", err
	}
	if !resp.OK {
		return "", fmt.Errorf("%s", resp.Error)
	}
	var logs AgentLogsResponse
	if err := json.Unmarshal(resp.Data, &logs); err != nil {
		return "", fmt.Errorf("decoding logs response: %w", err)
	}
	return logs.Output, nil
}

// AgentSession sends GET /agents/{name}/session to the daemon, returning the tmux session name.
// The `name` may be a shorthand query; the server resolves it before responding.
func AgentSession(workDir, name string) (string, error) {
	resp, err := Send(workDir, "GET", "/agents/"+url.PathEscape(name)+"/session", nil)
	if err != nil {
		return "", err
	}
	if !resp.OK {
		return "", fmt.Errorf("%s", resp.Error)
	}
	var s AgentSessionResponse
	if err := json.Unmarshal(resp.Data, &s); err != nil {
		return "", fmt.Errorf("decoding session response: %w", err)
	}
	return s.Session, nil
}

// AgentResolve asks the daemon to resolve a shorthand query to the canonical
// agent and returns the hydrated record (name, session, repo). Used by remote
// clients that need to confirm an agent exists before acting on it.
func AgentResolve(workDir, query string) (AgentResolveResponse, error) {
	resp, err := Send(workDir, "GET", "/agents/resolve?q="+url.QueryEscape(query), nil)
	if err != nil {
		return AgentResolveResponse{}, err
	}
	if !resp.OK {
		return AgentResolveResponse{}, fmt.Errorf("%s", resp.Error)
	}
	var out AgentResolveResponse
	if err := json.Unmarshal(resp.Data, &out); err != nil {
		return AgentResolveResponse{}, fmt.Errorf("decoding resolve response: %w", err)
	}
	return out, nil
}
