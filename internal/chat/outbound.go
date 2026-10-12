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
	text := strings.TrimSpace(req.Text)
	switch {
	case text == "" && len(req.Files) == 0:
		return Message{}, fmt.Errorf("%w: post needs text or files", ErrInvalid)
	case len(req.Text) > MaxTextBytes:
		return Message{}, fmt.Errorf("%w: text exceeds %d bytes", ErrInvalid, MaxTextBytes)
	case len(req.Files) > MaxPostFiles:
		return Message{}, fmt.Errorf("%w: at most %d files", ErrInvalid, MaxPostFiles)
	}
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

// AskRequest is leo_chat_ask.
type AskRequest struct {
	Caller   string
	Task     string
	Question string
	Options  []Option
	Timeout  time.Duration
}

// AskResult is leo_chat_ask's answer.
type AskResult struct {
	Value    string `json:"value"`
	Label    string `json:"label"`
	TimedOut bool   `json:"timed_out"`
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

// Ask posts a question with options to the caller's thread and blocks until
// it is answered, times out, or ctx ends (both expire it).
func (s *Service) Ask(ctx context.Context, req AskRequest) (AskResult, error) {
	timeout, err := validateAsk(req)
	if err != nil {
		return AskResult{}, err
	}
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
		m.Task = req.Task
	}
	answer, settled := s.await(ctx, m, timeout)
	if !settled {
		return AskResult{TimedOut: true}, nil
	}
	for _, o := range options {
		if o.Value == answer {
			return AskResult{Value: o.Value, Label: o.Label}, nil
		}
	}
	return AskResult{TimedOut: true}, nil
}

// Answer settles an open ask or permission message from the apps.
func (s *Service) Answer(agent, id, value string) (Message, error) {
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
	agent, _, err := s.thread(req.Caller, req.RunTask)
	if err != nil {
		return PageResult{}, err
	}
	return s.Messages(agent, req.Page)
}
