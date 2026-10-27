package daemon

import (
	"context"
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
