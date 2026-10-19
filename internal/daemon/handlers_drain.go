package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/blackpaw-studio/leo/internal/cron"
	"github.com/blackpaw-studio/leo/internal/drain"
)

// maxDrainTimeoutSeconds bounds POST /drain's timeout_seconds (one day).
const maxDrainTimeoutSeconds = 86400

// DrainBeginRequest is the body for POST /drain.
type DrainBeginRequest struct {
	TimeoutSeconds int `json:"timeout_seconds"`
}

// DrainCommitRequest is the body for POST /drain/commit.
type DrainCommitRequest struct {
	Force bool `json:"force"`
}

// DrainCancelRequest is the body for POST /drain/cancel.
type DrainCancelRequest struct {
	Reason string `json:"reason,omitempty"`
}

// drainNotifierHolder boxes the web server's notifier for atomic.Pointer.
type drainNotifierHolder struct{ n drain.Notifier }

// SetDrain installs the graceful-restart drain manager and runs its poll
// loop for the daemon's lifetime. Call SetParentContext first.
func (s *Server) SetDrain(m *drain.Manager) {
	s.drain.Store(m)
	go m.Run(s.parentContext)
}

// Scheduler is the daemon's cron scheduler (a drain.CronGate).
func (s *Server) Scheduler() *cron.Scheduler { return s.scheduler }

// DrainNotifier messages agents through the web server once StartWeb has
// built it; until then (or with web.enabled off) every call reports
// drain.ErrNotifierUnavailable.
func (s *Server) DrainNotifier() drain.Notifier { return daemonDrainNotifier{s} }

func (s *Server) setDrainNotifier(n drain.Notifier) {
	s.drainNotifier.Store(&drainNotifierHolder{n: n})
}

// RunningDispatches lists the running interactive dispatches as drain
// probes of KindDispatch; none until StartWeb has built the web server
// (or with web.enabled off).
func (s *Server) RunningDispatches() []drain.Probe {
	if list := s.runningDispatches.Load(); list != nil {
		return (*list)()
	}
	return nil
}

func (s *Server) setRunningDispatches(list func() []drain.Probe) {
	s.runningDispatches.Store(&list)
}

type daemonDrainNotifier struct{ s *Server }

func (d daemonDrainNotifier) Message(ctx context.Context, name, text, id string) (drain.Delivery, error) {
	h := d.s.drainNotifier.Load()
	if h == nil {
		return drain.Delivery{}, drain.ErrNotifierUnavailable
	}
	return h.n.Message(ctx, name, text, id)
}

func (d daemonDrainNotifier) Holds(name, id string) (bool, error) {
	h := d.s.drainNotifier.Load()
	if h == nil {
		return false, drain.ErrNotifierUnavailable
	}
	return h.n.Holds(name, id)
}

// drainManager returns the manager, or answers 503 and nil.
func (s *Server) drainManager(w http.ResponseWriter) *drain.Manager {
	m := s.drain.Load()
	if m == nil {
		writeJSON(w, http.StatusServiceUnavailable, Response{Error: "graceful restart is unavailable in this daemon", Code: ErrorCodeDrainUnavailable})
	}
	return m
}

func (s *Server) handleDrainBusy(w http.ResponseWriter, r *http.Request) {
	m := s.drainManager(w)
	if m == nil {
		return
	}
	busy, err := m.BusyAgents(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if busy == nil {
		busy = []drain.Probe{}
	}
	writeDrainData(w, busy)
}

func (s *Server) handleDrainBegin(w http.ResponseWriter, r *http.Request) {
	m := s.drainManager(w)
	if m == nil {
		return
	}
	var req DrainBeginRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.TimeoutSeconds < 1 || req.TimeoutSeconds > maxDrainTimeoutSeconds {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("timeout_seconds must be between 1 and %d", maxDrainTimeoutSeconds))
		return
	}
	st, err := m.Begin(r.Context(), time.Duration(req.TimeoutSeconds)*time.Second)
	if err != nil {
		writeDrainError(w, err)
		return
	}
	writeDrainData(w, st)
}

func (s *Server) handleDrainStatus(w http.ResponseWriter, _ *http.Request) {
	if m := s.drainManager(w); m != nil {
		writeDrainData(w, m.Status())
	}
}

func (s *Server) handleDrainCommit(w http.ResponseWriter, r *http.Request) {
	m := s.drainManager(w)
	if m == nil {
		return
	}
	var req DrainCommitRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := m.Commit(r.Context(), req.Force); err != nil {
		writeDrainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, Response{OK: true})
}

func (s *Server) handleDrainCancel(w http.ResponseWriter, r *http.Request) {
	m := s.drainManager(w)
	if m == nil {
		return
	}
	var req DrainCancelRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	reason := req.Reason
	if reason == "" {
		reason = "cancelled by the CLI"
	}
	// The cancel notices must go out even if the CLI hangs up mid-request.
	if err := m.Cancel(context.WithoutCancel(r.Context()), reason); err != nil {
		writeDrainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, Response{OK: true})
}

func writeDrainData(w http.ResponseWriter, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("marshaling drain response: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, Response{OK: true, Data: data})
}

// drainErrorCodes maps the manager's state errors to wire codes, which the
// client maps back.
var drainErrorCodes = []struct {
	err  error
	code string
}{
	{drain.ErrInProgress, ErrorCodeDrainInProgress},
	{drain.ErrNotDrained, ErrorCodeDrainNotDrained},
	{drain.ErrNoDrain, ErrorCodeDrainNone},
}

func writeDrainError(w http.ResponseWriter, err error) {
	for _, c := range drainErrorCodes {
		if errors.Is(err, c.err) {
			writeJSON(w, http.StatusConflict, Response{Error: err.Error(), Code: c.code})
			return
		}
	}
	writeError(w, http.StatusInternalServerError, err.Error())
}
