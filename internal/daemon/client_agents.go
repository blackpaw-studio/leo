package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/blackpaw-studio/leo/internal/agent"
)

// responseError turns a daemon failure envelope into the right error type.
// When Code identifies a classified failure (not_found, ambiguous), a typed
// agent error is returned so callers can branch with errors.As. Unclassified
// failures fall back to a plain message error.
func responseError(resp *Response, query string) error {
	switch resp.Code {
	case ErrorCodeNotFound:
		return &agent.ErrNotFound{Query: query}
	case ErrorCodeAmbiguous:
		return &agent.ErrAmbiguous{Query: query, Matches: resp.Matches}
	case ErrorCodeWorktreeDirty:
		return fmt.Errorf("%w: %s", agent.ErrWorktreeDirty, resp.Error)
	case ErrorCodeBranchCheckedOut:
		return fmt.Errorf("%w: %s", agent.ErrBranchCheckedOut, resp.Error)
	case ErrorCodeBranchNotMerged:
		return fmt.Errorf("%w: %s", agent.ErrBranchNotMerged, resp.Error)
	case ErrorCodeBranchNotFound:
		return fmt.Errorf("%w: %s", agent.ErrBranchNotFound, resp.Error)
	case ErrorCodeAgentStillRunning:
		return fmt.Errorf("%w: %s", agent.ErrAgentStillRunning, resp.Error)
	case ErrorCodeNotWorktreeAgent:
		return fmt.Errorf("%w: %s", agent.ErrNotWorktreeAgent, resp.Error)
	case ErrorCodeWorktreeRequireSep:
		return fmt.Errorf("%w: %s", agent.ErrWorktreeRequiresSlash, resp.Error)
	default:
		return fmt.Errorf("%s", resp.Error)
	}
}

// AgentSpawn sends POST /agents/spawn to the daemon and returns the new record.
func AgentSpawn(ctx context.Context, workDir string, req AgentSpawnRequest) (agent.Record, error) {
	resp, err := Send(ctx, workDir, "POST", "/agents/spawn", req)
	if err != nil {
		return agent.Record{}, err
	}
	if !resp.OK {
		return agent.Record{}, responseError(resp, req.Name)
	}
	var rec agent.Record
	if err := json.Unmarshal(resp.Data, &rec); err != nil {
		return agent.Record{}, fmt.Errorf("decoding spawn response: %w", err)
	}
	return rec, nil
}

// AgentDelete sends DELETE /agents/{name} to the daemon. On typed failures
// (ErrWorktreeDirty, ErrBranchNotMerged, ErrAgentStillRunning, ...) it returns
// a wrapped error that callers can match with errors.Is.
func AgentDelete(ctx context.Context, workDir, name string, req AgentDeleteRequest) error {
	resp, err := Send(ctx, workDir, "DELETE", "/agents/"+url.PathEscape(name), req)
	if err != nil {
		return err
	}
	if !resp.OK {
		return responseError(resp, name)
	}
	return nil
}

// AgentDeletePlan sends GET /agents/{name}/delete-plan to the daemon,
// returning what AgentDelete would remove without removing anything. name may
// be a shorthand — the server resolves it. On resolve failures it returns
// typed *agent.ErrNotFound or *agent.ErrAmbiguous.
func AgentDeletePlan(ctx context.Context, workDir, name string) (agent.DeletePlan, error) {
	resp, err := Send(ctx, workDir, "GET", "/agents/"+url.PathEscape(name)+"/delete-plan", nil)
	if err != nil {
		return agent.DeletePlan{}, err
	}
	if !resp.OK {
		return agent.DeletePlan{}, responseError(resp, name)
	}
	var plan agent.DeletePlan
	if err := json.Unmarshal(resp.Data, &plan); err != nil {
		return agent.DeletePlan{}, fmt.Errorf("decoding delete plan response: %w", err)
	}
	return plan, nil
}

