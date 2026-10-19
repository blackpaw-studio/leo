package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/chat"
	"github.com/blackpaw-studio/leo/internal/observe"
)

// Leo Chat HTTP surface (docs/configuration/chat.md). The app routes live
// under /api/v1/chat (operator and paired-device tokens) and, unauthenticated,
// under /chat on the daemon's 0600 socket for the Mac app. The agent tool
// routes live under /api/agent/{name}/chat.

const (
	// chatUploadPath is the one route whose body may exceed the global cap.
	chatUploadPath = "/api/v1/chat/uploads"
	// maxChatUploadBody is the upload cap plus room for multipart framing.
	maxChatUploadBody = chat.MaxFileBytes + 1<<20
	// chatTransferTimeout bounds reading an upload or writing an attachment
	// (the server's 30 s deadlines are far too short for 50 MB on a phone).
	chatTransferTimeout = 10 * time.Minute
	// maxChatJSONBody caps the chat JSON bodies.
	maxChatJSONBody = 4 * chat.MaxTextBytes
)

// ChatOptions wires Leo Chat into the web server. The zero value disables it.
type ChatOptions struct {
	// Store is the open chat database; nil disables chat.
	Store *chat.Store
	// AttachmentsDir is where attachment files live.
	AttachmentsDir string
	// Publisher receives chat.* events; it must reach both the event bus
	// and the push notifier.
	Publisher observe.Publisher
	// TaskRun returns the persistent task and run in flight in an agent
	// (daemon.Server.TaskRun); nil runs none.
	TaskRun func(agent string) (task, runID string, ok bool)
}

// setupChat builds the chat service with this server as its deliverer.
func (s *Server) setupChat(opts ChatOptions) {
	if opts.Store == nil {
		return
	}
	s.chat = chat.NewService(chat.Options{
		Store:   opts.Store,
		Files:   chat.Files{Root: opts.AttachmentsDir},
		Deliver: chatDeliverer{s},
		Transcribe: chat.Transcriber{Run: chat.ExecRunner, LookPath: exec.LookPath, Model: func() string {
			cfg, err := s.loadConfig()
			if err != nil {
				return ""
			}
			return cfg.ChatWhisperModel()
		}}.Transcribe,
		Publish: opts.Publisher,
		Agents:  s.chatAgents,
		Config:  s.loadConfig,
		Queued:  func(agent, ref string) (bool, error) { return s.bridgeRouter.Holds(agent, ref) },
		TaskRun: opts.TaskRun,
	})
}

// StartChat recovers the asks and prompts the previous daemon left open and
// starts attachment retention, until ctx ends.
func (s *Server) StartChat(ctx context.Context) {
	if s.chat == nil {
		return
	}
	if err := s.chat.Recover(); err != nil {
		fmt.Fprintf(os.Stderr, "chat: recovering asks and prompts: %v\n", err)
	}
	go s.chat.Run(ctx)
}

// CloseChatAsksOf closes a gone agent's open chat asks. Nil-safe: with chat
// off there are none.
func (s *Server) CloseChatAsksOf(name string) {
	if s == nil || s.chat == nil {
		return
	}
	s.chat.CloseAsksOf(name)
}

// chatAgents lists every agent record, running or stopped.
func (s *Server) chatAgents() []chat.AgentRef {
	if s.agentSvc == nil {
		return nil
	}
	recs := s.agentSvc.List()
	out := make([]chat.AgentRef, 0, len(recs))
	for _, r := range recs {
		out = append(out, chat.AgentRef{Name: r.Name, Workspace: r.Workspace})
	}
	return out
}

// chatDeliverer delivers chat prompts through the agent messaging cores:
// the mod bridge (and its durable outbox) for bridged claude agents, the
// SessionDriver for other harnesses, else the peer inbox or tmux paste. A
// stopped agent is started first and its prompt injected once it is ready.
type chatDeliverer struct{ s *Server }

// A bridge delivery goes under the command id ref, which the agent's outbox
// keeps it by until the mod acks it.
func (d chatDeliverer) Deliver(ctx context.Context, name, prompt, ref string, starting func()) (string, error) {
	s := d.s
	harnessName := "claude"
	if hn, _, ok := s.resolveMessageTarget(name); ok && hn != "" {
		harnessName = hn
	}
	if _, live := s.processStates()[name]; !live {
		if err := d.start(ctx, name, harnessName, prompt, starting); err != nil {
			return "", err
		}
		return harnessName, nil
	}
	out := s.messageAgentAs(ctx, name, "", prompt, ref)
	if out.err != "" {
		return "", errors.New(out.err)
	}
	return harnessName, nil
}

