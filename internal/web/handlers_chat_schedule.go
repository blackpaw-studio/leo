package web

import (
	"net/http"
	"time"

	"github.com/blackpaw-studio/leo/internal/chat"
)

// reminderView is a reminder as the agent tools see it; at is RFC3339 in
// the daemon's local offset, whatever offset the caller scheduled it in.
type reminderView struct {
	ID   string `json:"id"`
	At   string `json:"at"`
	Text string `json:"text,omitempty"`
}

func newReminderView(r chat.Schedule, withText bool) reminderView {
	v := reminderView{ID: r.ID, At: r.FireAt.Local().Format(time.RFC3339)}
	if withText {
		v.Text = r.Text
	}
	return v
}

// POST /api/agent/{name}/chat/schedule — schedule a reminder into the
// caller's thread.
func (s *Server) handleAgentChatSchedule(w http.ResponseWriter, r *http.Request) {
	if !s.chatAvailable(w) {
		return
	}
	req, ok := decodeChatTool(w, r)
	if !ok {
		return
	}
	sched, err := s.chat.Schedule(r.Context(), chat.ScheduleRequest{Caller: r.PathValue("name"), Task: req.Task, RunID: req.RunID, Text: req.Text, At: req.At})
	if err != nil {
		writeChatError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: newReminderView(sched, false)})
}

// POST /api/agent/{name}/chat/schedule/list — the caller's pending
// reminders, soonest first.
func (s *Server) handleAgentChatScheduleList(w http.ResponseWriter, r *http.Request) {
	if !s.chatAvailable(w) {
		return
	}
	req, ok := decodeChatTool(w, r)
	if !ok {
		return
	}
	pending, err := s.chat.Schedules(r.Context(), chat.ScheduleListRequest{Caller: r.PathValue("name"), Task: req.Task, RunID: req.RunID})
	if err != nil {
		writeChatError(w, err)
		return
	}
	views := make([]reminderView, 0, len(pending))
	for _, p := range pending {
		views = append(views, newReminderView(p, true))
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: views})
}

// POST /api/agent/{name}/chat/schedule/cancel
func (s *Server) handleAgentChatScheduleCancel(w http.ResponseWriter, r *http.Request) {
	if !s.chatAvailable(w) {
		return
	}
	req, ok := decodeChatTool(w, r)
	if !ok {
		return
	}
	if err := s.chat.CancelSchedule(r.Context(), chat.ScheduleCancelRequest{Caller: r.PathValue("name"), Task: req.Task, RunID: req.RunID, ID: req.ID}); err != nil {
		writeChatError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: map[string]any{"id": req.ID, "cancelled": true}})
}
