package daemon

import "net/http"

// controlVerbs are the agent control operations the socket serves at
// POST /agents/{name}/<verb> (docs/specs/2026-10-06-bridge-observe.md,
// "Control endpoints"). The 0600 socket is the auth, as for every route on
// this mux.
var controlVerbs = []string{"message", "interrupt", "compact", "clear"}

// controlHolder boxes the control handler for atomic.Pointer.
type controlHolder struct{ h http.Handler }

// setControl installs the handler the control routes delegate to: the web
// server's ControlHandler, whose cores own the bridge-then-legacy routing.
// StartWeb sets it after the socket is already serving, hence the atomic.
func (s *Server) setControl(h http.Handler) {
	s.control.Store(&controlHolder{h: h})
}

// handleAgentControl serves POST /agents/{name}/<verb> by handing the
// request, unchanged, to the web server's control handler. The cores live
// on the web server (its bridge router, process table and tmux seams), so
// the routes answer 503 until StartWeb has built it — or always, with
// web.enabled off.
func (s *Server) handleAgentControl(w http.ResponseWriter, r *http.Request) {
	holder := s.control.Load()
	if holder == nil {
		writeError(w, http.StatusServiceUnavailable, "agent control is unavailable: the web server is not running (web.enabled)")
		return
	}
	holder.h.ServeHTTP(w, r)
}