func (d chatDeliverer) start(ctx context.Context, name, harnessName, prompt string, starting func()) error {
	s := d.s
	if s.agentSvc == nil {
		return fmt.Errorf("agent %s is not running", name)
	}
	starting()
	// Started by someone else since the live check: deliver all the same,
	// through the readiness-aware path below, as it may still be booting.
	if err := s.agentSvc.Start(name); err != nil && !errors.Is(err, agent.ErrAgentAlreadyRunning) {
		return fmt.Errorf("starting %s: %w", name, err)
	}
	if harnessName != "claude" {
		_, h, ok := s.resolveMessageTarget(name)
		if !ok {
			return fmt.Errorf("resolving %s after start", name)
		}
		if out := s.dispatchNonClaudeMessage(harnessName, h, stripControl(prompt, true)); out.err != "" {
			return errors.New(out.err)
		}
		return nil
	}
	// A cold claude takes a while before its prompt accepts input; the
	// readiness-probing injector waits for it.
	if err := s.injectPrompt(ctx, agent.SessionName(name), stripControl(prompt, true)); err != nil {
		return fmt.Errorf("delivering to %s after start: %w", name, err)
	}
	return nil
}

// registerChatRoutes registers the app routes on mux under prefix, each
// wrapped by wrap. GET /{seg}/{rest} serves both
// GET <prefix>/attachments/{id} and GET <prefix>/{agent}/messages: Go's mux
// rejects the two as overlapping patterns.
func (s *Server) registerChatRoutes(mux *http.ServeMux, prefix string, wrap func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET "+prefix+"/threads", wrap(s.handleChatThreads))
	mux.HandleFunc("GET "+prefix+"/{seg}/{rest}", wrap(s.handleChatGet))
	mux.HandleFunc("POST "+prefix+"/uploads", wrap(s.handleChatUpload))
	mux.HandleFunc("POST "+prefix+"/{agent}/messages", wrap(s.handleChatSend))
	mux.HandleFunc("POST "+prefix+"/{agent}/messages/{id}/answer", wrap(s.handleChatAnswer))
	mux.HandleFunc("POST "+prefix+"/{agent}/messages/{id}/dismiss", wrap(s.handleChatDismiss))
	mux.HandleFunc("POST "+prefix+"/{agent}/read", wrap(s.handleChatRead))
}

// chatAPIPrefix is where the app routes live, on TCP and on the socket.
const chatAPIPrefix = "/api/v1/chat"

// ChatHandler serves the app routes unauthenticated at the same
// /api/v1/chat paths, for the daemon to mount on its unix socket, whose
// 0600 mode is the auth. It also serves the CLI-only routes that the TCP
// listener never registers.
func (s *Server) ChatHandler() http.Handler {
	mux := http.NewServeMux()
	s.registerChatRoutes(mux, chatAPIPrefix, func(h http.HandlerFunc) http.HandlerFunc { return h })
	mux.HandleFunc("POST /chat/task-failure", s.handleChatTaskFailure)
	return mux
}

// POST /chat/task-failure — a `leo run` process reports an exhausted oneshot
// task run; the card lands in the task owner's thread. Socket-only.
func (s *Server) handleChatTaskFailure(w http.ResponseWriter, r *http.Request) {
	if !s.chatAvailable(w) {
		return
	}
	var req struct {
		Task     string `json:"task"`
		RunID    string `json:"run_id"`
		Attempts int    `json:"attempts"`
		Error    string `json:"error"`
		LogPath  string `json:"log_path"`
	}
	if !decodeChatJSON(w, r, &req) {
		return
	}
	m, err := s.chat.TaskFailure(r.Context(), chat.TaskFailureRequest{
		Task: req.Task, RunID: req.RunID, Attempts: req.Attempts, Error: req.Error, LogPath: req.LogPath,
	})
	if err != nil {
		writeChatError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: map[string]string{"id": m.ID, "agent": m.Agent}})
}

// chatAvailable answers 503 when chat is not wired.
func (s *Server) chatAvailable(w http.ResponseWriter) bool {
	if s.chat == nil {
		writeJSON(w, http.StatusServiceUnavailable, apiResponse{Error: "Leo Chat is not available on this daemon"})
		return false
	}
	return true
}

