package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/blackpaw-studio/leo/internal/agentmove"
)

// MoveVerb posts a destination verb's manifest to the daemon at workDir and
// returns the HTTP status and body. There is no overall timeout: a commit
// runs post_receive hooks and waits out the start confirmation.
func MoveVerb(ctx context.Context, workDir, verb string, body []byte) (int, []byte, error) {
	return sendRaw(ctx, newUnixClientNoTimeout(SockPath(workDir)), http.MethodPost, "http://daemon/agents/move/"+verb, body)
}

// StartMove asks the daemon at workDir to move or copy agent name. It
// answers at once with the job, which MoveJob polls.
func StartMove(ctx context.Context, workDir, name string, req agentmove.Request) (agentmove.JobStatus, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return agentmove.JobStatus{}, err
	}
	return moveJobCall(ctx, workDir, http.MethodPost, "/agents/"+url.PathEscape(name)+"/move", body, http.StatusAccepted)
}

// MoveJob reads a move job's status from the daemon at workDir.
func MoveJob(ctx context.Context, workDir, id string) (agentmove.JobStatus, error) {
	return moveJobCall(ctx, workDir, http.MethodGet, "/moves/"+url.PathEscape(id), nil, http.StatusOK)
}

// moveJobCall decodes a job, or the daemon's refusal as an
// *agentmove.VerbError.
func moveJobCall(ctx context.Context, workDir, method, path string, body []byte, want int) (agentmove.JobStatus, error) {
	status, out, err := sendRaw(ctx, newUnixClient(SockPath(workDir)), method, "http://daemon"+path, body)
	if err != nil {
		return agentmove.JobStatus{}, err
	}
	if status != want {
		var ve agentmove.VerbError
		if json.Unmarshal(out, &ve) == nil && ve.Code != "" {
			return agentmove.JobStatus{}, &ve
		}
		return agentmove.JobStatus{}, fmt.Errorf("daemon answered HTTP %d: %s", status, strings.TrimSpace(string(out)))
	}
	var st agentmove.JobStatus
	if err := json.Unmarshal(out, &st); err != nil {
		return agentmove.JobStatus{}, fmt.Errorf("decoding the move job: %w", err)
	}
	return st, nil
}

// ReconcileMove asks the daemon at workDir to settle agent name's pending
// move. It waits for the destination's answer.
func ReconcileMove(ctx context.Context, workDir, name string) (agentmove.SettleResult, error) {
	return settleCall(ctx, workDir, "/agents/"+url.PathEscape(name)+"/move/reconcile")
}

// AbandonMove asks the daemon at workDir to lift agent name's move fence
// without asking its destination.
func AbandonMove(ctx context.Context, workDir, name string) (agentmove.SettleResult, error) {
	return settleCall(ctx, workDir, "/agents/"+url.PathEscape(name)+"/move/abandon")
}

// settleCall decodes a settled move, or the daemon's refusal as an
// *agentmove.VerbError.
func settleCall(ctx context.Context, workDir, path string) (agentmove.SettleResult, error) {
	status, out, err := sendRaw(ctx, newUnixClientNoTimeout(SockPath(workDir)), http.MethodPost, "http://daemon"+path, []byte("{}"))
	if err != nil {
		return agentmove.SettleResult{}, err
	}
	if status != http.StatusOK {
		var ve agentmove.VerbError
		if json.Unmarshal(out, &ve) == nil && ve.Code != "" {
			return agentmove.SettleResult{}, &ve
		}
		return agentmove.SettleResult{}, fmt.Errorf("daemon answered HTTP %d: %s", status, strings.TrimSpace(string(out)))
	}
	var res agentmove.SettleResult
	if err := json.Unmarshal(out, &res); err != nil {
		return agentmove.SettleResult{}, fmt.Errorf("decoding the settled move: %w", err)
	}
	return res, nil
}
