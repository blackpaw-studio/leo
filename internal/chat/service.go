package chat

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/observe"
)

// Request errors, mapped to HTTP statuses by the web layer.
var (
	ErrInvalid      = errors.New("invalid chat request")
	ErrUnknownAgent = errors.New("unknown agent")
	ErrConflict     = errors.New("message already settled")
)

// Limits.
const (
	// MaxTextBytes caps one message's text.
	MaxTextBytes = 256 << 10
	// MaxAttachmentsPerMessage caps the uploads one message carries.
	MaxAttachmentsPerMessage = 10
	// deliverTimeout bounds one delivery, a cold agent start included.
	deliverTimeout = 4 * time.Minute
	// pruneInterval is how often attachment retention runs.
	pruneInterval = 24 * time.Hour
)

// Deliverer submits a prompt into an agent as a user turn.
type Deliverer interface {
	// Deliver delivers prompt into agent, starting it first if it is
	// stopped (calling starting before it does), and returns the agent's
	// harness name. ref is the id a durable (bridge) delivery is queued
	// under; see Options.Queued.
	Deliver(ctx context.Context, agent, prompt, ref string, starting func()) (harness string, err error)
}

// Options are a Service's dependencies.
type Options struct {
	Store   *Store
	Files   Files
	Deliver Deliverer
	// Transcribe turns a voice note into text (Transcriber.Transcribe).
	Transcribe func(ctx context.Context, path string) (string, error)
	Publish    observe.Publisher
	Now        func() time.Time
	IDs        IDSource
	// After is the timer ask and permission waits use (time.After).
	After func(time.Duration) <-chan time.Time
	// Agents lists every known agent, running or stopped.
	Agents func() []AgentRef
	Config func() (*config.Config, error)
	// TaskRun returns the persistent task and run whose turn is running in
	// agent, if any. Nil runs none.
	TaskRun func(agent string) (task, runID string, ok bool)
	// Queued reports whether agent's durable outbox still holds the
	// delivery queued under ref, which its next launch then delivers. Nil
	// holds nothing.
	Queued func(agent, ref string) (bool, error)
	Logf   func(format string, args ...any)
}

// Service is Leo Chat's daemon side. Methods are safe for concurrent use.
type Service struct {
	opts Options

	queues  *serialQueues
	waiters *waiters
	// insertMu is the one point every message is stored through: it takes
	// the message's id and stores it under one lock, so ids increase in
	// store order and a message never lands below a read watermark or a
	// paging cursor a client already took.
	insertMu sync.Mutex
	// marks is each agent's last answered prompt stamp (ResolveCaptures).
	marks *captureMarks
	stop  chan struct{}
	once  sync.Once
}

// NewService builds a Service; zero-valued options take defaults.
func NewService(opts Options) *Service {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.IDs == nil {
		opts.IDs = NewULIDSource(opts.Now, nil)
	}
	if opts.After == nil {
		opts.After = time.After
	}
	if opts.Logf == nil {
		opts.Logf = log.Printf
	}
	if opts.Agents == nil {
		opts.Agents = func() []AgentRef { return nil }
	}
	return &Service{opts: opts, queues: newSerialQueues(), waiters: newWaiters(), marks: &captureMarks{m: make(map[string]time.Time)}, stop: make(chan struct{})}
}

// Recover runs once at daemon start: prompts left open by the previous
// daemon have no waiter any more, so they expire.
func (s *Service) Recover() error {
	ids, err := s.opts.Store.ExpireOpen()
	if err != nil {
		return err
	}
	for _, id := range ids {
		s.publishUpdated(id, ChangeState)
	}
	pending, err := s.opts.Store.MessagesInState(StatePending)
	if err != nil {
		return fmt.Errorf("reading undelivered messages: %w", err)
	}
	for _, m := range pending {
		s.recoverPending(m)
	}
	return nil
}

// recoverPending settles a message the previous daemon left pending. Its
// delivery queues lived in memory, but a bridge delivery also waits in the
// agent's durable outbox, which the agent's next launch delivers: that one
// is delivered, and failing it would have the user send it twice. Any other
// was never delivered, or not knowably so, and failing it visibly beats
// replaying it twice.
func (s *Service) recoverPending(m Message) {
	state := StateFailed
	if m.DeliveryRef != "" && s.opts.Queued != nil {
		held, err := s.opts.Queued(m.Agent, m.DeliveryRef)
		if err != nil {
			s.opts.Logf("chat: checking the outbox of %s for %s: %v", m.Agent, m.ID, err)
		}
		if held {
			state = StateDelivered
		}
	}
	ok, err := s.opts.Store.Transition(m.ID, []string{StatePending}, state, "")
	if err != nil || !ok {
		if err != nil {
			s.opts.Logf("chat: settling undelivered %s: %v", m.ID, err)
		}
		return
	}
	s.publishUpdated(m.ID, ChangeState)
	if state == StateFailed {
		s.postSystem(m.Agent, "Not delivered: the daemon restarted first. Send it again.")
	}
}