// writeChatError maps a chat error to its status.
func writeChatError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, chat.ErrInvalid):
		status = http.StatusBadRequest
	case errors.Is(err, chat.ErrUnknownAgent), errors.Is(err, chat.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, chat.ErrConflict):
		status = http.StatusConflict
	case errors.Is(err, chat.ErrTooLarge):
		status = http.StatusRequestEntityTooLarge
	case errors.Is(err, chat.ErrClosed):
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, apiResponse{Error: err.Error()})
}

// GET /api/v1/chat/threads
func (s *Server) handleChatThreads(w http.ResponseWriter, _ *http.Request) {
	if !s.chatAvailable(w) {
		return
	}
	threads, err := s.chat.Threads()
	if err != nil {
		writeChatError(w, err)
		return
	}
	if threads == nil {
		threads = []chat.Thread{}
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: map[string][]chat.Thread{"threads": threads}})
}

func (s *Server) handleChatGet(w http.ResponseWriter, r *http.Request) {
	seg, rest := r.PathValue("seg"), r.PathValue("rest")
	// Thread routes first: "attachments" is a valid agent name, and an
	// attachment id (a ULID) is never "messages".
	switch {
	case rest == "messages":
		s.handleChatMessages(w, r, seg)
	case seg == "attachments":
		s.serveChatAttachment(w, r, rest)
	default:
		http.NotFound(w, r)
	}
}

// GET /api/v1/chat/{agent}/messages?before=&limit=&task=
func (s *Server) handleChatMessages(w http.ResponseWriter, r *http.Request, agentName string) {
	if !s.chatAvailable(w) {
		return
	}
	q := r.URL.Query()
	page := chat.Page{Before: q.Get("before"), Task: q.Get("task")}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			writeJSON(w, http.StatusBadRequest, apiResponse{Error: "limit must be a positive integer"})
			return
		}
		page.Limit = n
	}
	msgs, err := s.chat.Messages(agentName, page)
	if err != nil {
		writeChatError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: msgs})
}

// decodeChatJSON decodes one JSON object of at most maxChatJSONBody.
func decodeChatJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxChatJSONBody))
	if err := dec.Decode(v); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, apiResponse{Error: "request body too large"})
			return false
		}
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: fmt.Sprintf("invalid request: %v", err)})
		return false
	}
	if dec.More() {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "invalid request: unexpected data after the JSON object"})
		return false
	}
	return true
}

// POST /api/v1/chat/{agent}/messages {text, reply_to?, attachment_ids?}
func (s *Server) handleChatSend(w http.ResponseWriter, r *http.Request) {
	if !s.chatAvailable(w) {
		return
	}
	var req chat.SendRequest
	if !decodeChatJSON(w, r, &req) {
		return
	}
	m, err := s.chat.Send(r.Context(), r.PathValue("agent"), req)
	if err != nil {
		writeChatError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, apiResponse{OK: true, Data: m})
}

// POST /api/v1/chat/uploads — multipart: file (one part), optionally after
// an agent field that files the upload under that agent at once.
func (s *Server) handleChatUpload(w http.ResponseWriter, r *http.Request) {
	if !s.chatAvailable(w) {
		return
	}
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(chatTransferTimeout))
	_ = rc.SetWriteDeadline(time.Now().Add(chatTransferTimeout))
	r.Body = http.MaxBytesReader(w, r.Body, maxChatUploadBody)
	mr, err := r.MultipartReader()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "expected a multipart/form-data body"})
		return
	}
	var agentName string
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			writeJSON(w, http.StatusBadRequest, apiResponse{Error: "missing file part"})
			return
		}
		if err != nil {
			writeUploadError(w, err)
			return
		}
		switch part.FormName() {
		case "agent":
			b, err := io.ReadAll(io.LimitReader(part, 128))
			if err != nil {
				writeUploadError(w, err)
				return
			}
			agentName = strings.TrimSpace(string(b))
		case "file":
			a, err := s.chat.Upload(agentName, part.FileName(), part.Header.Get("Content-Type"), part)
			if err != nil {
				if errors.As(err, new(*http.MaxBytesError)) {
					err = chat.ErrTooLarge
				}
				writeChatError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: a})
			return
		}
	}
}

func writeUploadError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeJSON(w, http.StatusRequestEntityTooLarge, apiResponse{Error: chat.ErrTooLarge.Error()})
		return
	}
	writeJSON(w, http.StatusBadRequest, apiResponse{Error: fmt.Sprintf("reading upload: %v", err)})
}

