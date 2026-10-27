package agentmove

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/config"
)

// How a pending move was settled.
const (
	// SettleMoved: the agent is live (or was delivered) on the destination
	// and is marked moved here.
	SettleMoved = "moved"
	// SettleFailed: the commit failed there; the agent is startable here.
	SettleFailed = "failed"
)

// SettleResult is a settled pending move.
type SettleResult struct {
	Name    string `json:"name"`
	To      string `json:"to"`
	RunID   string `json:"run_id"`
	Outcome string `json:"outcome"`
}

// Reconcile settles name's pending move by asking the destination how far
// the fenced run's commit got: delivered marks the agent moved here, a
// settled failure lifts the fence, and anything else, or no answer, keeps
// it fenced (outcome_unknown).
func (mv *Mover) Reconcile(ctx context.Context, name string) (SettleResult, error) {
	release, cfg, rec, err := mv.pending(name)
	if err != nil {
		return SettleResult{}, err
	}
	defer release()
	p := *rec.MovePending
	res := SettleResult{Name: name, To: p.To, RunID: p.RunID}
	st, err := mv.pendingStatus(ctx, cfg, rec)
	if err != nil {
		return res, problemf(CodeOutcomeUnknown,
			"%s's move to %s cannot be checked there (%v), so it stays fenced: check `leo --host %s agent list` and retry, or run `leo agent move %s --abandon-remote` if %s is gone for good",
			name, p.To, err, p.To, name, p.To)
	}
	switch {
	case st.Live || st.Phase == PhaseLive:
		if err := mv.Agents.MarkMoved(name, p.To, p.Peer, mv.Clock.Now()); err != nil {
			return res, fmt.Errorf("%s is on %s, but marking it moved here failed: %w; it stays fenced", name, p.To, err)
		}
		res.Outcome = SettleMoved
	case st.Phase == PhaseFailed:
		if err := mv.Agents.ClearMoveFence(name, p.RunID); err != nil {
			return res, fmt.Errorf("lifting %s's move fence: %w", name, err)
		}
		res.Outcome = SettleFailed
	default:
		phase := st.Phase
		if phase == "" {
			phase = "unknown"
		}
		return res, problemf(CodeOutcomeUnknown,
			"%s's move to %s has not settled there (phase %s), so it stays fenced: check `leo --host %s agent list` and retry `leo agent move %s --reconcile`",
			name, p.To, phase, p.To, name)
	}
	return res, nil
}

// ReconcilePending reconciles every fenced agent here, best-effort, and logs
// each outcome. The daemon runs it at start.
func (mv *Mover) ReconcilePending(ctx context.Context, logf func(format string, args ...any)) {
	cfg, err := mv.Config()
	if err != nil {
		logf("move reconcile: loading config: %v", err)
		return
	}
	records, err := agentstore.Load(agentstore.FilePath(cfg.HomePath))
	if err != nil {
		logf("move reconcile: loading agents: %v", err)
		return
	}
	var names []string
	for name, rec := range records {
		if rec.MovePending != nil {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	for _, name := range names {
		res, err := mv.Reconcile(ctx, name)
		if err != nil {
			logf("move reconcile: %s: %v", name, err)
			continue
		}
		logf("move reconcile: %s: %s on %s", name, res.Outcome, res.To)
	}
}

// pending takes name's move lock and returns its fenced record; release
// frees the lock.
func (mv *Mover) pending(name string) (func(), *config.Config, agentstore.Record, error) {
	release, err := mv.Agents.BeginMove(name, agent.MoveModeMove)
	if err != nil {
		return nil, nil, agentstore.Record{}, problemf(CodeAgentMoving, "%v", err)
	}
	fail := func(err error) (func(), *config.Config, agentstore.Record, error) {
		release()
		return nil, nil, agentstore.Record{}, err
	}
	cfg, err := mv.Config()
	if err != nil {
		return fail(fmt.Errorf("loading config: %w", err))
	}
	records, err := agentstore.Load(agentstore.FilePath(cfg.HomePath))
	if err != nil {
		return fail(fmt.Errorf("loading agents: %w", err))
	}
	rec, ok := records[name]
	switch {
	case !ok:
		return fail(problemf(CodeBadRequest, "agent %q not found", name))
	case rec.MovePending == nil:
		return fail(problemf(CodeBadRequest, "%s has no move pending", name))
	}
	return release, cfg, rec, nil
}

// pendingStatus asks the fenced run's destination about it.
func (mv *Mover) pendingStatus(ctx context.Context, cfg *config.Config, rec agentstore.Record) (StatusResult, error) {
	ep, err := mv.Endpoint(cfg, rec.MovePending.To)
	if err != nil {
		return StatusResult{}, err
	}
	host, err := mv.Host()
	if err != nil {
		return StatusResult{}, err
	}
	body, err := json.Marshal(Manifest{Protocol: Protocol, Mode: ModeMove, From: host, Agent: rec.Portable(), Transfer: rec.MovePending.RunID})
	if err != nil {
		return StatusResult{}, err
	}
	out, err := ep.Run(ctx, VerbStatus, body)
	if err != nil {
		return StatusResult{}, err
	}
	var st StatusResult
	if err := json.Unmarshal(out, &st); err != nil {
		return StatusResult{}, fmt.Errorf("decoding the status: %w", err)
	}
	return st, nil
}