// deliveryRef is the id message id is delivered under: a bridge command id
// the mod dedups by, so a repeat of the same delivery is re-acked.
func deliveryRef(id string) string { return "chat-" + id }

// Run prunes expired attachments now and then daily until ctx ends.
func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(pruneInterval)
	defer ticker.Stop()
	for {
		if err := s.Prune(); err != nil {
			s.opts.Logf("chat: pruning attachments: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-s.stop:
			return
		case <-ticker.C:
		}
	}
}

// Prune deletes attachment files past AttachmentRetention; their rows stay.
func (s *Service) Prune() error {
	paths, err := s.opts.Store.PruneAttachments(s.opts.Now().Add(-AttachmentRetention))
	if err != nil {
		return err
	}
	for _, p := range paths {
		if err := s.opts.Files.Remove(p); err != nil {
			s.opts.Logf("chat: removing %s: %v", p, err)
		}
	}
	return nil
}

// Flush waits for every queued delivery to finish.
func (s *Service) Flush() { s.queues.wait() }

// Close stops background work and waits for deliveries in flight.
func (s *Service) Close() {
	s.once.Do(func() { close(s.stop) })
	s.queues.wait()
}

func (s *Service) knownAgent(name string) bool {
	for _, a := range s.opts.Agents() {
		if a.Name == name {
			return true
		}
	}
	return false
}

// Threads lists every thread with messages.
func (s *Service) Threads() ([]Thread, error) { return s.opts.Store.Threads() }

// Messages returns a page of agent's thread.
func (s *Service) Messages(agent string, p Page) (PageResult, error) {
	if !config.IsAgentName(agent) {
		return PageResult{}, fmt.Errorf("%w: agent name %q", ErrInvalid, agent)
	}
	if p.Before != "" && !validID(p.Before) {
		return PageResult{}, fmt.Errorf("%w: before must be a message id", ErrInvalid)
	}
	msgs, more, err := s.opts.Store.Messages(agent, p)
	if err != nil {
		return PageResult{}, err
	}
	return PageResult{Messages: msgs, HasMore: more}, nil
}

// MarkRead marks agent's thread read up to id.
func (s *Service) MarkRead(agent, id string) error {
	if !config.IsAgentName(agent) || !validID(id) {
		return fmt.Errorf("%w: agent and message id required", ErrInvalid)
	}
	return s.opts.Store.MarkRead(agent, id)
}

// Attachment returns an attachment's record for serving.
func (s *Service) Attachment(id string) (Attachment, error) {
	if !validID(id) {
		return Attachment{}, ErrNotFound
	}
	return s.opts.Store.Attachment(id)
}

// Upload stores one file for a later message and returns its record. Audio
// uploads make that message a voice note. agent is optional: an upload
// without one is claimed by the first message that sends it.
func (s *Service) Upload(agent, filename, declaredMIME string, r io.Reader) (Attachment, error) {
	if agent != "" && !config.IsAgentName(agent) {
		return Attachment{}, fmt.Errorf("%w: agent name %q", ErrInvalid, agent)
	}
	if agent != "" && !s.knownAgent(agent) {
		return Attachment{}, fmt.Errorf("%w: %s", ErrUnknownAgent, agent)
	}
	id := s.opts.IDs.New()
	br := bufio.NewReaderSize(r, 512)
	head, _ := br.Peek(512)
	mt := DetectMIME(filename, declaredMIME, head)
	path, n, err := s.opts.Files.Save(agent, id, filename, br)
	if err != nil {
		if errors.Is(err, ErrTooLarge) {
			return Attachment{}, err
		}
		return Attachment{}, fmt.Errorf("storing upload: %w", err)
	}
	a := Attachment{ID: id, Agent: agent, Filename: SanitizeFilename(filename), MIME: mt, Bytes: n, Path: path, Available: true, Created: s.opts.Now()}
	if err := s.opts.Store.InsertAttachment(a); err != nil {
		_ = s.opts.Files.Remove(path)
		return Attachment{}, err
	}
	return a, nil
}