// GET /api/v1/chat/attachments/{id} — Range supported.
func (s *Server) serveChatAttachment(w http.ResponseWriter, r *http.Request, id string) {
	if !s.chatAvailable(w) {
		return
	}
	a, err := s.chat.Attachment(id)
	if err != nil {
		writeChatError(w, err)
		return
	}
	if !a.Available {
		writeJSON(w, http.StatusGone, apiResponse{Error: "attachment file was pruned"})
		return
	}
	f, err := os.OpenFile(a.Path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		writeJSON(w, http.StatusGone, apiResponse{Error: "attachment file is missing"})
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		writeJSON(w, http.StatusGone, apiResponse{Error: "attachment file is missing"})
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(chatTransferTimeout))
	h := w.Header()
	h.Set("Content-Type", a.MIME)
	h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": a.Filename}))
	h.Set("Content-Security-Policy", "sandbox; default-src 'none'")
	h.Set("Cache-Control", "private, max-age=86400, immutable")
	http.ServeContent(w, r, a.Filename, info.ModTime(), f)
}

// POST /api/v1/chat/{agent}/messages/{id}/answer {values, text}
func (s *Server) handleChatAnswer(w http.ResponseWriter, r *http.Request) {
	if !s.chatAvailable(w) {
		return
	}
	var req chat.Answer
	if !decodeChatJSON(w, r, &req) {
		return
	}
	m, err := s.chat.Answer(r.PathValue("agent"), r.PathValue("id"), req)
	if err != nil {
		writeChatError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: m})
}

// POST /api/v1/chat/{agent}/messages/{id}/dismiss — closes an open questions
// card without answering it, so the terminal's own dialog takes over. The
// body is ignored.
func (s *Server) handleChatDismiss(w http.ResponseWriter, r *http.Request) {
	if !s.chatAvailable(w) {
		return
	}
	m, err := s.chat.Dismiss(r.PathValue("agent"), r.PathValue("id"))
	if err != nil {
		writeChatError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: m})
}

// POST /api/v1/chat/{agent}/read {up_to}
func (s *Server) handleChatRead(w http.ResponseWriter, r *http.Request) {
	if !s.chatAvailable(w) {
		return
	}
	var req struct {
		UpTo string `json:"up_to"`
	}
	if !decodeChatJSON(w, r, &req) {
		return
	}
	if err := s.chat.MarkRead(r.PathValue("agent"), req.UpTo); err != nil {
		writeChatError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true})
}

// chatToolRequest is the body of the agent tool routes. Task and RunID are
// the caller's LEO_TASK / LEO_RUN_ID; DispatchID its LEO_DISPATCH_ID.
type chatToolRequest struct {
	Text       string        `json:"text"`
	Files      []string      `json:"files"`
	Question   string        `json:"question"`
	Options    []chat.Option `json:"options"` // legacy: buttons
	Input      *chat.Input   `json:"input"`
	TimeoutS   float64       `json:"timeout_s"`
	WaitS      float64       `json:"wait_s"`
	MessageID  string        `json:"message_id"`
	Limit      int           `json:"limit"`
	Before     string        `json:"before"`
	Filter     string        `json:"task_filter"`
	At         string        `json:"at"`
	ID         string        `json:"id"`
	Task       string        `json:"task"`
	RunID      string        `json:"run_id"`
	DispatchID string        `json:"dispatch_id"`
}

// decodeChatTool decodes a tool request, refusing leo_dispatch subagents,
// which inherit their orchestrator's process name and would post into its
// thread.
//
// Trust boundary: {name} and the task fields are
// self-asserted by the calling MCP server and every agent shares one agent
// token, so this keeps honest callers in their own thread; it is not
// authentication.
func decodeChatTool(w http.ResponseWriter, r *http.Request) (chatToolRequest, bool) {
	var req chatToolRequest
	if !decodeChatJSON(w, r, &req) {
		return req, false
	}
	if req.DispatchID != "" {
		writeJSON(w, http.StatusForbidden, apiResponse{Error: fmt.Sprintf("Leo Chat tools are not available to leo_dispatch subagents (dispatch %s); report to your orchestrator instead", req.DispatchID)})
		return req, false
	}
	return req, true
}

// POST /api/agent/{name}/chat/post
func (s *Server) handleAgentChatPost(w http.ResponseWriter, r *http.Request) {
	if !s.chatAvailable(w) {
		return
	}
	req, ok := decodeChatTool(w, r)
	if !ok {
		return
	}
	m, err := s.chat.Post(r.Context(), chat.PostRequest{Caller: r.PathValue("name"), Task: req.Task, RunID: req.RunID, Text: req.Text, Files: req.Files})
	if err != nil {
		writeChatError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: map[string]string{"id": m.ID, "agent": m.Agent}})
}

