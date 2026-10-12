package chat

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
)

// Ask limits.
const (
	MaxAskOptions     = 12
	DefaultAskTimeout = 300 * time.Second
	MaxAskTimeout     = time.Hour
	// MaxPostFiles caps the files one leo_chat_post copies.
	MaxPostFiles = MaxAttachmentsPerMessage
)

// taskCallerPrefix starts the process name of a headless task run.
const taskCallerPrefix = "task:"

// PostRequest is leo_chat_post. Caller is the calling process (an agent
// name, or task:<name> for a headless run); Task and RunID come from the
// run's LEO_TASK / LEO_RUN_ID.
type PostRequest struct {
	Caller string
	Task   string
	RunID  string
	Text   string
	Files  []string
}

// withRun fills in the task run a caller's chat output belongs to when the
// caller names none: an agent running a persistent task's turn has no
// LEO_TASK of its own (it serves every turn), so the run is the one the
// daemon has in flight in it.
func (s *Service) withRun(caller, task, runID string) (string, string) {
	if task != "" || s.opts.TaskRun == nil || strings.HasPrefix(caller, taskCallerPrefix) {
		return task, runID
	}
	if t, r, ok := s.opts.TaskRun(caller); ok {
		return t, r
	}
	return task, runID
}

// thread resolves the thread a caller posts to: its own for an agent, the
// task owner's for a run. note is a system note to post first (the owner
// was missing).
func (s *Service) thread(caller, task string) (agent, note string, err error) {
	if task != "" {
		cfg, err := s.opts.Config()
		if err != nil {
			return "", "", fmt.Errorf("loading config: %w", err)
		}
		if _, ok := cfg.Tasks[task]; !ok {
			return "", "", fmt.Errorf("%w: unknown task %q", ErrInvalid, task)
		}
		owner := ResolveOwner(cfg, task, s.opts.Agents())
		if owner.Missing != "" {
			note = fmt.Sprintf("Task %s names owner %q, which is not an agent; its output posts here.", task, owner.Missing)
		}
		return owner.Agent, note, nil
	}
	if strings.HasPrefix(caller, taskCallerPrefix) {
		return "", "", fmt.Errorf("%w: a task run must report LEO_TASK", ErrInvalid)
	}
	if !config.IsAgentName(caller) {
		return "", "", fmt.Errorf("%w: %q is not an agent; only supervised agents and task runs may use Leo Chat", ErrInvalid, caller)
	}
	if !s.knownAgent(caller) {
		return "", "", fmt.Errorf("%w: %q is not a supervised agent", ErrInvalid, caller)
	}
	return caller, "", nil
}

// Post adds an agent's (or a task run's) message to its thread, copying
// files into the attachment store.
func (s *Service) Post(_ context.Context, req PostRequest) (Message, error) {
	if err := s.enter(); err != nil {
		return Message{}, err
	}
	defer s.leave()
	text := strings.TrimSpace(req.Text)
	switch {
	case text == "" && len(req.Files) == 0:
		return Message{}, fmt.Errorf("%w: post needs text or files", ErrInvalid)
	case len(req.Text) > MaxTextBytes:
		return Message{}, fmt.Errorf("%w: text exceeds %d bytes", ErrInvalid, MaxTextBytes)
	case len(req.Files) > MaxPostFiles:
		return Message{}, fmt.Errorf("%w: at most %d files", ErrInvalid, MaxPostFiles)
	}
	req.Task, req.RunID = s.withRun(req.Caller, req.Task, req.RunID)
	agent, note, err := s.thread(req.Caller, req.Task)
	if err != nil {
		return Message{}, err
	}
	if note != "" {
		s.postSystem(agent, note)
	}
	kind := KindText
	switch {
	case req.Task != "":
		kind = KindTask
	case text == "":
		kind = KindFile
	}
	m := Message{Agent: agent, Direction: DirectionOut, Kind: kind, Text: text}
	if req.Task != "" {
		m.Task, m.RunID = req.Task, req.RunID
	}
	// The files are copied before the message takes its id (see insert):
	// a slow copy must not hold an id that later messages pass.
	files, err := s.copyFiles(agent, req.Files)
	if err != nil {
		return Message{}, err
	}
	m, err = s.insert(m, files, nil, nil)
	if err != nil {
		s.removeFiles(files)
		return Message{}, err
	}
	stored, err := s.opts.Store.Message(m.ID)
	if err != nil {
		return Message{}, err
	}
	s.publish(EventMessage, messageEvent(stored))
	return stored, nil
}