// SendRequest is an inbound message from the apps.
type SendRequest struct {
	Text          string   `json:"text"`
	ReplyTo       string   `json:"reply_to,omitempty"`
	AttachmentIDs []string `json:"attachment_ids,omitempty"`
}

// Send records an inbound message (pending) and queues its delivery into
// the agent: voice notes are transcribed first, a stopped agent is started.
func (s *Service) Send(_ context.Context, agent string, req SendRequest) (Message, error) {
	if !config.IsAgentName(agent) {
		return Message{}, fmt.Errorf("%w: agent name %q", ErrInvalid, agent)
	}
	if !s.knownAgent(agent) {
		return Message{}, fmt.Errorf("%w: %s", ErrUnknownAgent, agent)
	}
	if err := validateSend(req); err != nil {
		return Message{}, err
	}
	if req.ReplyTo != "" {
		target, err := s.opts.Store.Message(req.ReplyTo)
		if err != nil || target.Agent != agent {
			return Message{}, fmt.Errorf("%w: reply_to %s is not a message in %s's thread", ErrInvalid, req.ReplyTo, agent)
		}
	}
	kind, err := s.inboundKind(agent, req.AttachmentIDs)
	if err != nil {
		return Message{}, err
	}
	m, err := s.insert(Message{Agent: agent, Direction: DirectionIn, Kind: kind, Text: strings.TrimSpace(req.Text), ReplyTo: req.ReplyTo, State: StatePending}, nil, req.AttachmentIDs, nil)
	if errors.Is(err, ErrUnusable) {
		return Message{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if err != nil {
		return Message{}, err
	}
	stored, err := s.opts.Store.Message(m.ID)
	if err != nil {
		return Message{}, err
	}
	s.publish(EventMessage, messageEvent(stored))
	s.queues.enqueue(agent, func() { s.deliver(stored) })
	return stored, nil
}

func validateSend(req SendRequest) error {
	switch {
	case strings.TrimSpace(req.Text) == "" && len(req.AttachmentIDs) == 0:
		return fmt.Errorf("%w: a message needs text or attachments", ErrInvalid)
	case len(req.Text) > MaxTextBytes:
		return fmt.Errorf("%w: text exceeds %d bytes", ErrInvalid, MaxTextBytes)
	case len(req.AttachmentIDs) > MaxAttachmentsPerMessage:
		return fmt.Errorf("%w: at most %d attachments", ErrInvalid, MaxAttachmentsPerMessage)
	}
	for _, id := range req.AttachmentIDs {
		if !validID(id) {
			return fmt.Errorf("%w: attachment id %q", ErrInvalid, id)
		}
	}
	return nil
}

// inboundKind is voice when any upload is audio, file for other uploads,
// else text. Uploads must be agent's own and unused.
func (s *Service) inboundKind(agent string, ids []string) (string, error) {
	kind := KindText
	for _, id := range ids {
		a, err := s.opts.Store.Attachment(id)
		if err != nil || (a.Agent != agent && a.Agent != "") || a.MessageID != "" {
			return "", fmt.Errorf("%w: attachment %s is not an unused upload for %s", ErrInvalid, id, agent)
		}
		switch {
		case IsAudio(a.MIME):
			kind = KindVoice
		case kind == KindText:
			kind = KindFile
		}
	}
	return kind, nil
}

// deliver runs on agent's serial queue: transcribe, frame, deliver, then
// record the capture and the outcome.
func (s *Service) deliver(m Message) {
	ctx, cancel := context.WithTimeout(context.Background(), deliverTimeout)
	defer cancel()
	d := Delivery{Text: m.Text, Voice: m.Kind == KindVoice}
	for _, a := range m.Attachments {
		if a.Path != "" {
			d.Attachments = append(d.Attachments, a.Path)
		}
	}
	if d.Voice {
		d.Transcript, d.TranscriptionFailed = s.transcribe(ctx, m)
	}
	if m.ReplyTo != "" {
		if target, err := s.opts.Store.Message(m.ReplyTo); err == nil {
			d.ReplyTo = &target
		}
	}
	prompt := FramePrompt(d)
	// The capture is recorded before delivery so even an instant turn's
	// Stop hook finds it.
	capture := Capture{MessageID: m.ID, Agent: m.Agent, Prompt: prompt, DeliveredAt: s.opts.Now()}
	if err := s.opts.Store.AddCapture(capture); err != nil {
		s.opts.Logf("chat: recording capture for %s: %v", m.ID, err)
	}
	// The ref is stored before delivery: a restart while the delivery waits
	// in the agent's outbox must find it there (see recoverPending).
	ref := deliveryRef(m.ID)
	if err := s.opts.Store.SetDeliveryRef(m.ID, ref); err != nil {
		s.opts.Logf("chat: recording the delivery ref of %s: %v", m.ID, err)
	}
	harness, err := s.opts.Deliver.Deliver(ctx, m.Agent, prompt, ref, func() {
		s.postSystem(m.Agent, "Starting "+m.Agent+"…")
	})
	if err != nil || harness != "claude" {
		// Only claude agents' replies are auto-captured; the others post
		// with leo_chat_post.
		_, _ = s.opts.Store.RemoveCapture(m.ID)
	}
	if err != nil {
		s.opts.Logf("chat: delivering %s to %s: %v", m.ID, m.Agent, err)
		if ok, _ := s.opts.Store.Transition(m.ID, []string{StatePending}, StateFailed, ""); ok {
			s.publishUpdated(m.ID, ChangeState)
		}
		s.postSystem(m.Agent, "Delivery failed: "+clipRunes(err.Error(), 300))
		return
	}
	if ok, _ := s.opts.Store.Transition(m.ID, []string{StatePending}, StateDelivered, ""); ok {
		s.publishUpdated(m.ID, ChangeState)
	}
}

// transcribe transcribes m's first audio attachment, storing and announcing
// the transcript; failed reports that it could not.
func (s *Service) transcribe(ctx context.Context, m Message) (transcript string, failed bool) {
	for _, a := range m.Attachments {
		if !IsAudio(a.MIME) || a.Path == "" {
			continue
		}
		if s.opts.Transcribe == nil {
			return "", true
		}
		text, err := s.opts.Transcribe(ctx, a.Path)
		if err != nil {
			s.opts.Logf("chat: transcribing %s: %v", m.ID, err)
			return "", true
		}
		if err := s.opts.Store.SetTranscript(m.ID, text); err != nil {
			s.opts.Logf("chat: storing transcript of %s: %v", m.ID, err)
		}
		s.publishUpdated(m.ID, ChangeTranscript)
		return text, false
	}
	return "", true
}

// insert stores m — with its new attachments add and the earlier uploads
// link — under insertMu, taking its id and time there; taken (may be nil)
// sees the id first.
func (s *Service) insert(m Message, add []Attachment, link []string, taken func(id string)) (Message, error) {
	s.insertMu.Lock()
	defer s.insertMu.Unlock()
	m.ID, m.TS = s.opts.IDs.New(), s.opts.Now()
	if taken != nil {
		taken(m.ID)
	}
	if err := s.opts.Store.InsertMessageWith(m, add, link); err != nil {
		return Message{}, err
	}
	return m, nil
}

// postSystem adds a daemon note to agent's thread.
func (s *Service) postSystem(agent, text string) {
	m, err := s.insert(Message{Agent: agent, Direction: DirectionOut, Kind: KindSystem, Text: text}, nil, nil, nil)
	if err != nil {
		s.opts.Logf("chat: system note for %s: %v", agent, err)
		return
	}
	s.publish(EventMessage, messageEvent(m))
}

func (s *Service) publish(t observe.EventType, p observe.Payload) {
	if s.opts.Publish != nil {
		s.opts.Publish.Publish(observe.Event{Type: t, Payload: p})
	}
}

func (s *Service) publishUpdated(id, change string) {
	m, err := s.opts.Store.Message(id)
	if err != nil {
		s.opts.Logf("chat: reading %s for an update event: %v", id, err)
		return
	}
	s.publish(EventUpdated, updatedEvent(m, change))
}

// serialQueues runs each agent's jobs one at a time, in order.
type serialQueues struct {
	mu      sync.Mutex
	pending map[string][]func()
	wg      sync.WaitGroup
}

func newSerialQueues() *serialQueues { return &serialQueues{pending: map[string][]func(){}} }

func (q *serialQueues) enqueue(key string, job func()) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.wg.Add(1)
	_, running := q.pending[key]
	q.pending[key] = append(q.pending[key], job)
	if !running {
		go q.drain(key)
	}
}

func (q *serialQueues) drain(key string) {
	for {
		q.mu.Lock()
		jobs := q.pending[key]
		if len(jobs) == 0 {
			delete(q.pending, key)
			q.mu.Unlock()
			return
		}
		job := jobs[0]
		q.pending[key] = jobs[1:]
		q.mu.Unlock()
		job()
		q.wg.Done()
	}
}

func (q *serialQueues) wait() { q.wg.Wait() }