// POST /api/agent/{name}/chat/ask — blocks until answered, closed, or wait_s
// passes (pending: resume with message_id, or wait for the late answer that
// the daemon delivers to the agent). An ask with no timeout_s has no deadline.
func (s *Server) handleAgentChatAsk(w http.ResponseWriter, r *http.Request) {
	if !s.chatAvailable(w) {
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	req, ok := decodeChatTool(w, r)
	if !ok {
		return
	}
	maxS := chat.MaxAskTimeout.Seconds()
	if req.TimeoutS < 0 || req.TimeoutS > maxS || req.WaitS < 0 || req.WaitS > maxS {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: fmt.Sprintf("timeout_s and wait_s must be between 0 and %d", int(maxS))})
		return
	}
	res, err := s.chat.Ask(r.Context(), chat.AskRequest{
		Caller: r.PathValue("name"), Task: req.Task, RunID: req.RunID,
		Question: req.Question, Options: req.Options, Input: req.Input, MessageID: req.MessageID,
		Timeout: time.Duration(req.TimeoutS * float64(time.Second)),
		Wait:    time.Duration(req.WaitS * float64(time.Second)),
	})
	if err != nil {
		writeChatError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: res})
}

// POST /api/agent/{name}/chat/ask/withdraw {message_id}
func (s *Server) handleAgentChatAskWithdraw(w http.ResponseWriter, r *http.Request) {
	if !s.chatAvailable(w) {
		return
	}
	req, ok := decodeChatTool(w, r)
	if !ok {
		return
	}
	m, err := s.chat.WithdrawAsk(r.Context(), chat.WithdrawRequest{Caller: r.PathValue("name"), MessageID: req.MessageID})
	if err != nil {
		writeChatError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: map[string]any{"message_id": m.ID, "withdrawn": true}})
}

// POST /api/agent/{name}/chat/history
func (s *Server) handleAgentChatHistory(w http.ResponseWriter, r *http.Request) {
	if !s.chatAvailable(w) {
		return
	}
	req, ok := decodeChatTool(w, r)
	if !ok {
		return
	}
	msgs, err := s.chat.History(r.Context(), chat.HistoryRequest{Caller: r.PathValue("name"), RunTask: req.Task, Page: chat.Page{Limit: req.Limit, Before: req.Before, Task: req.Filter}})
	if err != nil {
		writeChatError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: msgs})
}

// handleAPIPermission is the long poll behind Claude's PermissionRequest
// hook (`leo dispatch permission`), for a dispatched claude (its
// orchestrator decides) and for a supervised agent (Leo Chat decides).
// Either way it answers with a decision, or none (an empty object) once the
// wait ends, and Claude's own terminal prompt — already on screen — stands.
//
// POST /api/dispatch/{id}/permission {payload}
// POST /api/agent/permission {token, payload}
func (s *Server) handleAPIPermission(w http.ResponseWriter, r *http.Request) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	var req struct {
		Token   string          `json:"token"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := decodeDispatchJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: fmt.Sprintf("invalid request: %v", err)})
		return
	}
	cfg, err := s.loadConfig()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: fmt.Sprintf("loading config: %v", err)})
		return
	}
	if id := r.PathValue("id"); id != "" {
		decision := s.consults.RequestPermission(r.Context(), id, req.Payload, cfg.DispatchApprovalTimeout())
		writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: decision})
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: s.agentPermission(r.Context(), req.Token, req.Payload, chat.PermissionWaits{Permission: cfg.ChatPermissionTimeout(), Question: cfg.ChatQuestionTimeout()})})
}

// agentPermission routes a supervised agent's permission prompt to Leo
// Chat. An unknown token, a foreign session or chat being off is no
// decision.
func (s *Server) agentPermission(ctx context.Context, token string, payload json.RawMessage, waits chat.PermissionWaits) chat.Decision {
	none := chat.Decision{}
	if s.chat == nil || s.attention == nil {
		return none
	}
	name, known := s.attention.AgentForToken(token)
	if !known {
		return none
	}
	var p agentHookPayload
	_ = json.Unmarshal(payload, &p)
	if !s.isAgentHookSession(name, p.SessionID) {
		return none
	}
	return s.chat.RequestPermission(ctx, name, payload, waits)
}

// chatHook hands an agent hook to Leo Chat (auto-capture, terminal-answered
// permission prompts).
func (s *Server) chatHook(name string, raw json.RawMessage) {
	if s.chat == nil {
		return
	}
	var ev chat.HookEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		return
	}
	s.chat.OnHook(name, ev)
}