// copyFiles copies each file into the store, returning the attachments for
// the message that carries them. On any failure the copies already made are
// removed.
func (s *Service) copyFiles(agent string, files []string) ([]Attachment, error) {
	var stored []Attachment
	for _, src := range files {
		id := s.opts.IDs.New()
		path, name, n, err := s.opts.Files.Copy(agent, id, src)
		if err != nil {
			s.removeFiles(stored)
			return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		stored = append(stored, Attachment{ID: id, Agent: agent, Filename: name, MIME: DetectMIME(name, "", nil), Bytes: n, Path: path, Created: s.opts.Now()})
	}
	return stored, nil
}

func (s *Service) removeFiles(as []Attachment) {
	for _, a := range as {
		_ = s.opts.Files.Remove(a.Path)
	}
}

// AskRequest is leo_chat_ask: a new question, or with MessageID the resume
// of one an earlier call left pending.
type AskRequest struct {
	Caller    string
	Task      string
	RunID     string
	Question  string
	Options   []Option
	Timeout   time.Duration
	MessageID string
	// Wait bounds this call's wait (0: until the ask settles). A call can be
	// cut off sooner than the ask's own deadline — a harness kills a tool
	// call at its MCP ceiling — so the ask outlives the call and is resumed.
	Wait time.Duration
}

// AskResult is leo_chat_ask's answer. Pending says the call's wait ended
// with the question still open: ask again with MessageID to keep waiting.
type AskResult struct {
	Value     string `json:"value"`
	Label     string `json:"label"`
	TimedOut  bool   `json:"timed_out"`
	Pending   bool   `json:"pending,omitempty"`
	MessageID string `json:"message_id,omitempty"`
}

func validateAsk(req AskRequest) (time.Duration, error) {
	timeout := req.Timeout
	if timeout == 0 {
		timeout = DefaultAskTimeout
	}
	switch {
	case strings.TrimSpace(req.Question) == "":
		return 0, fmt.Errorf("%w: question is required", ErrInvalid)
	case len(req.Question) > MaxTextBytes:
		return 0, fmt.Errorf("%w: question exceeds %d bytes", ErrInvalid, MaxTextBytes)
	case len(req.Options) == 0 || len(req.Options) > MaxAskOptions:
		return 0, fmt.Errorf("%w: give 1 to %d options", ErrInvalid, MaxAskOptions)
	case timeout < 0 || timeout > MaxAskTimeout:
		return 0, fmt.Errorf("%w: timeout must be at most %s", ErrInvalid, MaxAskTimeout)
	case req.Wait < 0:
		return 0, fmt.Errorf("%w: wait must not be negative", ErrInvalid)
	}
	seen := map[string]bool{}
	for _, o := range req.Options {
		if strings.TrimSpace(o.Value) == "" || seen[o.Value] {
			return 0, fmt.Errorf("%w: option values must be non-empty and distinct", ErrInvalid)
		}
		seen[o.Value] = true
	}
	return timeout, nil
}

// Ask posts a question with options to the caller's thread and waits for
// the answer: until it is answered or its own deadline (Timeout) passes,
// which expires it, or until the call's Wait or ctx ends first, which
// leaves it open and the result Pending.
func (s *Service) Ask(ctx context.Context, req AskRequest) (AskResult, error) {
	if err := s.enter(); err != nil {
		return AskResult{}, err
	}
	defer s.leave()
	if req.MessageID != "" {
		return s.resumeAsk(ctx, req)
	}
	timeout, err := validateAsk(req)
	if err != nil {
		return AskResult{}, err
	}
	req.Task, req.RunID = s.withRun(req.Caller, req.Task, req.RunID)
	agent, note, err := s.thread(req.Caller, req.Task)
	if err != nil {
		return AskResult{}, err
	}
	if note != "" {
		s.postSystem(agent, note)
	}
	options := make([]Option, len(req.Options))
	for i, o := range req.Options {
		options[i] = o
		if options[i].Label == "" {
			options[i].Label = o.Value
		}
	}
	m := Message{Agent: agent, Direction: DirectionOut, Kind: KindAsk, Text: strings.TrimSpace(req.Question), State: StateOpen, Options: options}
	if req.Task != "" {
		m.Task, m.RunID = req.Task, req.RunID
	}
	m, wt, err := s.insertOpen(m)
	if err != nil {
		return AskResult{}, err
	}
	deadline := s.opts.After(timeout)
	// The deadline is the ask's own: the call holds no lease on it. Close
	// waits for it (active), and cancelling s.ctx ends it.
	s.active.Add(1)
	go s.expireAsk(m.ID, wt, deadline)
	return s.waitAsk(ctx, m.ID, wt, req.Wait), nil
}

// expireAsk expires ask id at its deadline, or at shutdown, unless it is
// answered first, and wakes everyone waiting on it.
func (s *Service) expireAsk(id string, wt *waiter, deadline <-chan time.Time) {
	defer s.active.Done()
	defer s.waiters.remove(id)
	select {
	case <-wt.done:
		return
	case <-deadline:
	case <-s.ctx.Done():
	}
	ok, err := s.opts.Store.Transition(id, []string{StateOpen}, StateExpired, "")
	if err != nil {
		s.opts.Logf("chat: expiring ask %s: %v", id, err)
	}
	if ok {
		s.publishUpdated(id, ChangeState)
	}
	s.waiters.settle(id, "")
}

// waitAsk waits for ask id to settle, up to window (0: no bound) or ctx's
// end, either of which leaves it pending.
func (s *Service) waitAsk(ctx context.Context, id string, wt *waiter, window time.Duration) AskResult {
	var windowEnd <-chan time.Time
	if window > 0 {
		windowEnd = s.opts.After(window)
	}
	select {
	case <-wt.done:
		return s.askOutcome(id)
	case <-windowEnd:
	case <-ctx.Done():
	}
	return AskResult{Pending: true, MessageID: id}
}

// askOutcome is a settled ask's result: the chosen option, else timed out.
func (s *Service) askOutcome(id string) AskResult {
	m, err := s.opts.Store.Message(id)
	if err == nil && m.State == StateAnswered {
		for _, o := range m.Options {
			if o.Value == m.Answer {
				return AskResult{Value: o.Value, Label: o.Label, MessageID: id}
			}
		}
	}
	return AskResult{TimedOut: true, MessageID: id}
}

// resumeAsk waits again on an ask an earlier call of the same caller left
// pending.
func (s *Service) resumeAsk(ctx context.Context, req AskRequest) (AskResult, error) {
	switch {
	case !validID(req.MessageID):
		return AskResult{}, fmt.Errorf("%w: message_id must be a message id", ErrInvalid)
	case req.Question != "" || len(req.Options) > 0:
		return AskResult{}, fmt.Errorf("%w: resume an ask with message_id alone", ErrInvalid)
	case req.Wait < 0:
		return AskResult{}, fmt.Errorf("%w: wait must not be negative", ErrInvalid)
	}
	task, _ := s.withRun(req.Caller, req.Task, req.RunID)
	agent, _, err := s.thread(req.Caller, task)
	if err != nil {
		return AskResult{}, err
	}
	m, err := s.opts.Store.Message(req.MessageID)
	if err != nil || m.Agent != agent || m.Kind != KindAsk {
		return AskResult{}, fmt.Errorf("%w: %s is not an ask in %s's thread", ErrInvalid, req.MessageID, agent)
	}
	wt, ok := s.waiters.get(m.ID)
	if !ok {
		return s.askOutcome(m.ID), nil
	}
	return s.waitAsk(ctx, m.ID, wt, req.Wait), nil
}

// Answer settles an open ask or permission message from the apps.
func (s *Service) Answer(agent, id, value string) (Message, error) {
	if err := s.enter(); err != nil {
		return Message{}, err
	}
	defer s.leave()
	m, err := s.opts.Store.Message(id)
	if err != nil || m.Agent != agent {
		return Message{}, ErrNotFound
	}
	if err := validAnswer(m, value); err != nil {
		return Message{}, err
	}
	if m.State != StateOpen {
		return Message{}, ErrConflict
	}
	ok, err := s.opts.Store.Transition(id, []string{StateOpen}, StateAnswered, value)
	if err != nil {
		return Message{}, err
	}
	if !ok {
		return Message{}, ErrConflict
	}
	s.waiters.settle(id, value)
	s.publishUpdated(id, ChangeAnswer)
	return s.opts.Store.Message(id)
}

func validAnswer(m Message, value string) error {
	switch m.Kind {
	case KindAsk:
		for _, o := range m.Options {
			if o.Value == value {
				return nil
			}
		}
		return fmt.Errorf("%w: %q is not one of the options", ErrInvalid, value)
	case KindPermission:
		if value == "allow" || value == "deny" {
			return nil
		}
		return fmt.Errorf("%w: answer allow or deny", ErrInvalid)
	}
	return fmt.Errorf("%w: only ask and permission messages take answers", ErrInvalid)
}

// HistoryRequest is leo_chat_history.
type HistoryRequest struct {
	Caller  string
	RunTask string // the calling run's LEO_TASK, which picks the owner's thread
	Page    Page
}

// History returns a page of the caller's own thread.
func (s *Service) History(_ context.Context, req HistoryRequest) (PageResult, error) {
	task, _ := s.withRun(req.Caller, req.RunTask, "")
	agent, _, err := s.thread(req.Caller, task)
	if err != nil {
		return PageResult{}, err
	}
	return s.Messages(agent, req.Page)
}
