package daemon

import "net/http"

// setAgentContext installs the handler POST /api/agent/context delegates to:
// the web server's, which owns the agent attention state and what each session
// was told. StartWeb sets it after the socket is already serving, hence the
// atomic.
func (s *Server) setAgentContext(h http.Handler) {
	s.agentContext.Store(&controlHolder{h: h})
}

// handleAgentContext serves POST /api/agent/context, the context hooks' route:
// the request goes, unchanged, to the web server's handler. It is served on
// the socket so every request carries the process the kernel says connected
// (peercred): that, not anything in the body, is what lets a hook rebind an
// agent's session. 503 until StartWeb has built the web server, or always with
// web.enabled off.
func (s *Server) handleAgentContext(w http.ResponseWriter, r *http.Request) {
	holder := s.agentContext.Load()
	if holder == nil {
		writeError(w, http.StatusServiceUnavailable, "agent context is unavailable: the web server is not running (web.enabled)")
		return
	}
	holder.h.ServeHTTP(w, r)
}