// AgentList sends GET /agents/list to the daemon.
func AgentList(ctx context.Context, workDir string) ([]agent.Record, error) {
	resp, err := Send(ctx, workDir, "GET", "/agents/list", nil)
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

// AgentStop sends POST /agents/{name}/stop to the daemon. wakeOnMessage
// carries intent, not state: true lets a subsequent inbound message auto-start
// the agent again (the idle sweep's stop); false (an operator-initiated stop)
// leaves it dormant until an operator runs `leo agent start` explicitly. On
// resolve failures it returns typed *agent.ErrNotFound or *agent.ErrAmbiguous
// so callers can branch with errors.As.
func AgentStop(ctx context.Context, workDir, name string, wakeOnMessage bool) error {
	resp, err := Send(ctx, workDir, "POST", "/agents/"+url.PathEscape(name)+"/stop", AgentStopRequest{WakeOnMessage: wakeOnMessage})
	if err != nil {
		return err
	}
	if !resp.OK {
		return responseError(resp, name)
	}
	return nil
}

// AgentStart sends POST /agents/{name}/start to the daemon. The dormant agent
// is re-spawned with --resume so the prior conversation continues.
func AgentStart(ctx context.Context, workDir, name string) error {
	resp, err := Send(ctx, workDir, "POST", "/agents/"+url.PathEscape(name)+"/start", nil)
	if err != nil {
		return err
	}
	if !resp.OK {
		return responseError(resp, name)
	}
	return nil
}

// AgentReset sends POST /agents/{name}/reset to the daemon. The agent's
// process/tmux session is stopped, its stored claude session id is cleared,
// and it is respawned fresh — a brand-new conversation, not a resume.
func AgentReset(ctx context.Context, workDir, name string) error {
	resp, err := Send(ctx, workDir, "POST", "/agents/"+url.PathEscape(name)+"/reset", nil)
	if err != nil {
		return err
	}
	if !resp.OK {
		return responseError(resp, name)
	}
	return nil
}

// AgentRestart sends POST /agents/{name}/restart to the daemon. The agent's
// process/tmux session is stopped and respawned with --resume so the prior
// conversation continues (unlike AgentReset, which starts fresh). name may be
// a shorthand — the server resolves it (with a store fallback for a record
// stopped by a failed boot-time restore that plain Resolve excludes) and
// echoes the canonical record back, so callers do not need their own
// pre-resolve step. On resolve failures it returns typed *agent.ErrNotFound
// or *agent.ErrAmbiguous.
func AgentRestart(ctx context.Context, workDir, name string) (agent.Record, error) {
	resp, err := Send(ctx, workDir, "POST", "/agents/"+url.PathEscape(name)+"/restart", nil)
	if err != nil {
		return agent.Record{}, err
	}
	if !resp.OK {
		return agent.Record{}, responseError(resp, name)
	}
	var rec agent.Record
	if err := json.Unmarshal(resp.Data, &rec); err != nil {
		return agent.Record{}, fmt.Errorf("decoding restart response: %w", err)
	}
	return rec, nil
}

// AgentSwitchTemplate sends POST /agents/{name}/set-template?template=... to
// the daemon, re-pointing the agent at another template: its wiring is rebuilt
// from that template and the conversation it last had there is restored (or a
// fresh one started). Backs `leo agent set-template`. On resolve failures it
// returns typed *agent.ErrNotFound or *agent.ErrAmbiguous.
func AgentSwitchTemplate(ctx context.Context, workDir, name, template string) (agent.SwitchResult, error) {
	path := "/agents/" + url.PathEscape(name) + "/set-template?template=" + url.QueryEscape(template)
	resp, err := Send(ctx, workDir, "POST", path, nil)
	if err != nil {
		return agent.SwitchResult{}, err
	}
	if !resp.OK {
		return agent.SwitchResult{}, responseError(resp, name)
	}
	var out agent.SwitchResult
	if err := json.Unmarshal(resp.Data, &out); err != nil {
		return agent.SwitchResult{}, fmt.Errorf("decoding set-template response: %w", err)
	}
	return out, nil
}

// AgentStale sends GET /agents/stale, returning the running agents whose
// wiring would change if they were restarted. `leo update` uses it to decide
// whether to offer a restart after swapping the binary.
func AgentStale(ctx context.Context, workDir string) ([]agent.StaleAgent, error) {
	resp, err := Send(ctx, workDir, "GET", "/agents/stale", nil)
	if err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, fmt.Errorf("%s", resp.Error)
	}
	var out []agent.StaleAgent
	if err := json.Unmarshal(resp.Data, &out); err != nil {
		return nil, fmt.Errorf("decoding stale-agent response: %w", err)
	}
	return out, nil
}

// AgentRestartAllResult is the client-side decoding of AgentRestartAllResponse,
// with Failed reconstructed as plain errors instead of strings.
type AgentRestartAllResult struct {
	Restarted []string
	Skipped   []string
	Failed    map[string]error
}

// AgentRestartAll sends POST /agents/restart to the daemon, bouncing every
// currently-running agent (skipping suspended/stopped ones). Per-agent
// failures are reported in the result rather than surfaced as the call error.
func AgentRestartAll(ctx context.Context, workDir string) (AgentRestartAllResult, error) {
	resp, err := Send(ctx, workDir, "POST", "/agents/restart", nil)
	if err != nil {
		return AgentRestartAllResult{}, err
	}
	if !resp.OK {
		return AgentRestartAllResult{}, fmt.Errorf("%s", resp.Error)
	}
	var out AgentRestartAllResponse
	if err := json.Unmarshal(resp.Data, &out); err != nil {
		return AgentRestartAllResult{}, fmt.Errorf("decoding restart-all response: %w", err)
	}
	failed := make(map[string]error, len(out.Failed))
	for name, msg := range out.Failed {
		failed[name] = fmt.Errorf("%s", msg)
	}
	return AgentRestartAllResult{Restarted: out.Restarted, Skipped: out.Skipped, Failed: failed}, nil
}

