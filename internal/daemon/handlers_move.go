package daemon

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"

	"github.com/blackpaw-studio/leo/internal/agentmove"
)

// MoveReceiver serves the destination verbs of `leo agent move` and
// `leo agent copy` (see agentmove.Receiver).
type MoveReceiver interface {
	Handle(ctx context.Context, verb string, body []byte) ([]byte, error)
}

type moveReceiverHolder struct{ r MoveReceiver }

// SetMoveReceiver wires the destination verbs. Until it is called the
// routes answer 503.
func (s *Server) SetMoveReceiver(r MoveReceiver) { s.moveReceiver.Store(&moveReceiverHolder{r}) }

// maxMoveBody bounds a manifest.
const maxMoveBody = 8 << 20

// detachedMoveVerbs outlive their request: a dropped ssh must not abort a
// half-landed transfer (the source asks for status instead).
var detachedMoveVerbs = []string{agentmove.VerbCommit, agentmove.VerbGitSeed, agentmove.VerbGitPrepare}

func (s *Server) handleMoveVerb(verb string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h := s.moveReceiver.Load()
		if h == nil {
			writeJSON(w, http.StatusServiceUnavailable, agentmove.VerbError{Code: "unavailable", Message: "this daemon is not receiving moves yet"})
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maxMoveBody+1))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, agentmove.VerbError{Code: agentmove.CodeBadRequest, Message: err.Error()})
			return
		}
		if len(body) > maxMoveBody {
			writeJSON(w, http.StatusRequestEntityTooLarge, agentmove.VerbError{Code: agentmove.CodeBadRequest, Message: "manifest too large"})
			return
		}
		ctx := r.Context()
		if slices.Contains(detachedMoveVerbs, verb) {
			ctx = context.WithoutCancel(ctx)
		}
		out, err := h.r.Handle(ctx, verb, body)
		if err != nil {
			status := http.StatusConflict
			if agentmove.CodeOf(err) == agentmove.CodeBadRequest {
				status = http.StatusBadRequest
			}
			writeJSON(w, status, agentmove.VerbError{Code: agentmove.CodeOf(err), Message: err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(out)
	}
}

// MoveJobs runs source-side transfers (see agentmove.Jobs).
type MoveJobs interface {
	Start(ctx context.Context, req agentmove.Request) (agentmove.JobStatus, error)
	Get(id string) (agentmove.JobStatus, bool)
	// Reconcile settles a fenced agent's pending move.
	Reconcile(ctx context.Context, name string) (agentmove.SettleResult, error)
}

type moveJobsHolder struct{ j MoveJobs }

// SetMoveJobs wires `leo agent move` and `copy` on this host (the source
// side). Until it is called the routes answer 503.
func (s *Server) SetMoveJobs(j MoveJobs) { s.moveJobs.Store(&moveJobsHolder{j}) }

// maxMoveRequest bounds a move request body.
const maxMoveRequest = 64 << 10

// handleMoveStart starts a move or copy of the path's agent and answers 202
// with the job, which callers poll at GET /moves/{id}.
func (s *Server) handleMoveStart(w http.ResponseWriter, r *http.Request) {
	h := s.moveJobs.Load()
	if h == nil {
		writeJSON(w, http.StatusServiceUnavailable, agentmove.VerbError{Code: "unavailable", Message: "this daemon cannot move agents yet"})
		return
	}
	var req agentmove.Request
	if err := json.NewDecoder(io.LimitReader(r.Body, maxMoveRequest)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, agentmove.VerbError{Code: agentmove.CodeBadRequest, Message: err.Error()})
		return
	}
	req.Agent = r.PathValue("name")
	st, err := h.j.Start(r.Context(), req)
	if err != nil {
		writeMoveError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, st)
}

func (s *Server) handleMoveJob(w http.ResponseWriter, r *http.Request) {
	h := s.moveJobs.Load()
	if h == nil {
		writeJSON(w, http.StatusServiceUnavailable, agentmove.VerbError{Code: "unavailable", Message: "this daemon cannot move agents yet"})
		return
	}
	st, ok := h.j.Get(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, agentmove.VerbError{Code: agentmove.CodeBadRequest, Message: "no such move job"})
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// handleMoveReconcile settles the path's agent's pending move by asking its
// destination, and answers with the outcome.
func (s *Server) handleMoveReconcile(w http.ResponseWriter, r *http.Request) {
	h := s.moveJobs.Load()
	if h == nil {
		writeJSON(w, http.StatusServiceUnavailable, agentmove.VerbError{Code: "unavailable", Message: "this daemon cannot move agents yet"})
		return
	}
	res, err := h.j.Reconcile(r.Context(), r.PathValue("name"))
	if err != nil {
		writeMoveError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// writeMoveError answers a refused move request: 400 for a bad request,
// 409 otherwise, with the error's code.
func writeMoveError(w http.ResponseWriter, err error) {
	status := http.StatusConflict
	if agentmove.CodeOf(err) == agentmove.CodeBadRequest {
		status = http.StatusBadRequest
	}
	writeJSON(w, status, agentmove.VerbError{Code: agentmove.CodeOf(err), Message: err.Error()})
}
