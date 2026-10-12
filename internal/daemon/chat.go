package daemon

import (
	"net/http"

	"github.com/blackpaw-studio/leo/internal/chat"
	"github.com/blackpaw-studio/leo/internal/observe"
)

// chatSocketPrefix is where the socket serves Leo Chat: the same paths as
// the TCP API, for the Mac app. The 0600 socket is the auth.
const chatSocketPrefix = "/api/v1/chat/"

// SetChat hands StartWeb the chat store, the attachment directory and the
// publisher chat events go through (the push-wrapped chain, so chat
// messages push). A nil store leaves chat off. Must run before StartWeb.
func (s *Server) SetChat(store *chat.Store, attachmentsDir string, pub observe.Publisher) {
	s.chatStore, s.chatAttachmentsDir, s.chatPublisher = store, attachmentsDir, pub
}

// setChat installs the web server's chat handler behind the socket's chat
// routes; StartWeb sets it after the socket is already serving.
func (s *Server) setChat(h http.Handler) {
	s.chatHandler.Store(&controlHolder{h: h})
}

// handleChat serves /api/v1/chat/* by delegating to the web server, or 503
// until it exists (or with web.enabled off or chat unavailable).
func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	holder := s.chatHandler.Load()
	if holder == nil {
		writeError(w, http.StatusServiceUnavailable, "Leo Chat is unavailable: the web server is not running (web.enabled) or the chat store did not open")
		return
	}
	holder.h.ServeHTTP(w, r)
}