// AgentLogs sends GET /agents/{name}/logs?lines=N to the daemon.
// Pass lines<=0 to request the default tail. On resolve failures it returns
// typed *agent.ErrNotFound or *agent.ErrAmbiguous.
func AgentLogs(ctx context.Context, workDir, name string, lines int) (string, error) {
	path := "/agents/" + url.PathEscape(name) + "/logs"
	if lines > 0 {
		path += fmt.Sprintf("?lines=%d", lines)
	}
	resp, err := Send(ctx, workDir, "GET", path, nil)
	if err != nil {
		return "", err
	}
	if !resp.OK {
		return "", responseError(resp, name)
	}
	var logs AgentLogsResponse
	if err := json.Unmarshal(resp.Data, &logs); err != nil {
		return "", fmt.Errorf("decoding logs response: %w", err)
	}
	return logs.Output, nil
}

// AgentSession sends GET /agents/{name}/session to the daemon, returning the
// full response (tmux session name, canonical name, and whether the resolved
// agent is dormant). The `name` may be a shorthand query; the server resolves
// it before responding. On resolve failures it returns typed
// *agent.ErrNotFound or *agent.ErrAmbiguous.
func AgentSession(ctx context.Context, workDir, name string) (AgentSessionResponse, error) {
	resp, err := Send(ctx, workDir, "GET", "/agents/"+url.PathEscape(name)+"/session", nil)
	if err != nil {
		return AgentSessionResponse{}, err
	}
	if !resp.OK {
		return AgentSessionResponse{}, responseError(resp, name)
	}
	var s AgentSessionResponse
	if err := json.Unmarshal(resp.Data, &s); err != nil {
		return AgentSessionResponse{}, fmt.Errorf("decoding session response: %w", err)
	}
	return s, nil
}

// AgentAttachSpec sends GET /agents/{name}/attach-spec to the daemon,
// returning how to attach to a non-claude agent's live session (or an empty
// TmuxSession with Harness == "claude"/"" for a claude agent — callers
// should prefer AgentSession + the existing tmux attach flow for those). On
// resolve failures it returns typed *agent.ErrNotFound or *agent.ErrAmbiguous.
func AgentAttachSpec(ctx context.Context, workDir, name string) (AgentAttachSpecResponse, error) {
	resp, err := Send(ctx, workDir, "GET", "/agents/"+url.PathEscape(name)+"/attach-spec", nil)
	if err != nil {
		return AgentAttachSpecResponse{}, err
	}
	if !resp.OK {
		return AgentAttachSpecResponse{}, responseError(resp, name)
	}
	var out AgentAttachSpecResponse
	if err := json.Unmarshal(resp.Data, &out); err != nil {
		return AgentAttachSpecResponse{}, fmt.Errorf("decoding attach spec response: %w", err)
	}
	return out, nil
}

// AgentResolve asks the daemon to resolve a shorthand query to the canonical
// agent and returns the hydrated record (name, session, repo). Used by remote
// clients that need to confirm an agent exists before acting on it. On resolve
// failures it returns typed *agent.ErrNotFound or *agent.ErrAmbiguous.
func AgentResolve(ctx context.Context, workDir, query string) (AgentResolveResponse, error) {
	resp, err := Send(ctx, workDir, "GET", "/agents/resolve?q="+url.QueryEscape(query), nil)
	if err != nil {
		return AgentResolveResponse{}, err
	}
	if !resp.OK {
		return AgentResolveResponse{}, responseError(resp, query)
	}
	var out AgentResolveResponse
	if err := json.Unmarshal(resp.Data, &out); err != nil {
		return AgentResolveResponse{}, fmt.Errorf("decoding resolve response: %w", err)
	}
	return out, nil
}

// AgentRename renames the agent matching query to newName via the daemon and
// returns the updated record. On resolve failures it returns typed
// *agent.ErrNotFound or *agent.ErrAmbiguous.
func AgentRename(ctx context.Context, workDir, query, newName string) (agent.Record, error) {
	resp, err := Send(ctx, workDir, "POST", "/agents/"+url.PathEscape(query)+"/rename", AgentRenameRequest{NewName: newName})
	if err != nil {
		return agent.Record{}, err
	}
	if !resp.OK {
		return agent.Record{}, responseError(resp, query)
	}
	var rec agent.Record
	if err := json.Unmarshal(resp.Data, &rec); err != nil {
		return agent.Record{}, fmt.Errorf("decoding rename response: %w", err)
	}
	return rec, nil
}
