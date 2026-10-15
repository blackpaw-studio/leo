package chat

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/observe"
)

type fakeDeliverer struct {
	mu        sync.Mutex
	prompts   []string
	refs      []string
	harness   string
	err       error
	stopped   map[string]bool
	onDeliver func()
}

func (f *fakeDeliverer) Deliver(_ context.Context, agent, prompt, ref string, starting func()) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refs = append(f.refs, ref)
	if f.stopped[agent] {
		starting()
	}
	if f.onDeliver != nil {
		f.onDeliver()
	}
	if f.err != nil {
		return "", f.err
	}
	f.prompts = append(f.prompts, prompt)
	h := f.harness
	if h == "" {
		h = "claude"
	}
	return h, nil
}

func (f *fakeDeliverer) delivered() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.prompts...)
}

type recordingPublisher struct {
	mu     sync.Mutex
	events []observe.Event
	// msgs carries every EventMessage's message to waitMessage.
	msgs chan Message
	// upds carries every EventUpdated payload to waitUpdate; a full buffer
	// drops, as no test that waits on it publishes that many.
	upds chan UpdatedPayload
}

func newRecordingPublisher() *recordingPublisher {
	return &recordingPublisher{msgs: make(chan Message, 256), upds: make(chan UpdatedPayload, 256)}
}

func (p *recordingPublisher) Publish(ev observe.Event) {
	p.mu.Lock()
	p.events = append(p.events, ev)
	p.mu.Unlock()
	if mp, ok := ev.Payload.(*MessagePayload); ok && ev.Type == EventMessage {
		p.msgs <- mp.Message
	}
	if up, ok := ev.Payload.(*UpdatedPayload); ok && ev.Type == EventUpdated {
		select {
		case p.upds <- *up:
		default:
		}
	}
}

// waitUpdate returns the next unconsumed chat.updated payload, failing the
// test if none arrives (a guard, not a wait the passing path pays).
func (p *recordingPublisher) waitUpdate(t *testing.T) UpdatedPayload {
	t.Helper()
	select {
	case u := <-p.upds:
		return u
	case <-time.After(10 * time.Second):
		t.Fatal("no chat.updated event")
		return UpdatedPayload{}
	}
}

// waitMessage returns the next unconsumed EventMessage's message, failing
// the test if none arrives (a guard, not a wait the passing path pays).
func (p *recordingPublisher) waitMessage(t *testing.T) Message {
	t.Helper()
	select {
	case m := <-p.msgs:
		return m
	case <-time.After(10 * time.Second):
		t.Fatal("no chat.message event")
		return Message{}
	}
}

func (p *recordingPublisher) types() []observe.EventType {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]observe.EventType, len(p.events))
	for i, e := range p.events {
		out[i] = e.Type
	}
	return out
}

// manualTimers hands out timer channels the test fires by hand.
type manualTimers struct {
	mu  sync.Mutex
	chs []chan time.Time
	got chan time.Duration
}

func newManualTimers() *manualTimers { return &manualTimers{got: make(chan time.Duration, 16)} }

func (m *manualTimers) after(d time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	m.mu.Lock()
	m.chs = append(m.chs, ch)
	m.mu.Unlock()
	m.got <- d
	return ch
}

func (m *manualTimers) fireLast() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.chs[len(m.chs)-1] <- time.Time{}
}

// gatedIDs mints ULIDs; once armed, the next New mints its id, then blocks
// (after telling blocked) until release is closed — a caller that is slow
// between taking an id and using it.
type gatedIDs struct {
	src     *ULIDSource
	mu      sync.Mutex
	blocked chan struct{}
	release chan struct{}
}

func (g *gatedIDs) arm() (blocked, release chan struct{}) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.blocked, g.release = make(chan struct{}), make(chan struct{})
	return g.blocked, g.release
}

func (g *gatedIDs) New() string {
	g.mu.Lock()
	blocked, release := g.blocked, g.release
	g.blocked, g.release = nil, nil
	g.mu.Unlock()
	id := g.src.New()
	if blocked != nil {
		close(blocked)
		<-release
	}
	return id
}

type harnessFixture struct {
	svc     *Service
	store   *Store
	deliver *fakeDeliverer
	pub     *recordingPublisher
	clock   *testClock
	timers  *manualTimers
	ids     *gatedIDs
	// held is what the agents' durable outboxes hold, as agent/ref.
	held map[string]bool
	// runs is each agent's persistent-task run in flight, as task/run id.
	runs       map[string][2]string
	cfg        *config.Config
	root       string
	transcript func(ctx context.Context, path string) (string, error)
}

func newFixture(t *testing.T) *harnessFixture {
	t.Helper()
	f := &harnessFixture{
		store:   openTestStore(t),
		deliver: &fakeDeliverer{stopped: map[string]bool{}},
		pub:     newRecordingPublisher(),
		clock:   newTestClock(),
		timers:  newManualTimers(),
		ids:     &gatedIDs{},
		held:    map[string]bool{},
		runs:    map[string][2]string{},
		cfg:     &config.Config{HomePath: "/home/leo", Tasks: map[string]config.TaskConfig{}},
		root:    t.TempDir(),
	}
	f.transcript = func(context.Context, string) (string, error) { return "", errors.New("no whisper") }
	f.ids.src = NewULIDSource(f.clock.Now, nil)
	f.svc = f.newService()
	t.Cleanup(func() { f.svc.Close() })
	return f
}

// newService builds a Service on the fixture's store and seams.
func (f *harnessFixture) newService() *Service {
	return NewService(Options{
		Store:   f.store,
		Files:   Files{Root: f.root},
		Deliver: f.deliver,
		Transcribe: func(ctx context.Context, path string) (string, error) {
			return f.transcript(ctx, path)
		},
		Publish: f.pub,
		Now:     f.clock.Now,
		IDs:     f.ids,
		After:   f.timers.after,
		Agents: func() []AgentRef {
			return []AgentRef{{Name: "assistant", Workspace: "/home/leo/workspace"}, {Name: "web", Workspace: "/src/web"}}
		},
		Config: func() (*config.Config, error) { return f.cfg, nil },
		Queued: func(agent, ref string) (bool, error) { return f.held[agent+"/"+ref], nil },
		TaskRun: func(agent string) (string, string, bool) {
			r, ok := f.runs[agent]
			return r[0], r[1], ok
		},
	})
}

// restart closes the service and builds a new one on the same store, as a
// daemon restart does.
func (f *harnessFixture) restart(t *testing.T) {
	t.Helper()
	f.svc.Close()
	f.svc = f.newService()
}

func (f *harnessFixture) thread(t *testing.T, agent string) []Message {
	t.Helper()
	ms, _, err := f.store.Messages(agent, Page{})
	if err != nil {
		t.Fatal(err)
	}
	return ms
}

func TestSendDeliversFramedPromptAndRecordsCapture(t *testing.T) {
	f := newFixture(t)
	m, err := f.svc.Send(context.Background(), "assistant", SendRequest{Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if m.State != StatePending || m.Direction != DirectionIn || m.Kind != KindText {
		t.Fatalf("returned message = %+v", m)
	}
	f.svc.Flush()
	if got := f.deliver.delivered(); len(got) != 1 || got[0] != "[Leo Chat] hello" {
		t.Fatalf("delivered %q", got)
	}
	stored, _ := f.store.Message(m.ID)
	if stored.State != StateDelivered {
		t.Fatalf("state = %s", stored.State)
	}
	caps, _ := f.store.Captures("assistant")
	if len(caps) != 1 || caps[0].Prompt != "[Leo Chat] hello" || caps[0].MessageID != m.ID {
		t.Fatalf("captures = %+v", caps)
	}
	types := f.pub.types()
	if len(types) < 2 || types[0] != EventMessage || types[len(types)-1] != EventUpdated {
		t.Fatalf("events = %v", types)
	}
}

func TestSendRejectsUnknownAgentAndEmptyMessage(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.Send(context.Background(), "ghost", SendRequest{Text: "hi"}); !errors.Is(err, ErrUnknownAgent) {
		t.Fatalf("unknown agent err = %v", err)
	}
	if _, err := f.svc.Send(context.Background(), "assistant", SendRequest{Text: "  "}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty message err = %v", err)
	}
}

func TestSendToStoppedAgentPostsStartingNote(t *testing.T) {
	f := newFixture(t)
	f.deliver.stopped["web"] = true
	if _, err := f.svc.Send(context.Background(), "web", SendRequest{Text: "wake up"}); err != nil {
		t.Fatal(err)
	}
	f.svc.Flush()
	thread := f.thread(t, "web")
	if len(thread) != 2 || thread[0].Kind != KindSystem || !strings.Contains(thread[0].Text, "Starting") {
		t.Fatalf("thread = %+v", thread)
	}
}

func TestSendDeliveryFailureMarksFailed(t *testing.T) {
	f := newFixture(t)
	f.deliver.err = errors.New("tmux gone")
	m, _ := f.svc.Send(context.Background(), "assistant", SendRequest{Text: "hi"})
	f.svc.Flush()
	stored, _ := f.store.Message(m.ID)
	if stored.State != StateFailed {
		t.Fatalf("state = %s", stored.State)
	}
	if caps, _ := f.store.Captures("assistant"); len(caps) != 0 {
		t.Fatalf("a failed delivery leaves no capture: %+v", caps)
	}
}

func TestSendNonClaudeAgentKeepsNoCapture(t *testing.T) {
	f := newFixture(t)
	f.deliver.harness = "codex"
	_, _ = f.svc.Send(context.Background(), "assistant", SendRequest{Text: "hi"})
	f.svc.Flush()
	if caps, _ := f.store.Captures("assistant"); len(caps) != 0 {
		t.Fatalf("codex agents use the tools, not auto-capture: %+v", caps)
	}
}

func upload(t *testing.T, f *harnessFixture, agent, name, mime, body string) Attachment {
	t.Helper()
	a, err := f.svc.Upload(agent, name, mime, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestSendVoiceTranscribesThenDelivers(t *testing.T) {
	f := newFixture(t)
	f.transcript = func(_ context.Context, path string) (string, error) {
		if !strings.HasSuffix(path, "note.m4a") {
			t.Errorf("transcribing %q", path)
		}
		return "call Sam at five", nil
	}
	a := upload(t, f, "assistant", "note.m4a", "audio/mp4", "AAC")
	m, err := f.svc.Send(context.Background(), "assistant", SendRequest{AttachmentIDs: []string{a.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if m.Kind != KindVoice {
		t.Fatalf("kind = %s", m.Kind)
	}
	f.svc.Flush()
	stored, _ := f.store.Message(m.ID)
	if stored.Transcript != "call Sam at five" {
		t.Fatalf("transcript = %q", stored.Transcript)
	}
	got := f.deliver.delivered()
	if len(got) != 1 || !strings.HasPrefix(got[0], "[Leo Chat] call Sam at five\nAttachments: "+f.root) {
		t.Fatalf("delivered %q", got)
	}
}

func TestSendVoiceTranscriptionFailureStillDelivers(t *testing.T) {
	f := newFixture(t)
	a := upload(t, f, "assistant", "note.m4a", "audio/mp4", "AAC")
	_, _ = f.svc.Send(context.Background(), "assistant", SendRequest{AttachmentIDs: []string{a.ID}})
	f.svc.Flush()
	got := f.deliver.delivered()
	if len(got) != 1 || !strings.Contains(got[0], "transcription failed") {
		t.Fatalf("delivered %q", got)
	}
}

func TestSendTakesAnUploadWithoutAnAgent(t *testing.T) {
	f := newFixture(t)
	a := upload(t, f, "", "x.txt", "text/plain", "x")
	m, err := f.svc.Send(context.Background(), "web", SendRequest{AttachmentIDs: []string{a.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if m.Kind != KindFile || len(m.Attachments) != 1 {
		t.Fatalf("message = %+v", m)
	}
	if _, err := f.svc.Send(context.Background(), "assistant", SendRequest{AttachmentIDs: []string{a.ID}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("reusing a claimed upload: err = %v", err)
	}
}

func TestEventsNameTheirAgent(t *testing.T) {
	f := newFixture(t)
	_, _ = f.svc.Send(context.Background(), "web", SendRequest{Text: "hi"})
	f.svc.Flush()
	f.pub.mu.Lock()
	defer f.pub.mu.Unlock()
	if len(f.pub.events) == 0 {
		t.Fatal("no events")
	}
	for _, ev := range f.pub.events {
		b, _ := json.Marshal(ev.Payload)
		if !strings.Contains(string(b), `"agent":"web","message":{`) {
			t.Fatalf("%s payload lacks a top-level agent: %s", ev.Type, b)
		}
	}
}

func TestSendRefusesAnotherAgentsUpload(t *testing.T) {
	f := newFixture(t)
	a := upload(t, f, "web", "x.txt", "text/plain", "x")
	if _, err := f.svc.Send(context.Background(), "assistant", SendRequest{Text: "see", AttachmentIDs: []string{a.ID}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v", err)
	}
}

func TestSendReplyToTaskCardQuotesIt(t *testing.T) {
	f := newFixture(t)
	f.cfg.Tasks["daily"] = config.TaskConfig{Owner: "assistant"}
	card, err := f.svc.Post(context.Background(), PostRequest{Caller: "task:daily", Task: "daily", RunID: "r1", Text: "1. rates\n2. jobs"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Send(context.Background(), "assistant", SendRequest{Text: "more on 2", ReplyTo: card.ID}); err != nil {
		t.Fatal(err)
	}
	f.svc.Flush()
	got := f.deliver.delivered()
	if len(got) != 1 || !strings.Contains(got[0], "\nIn reply to task daily run r1 (") || !strings.Contains(got[0], "1. rates 2. jobs") && !strings.Contains(got[0], "1. rates\n2. jobs") {
		t.Fatalf("delivered %q", got)
	}
}

func TestSendDeliversInOrderPerAgent(t *testing.T) {
	f := newFixture(t)
	for _, text := range []string{"one", "two", "three"} {
		if _, err := f.svc.Send(context.Background(), "assistant", SendRequest{Text: text}); err != nil {
			t.Fatal(err)
		}
	}
	f.svc.Flush()
	got := f.deliver.delivered()
	if strings.Join(got, "|") != "[Leo Chat] one|[Leo Chat] two|[Leo Chat] three" {
		t.Fatalf("order = %q", got)
	}
}

func TestPostFromTaskGoesToOwnerAsTaskCard(t *testing.T) {
	f := newFixture(t)
	f.cfg.Tasks["build"] = config.TaskConfig{Workspace: "/src/web"}
	m, err := f.svc.Post(context.Background(), PostRequest{Caller: "task:build", Task: "build", RunID: "r9", Text: "green"})
	if err != nil {
		t.Fatal(err)
	}
	if m.Agent != "web" || m.Kind != KindTask || m.Task != "build" || m.RunID != "r9" || m.Direction != DirectionOut {
		t.Fatalf("card = %+v", m)
	}
}

// A persistent task runs in its agent, whose MCP server knows no task: the
// daemon files the agent's chat output under the run in flight.
func TestChatToolsDuringAPersistentRunCarryItsTaskAndRun(t *testing.T) {
	f := newFixture(t)
	f.cfg.Tasks["digest"] = config.TaskConfig{Runtime: "persistent", Template: "assistant"}
	f.runs["assistant"] = [2]string{"digest", "run-7"}
	m, err := f.svc.Post(context.Background(), PostRequest{Caller: "assistant", Text: "today's digest"})
	if err != nil {
		t.Fatal(err)
	}
	if m.Agent != "assistant" || m.Kind != KindTask || m.Task != "digest" || m.RunID != "run-7" {
		t.Fatalf("card = %+v", m)
	}
	go func() {
		<-f.timers.got
		open, _ := f.store.OpenMessages("assistant", KindAsk, StateOpen)
		_, _ = f.svc.Answer("assistant", open[0].ID, Answer{Values: []string{"y"}})
	}()
	res, err := f.svc.Ask(context.Background(), AskRequest{Caller: "assistant", Question: "Publish?", Options: []Option{{Value: "y"}}, Wait: 30 * time.Minute})
	if err != nil || len(res.Values) != 1 || res.Values[0] != "y" {
		t.Fatalf("ask = %+v, %v", res, err)
	}
	asks, _, _ := f.store.Messages("assistant", Page{Task: "digest"})
	if len(asks) != 2 || asks[0].Kind != KindAsk || asks[0].RunID != "run-7" {
		t.Fatalf("task thread = %+v", asks)
	}
}

func TestPostFromTaskWithMissingOwnerNotesIt(t *testing.T) {
	f := newFixture(t)
	f.cfg.Tasks["build"] = config.TaskConfig{Owner: "gone"}
	m, err := f.svc.Post(context.Background(), PostRequest{Caller: "task:build", Task: "build", RunID: "r1", Text: "done"})
	if err != nil {
		t.Fatal(err)
	}
	thread := f.thread(t, "assistant")
	if m.Agent != "assistant" || len(thread) != 2 || thread[1].Kind != KindSystem || !strings.Contains(thread[1].Text, "gone") {
		t.Fatalf("thread = %+v", thread)
	}
}

func TestPostCopiesFiles(t *testing.T) {
	f := newFixture(t)
	_, _ = f.svc.Send(context.Background(), "assistant", SendRequest{Text: "make a chart"})
	f.svc.Flush()
	src := filepath.Join(t.TempDir(), "chart.png")
	if err := os.WriteFile(src, []byte("PNG"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := f.svc.Post(context.Background(), PostRequest{Caller: "assistant", Text: "here", Files: []string{src}})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Attachments) != 1 || m.Attachments[0].Filename != "chart.png" || m.Attachments[0].MIME != "image/png" {
		t.Fatalf("attachments = %+v", m.Attachments)
	}
}

// A post's id is taken when it is stored, not before its files are copied:
// a message stored meanwhile must not end up above it, behind a read
// watermark or a paging cursor a client already took.
func TestPostIsOrderedByWhenItIsStored(t *testing.T) {
	f := newFixture(t)
	src := filepath.Join(t.TempDir(), "big.bin")
	if err := os.WriteFile(src, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	blocked, release := f.ids.arm()
	type result struct {
		m   Message
		err error
	}
	done := make(chan result, 1)
	go func() {
		m, err := f.svc.Post(context.Background(), PostRequest{Caller: "assistant", Text: "with file", Files: []string{src}})
		done <- result{m, err}
	}()
	<-blocked
	other, err := f.svc.Post(context.Background(), PostRequest{Caller: "assistant", Text: "meanwhile"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.MarkRead("assistant", other.ID); err != nil {
		t.Fatal(err)
	}
	close(release)
	r := <-done
	if r.err != nil {
		t.Fatal(r.err)
	}
	if r.m.ID <= other.ID {
		t.Fatalf("post stored last has id %s, below %s stored before it", r.m.ID, other.ID)
	}
	threads, err := f.svc.Threads()
	if err != nil || len(threads) != 1 || threads[0].Unread != 1 {
		t.Fatalf("threads = %+v, %v; the later post must count as unread", threads, err)
	}
}

func TestPostRefusesDispatchAndBadCaller(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.Post(context.Background(), PostRequest{Caller: "dispatch:d-1", Text: "x"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v", err)
	}
	if _, err := f.svc.Post(context.Background(), PostRequest{Caller: "assistant"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty post err = %v", err)
	}
}

func TestAskReturnsTheChosenOption(t *testing.T) {
	f := newFixture(t)
	type result struct {
		r   AskResult
		err error
	}
	done := make(chan result, 1)
	go func() {
		r, err := f.svc.Ask(context.Background(), AskRequest{Caller: "assistant", Question: "Ship it?", Options: []Option{{Value: "y", Label: "Yes"}, {Value: "n", Label: "No"}}, Timeout: 5 * time.Minute})
		done <- result{r, err}
	}()
	if d := <-f.timers.got; d != 5*time.Minute {
		t.Fatalf("ask timer = %s", d)
	}
	open := f.thread(t, "assistant")[0]
	if open.Kind != KindAsk || open.State != StateOpen {
		t.Fatalf("ask message = %+v", open)
	}
	if _, err := f.svc.Answer("assistant", open.ID, Answer{Values: []string{"maybe"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a value outside the options must be refused: %v", err)
	}
	if _, err := f.svc.Answer("assistant", open.ID, Answer{Values: []string{"y"}}); err != nil {
		t.Fatal(err)
	}
	got := <-done
	if got.err != nil || len(got.r.Values) != 1 || got.r.Values[0] != "y" || got.r.Labels[0] != "Yes" || got.r.TimedOut {
		t.Fatalf("ask result = %+v", got)
	}
	if _, err := f.svc.Answer("assistant", open.ID, Answer{Values: []string{"n"}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("second answer err = %v", err)
	}
}

func TestAskTimesOutAndExpires(t *testing.T) {
	f := newFixture(t)
	done := make(chan AskResult, 1)
	go func() {
		r, _ := f.svc.Ask(context.Background(), AskRequest{Caller: "assistant", Question: "?", Options: []Option{{Value: "a", Label: "a"}}, Timeout: 5 * time.Minute})
		done <- r
	}()
	if d := <-f.timers.got; d != 5*time.Minute {
		t.Fatalf("timeout = %s", d)
	}
	f.timers.fireLast()
	if r := <-done; !r.TimedOut {
		t.Fatalf("result = %+v", r)
	}
	if m := f.thread(t, "assistant")[0]; m.State != StateExpired {
		t.Fatalf("state = %s", m.State)
	}
}

func TestAskValidation(t *testing.T) {
	f := newFixture(t)
	many := make([]Option, MaxAskOptions+1)
	for i := range many {
		many[i] = Option{Value: string(rune('a' + i)), Label: "x"}
	}
	for name, req := range map[string]AskRequest{
		"no options":       {Caller: "assistant", Question: "?"},
		"too many":         {Caller: "assistant", Question: "?", Options: many},
		"no question":      {Caller: "assistant", Options: []Option{{Value: "a"}}},
		"long timeout":     {Caller: "assistant", Question: "?", Options: []Option{{Value: "a"}}, Timeout: 2 * time.Hour},
		"negative timeout": {Caller: "assistant", Question: "?", Options: []Option{{Value: "a"}}, Timeout: -time.Second},
		"dup values":       {Caller: "assistant", Question: "?", Options: []Option{{Value: "a"}, {Value: "a"}}},
	} {
		if _, err := f.svc.Ask(context.Background(), req); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func permissionPayload(tool, command string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"hook_event_name": "PermissionRequest", "tool_name": tool, "tool_input": map[string]any{"command": command}})
	return b
}

func TestPermissionAnsweredFromChat(t *testing.T) {
	f := newFixture(t)
	done := make(chan string, 1)
	go func() {
		done <- f.svc.RequestPermission(context.Background(), "assistant", permissionPayload("Bash", "rm -rf build"), 2*time.Minute)
	}()
	<-f.timers.got
	m := f.thread(t, "assistant")[0]
	if m.Kind != KindPermission || m.Permission == nil || m.Permission.Tool != "Bash" || m.Permission.Summary != "rm -rf build" {
		t.Fatalf("permission message = %+v", m)
	}
	if m.Input == nil || m.Input.Type != InputButtons || len(m.Input.Options) != 2 || m.Input.Options[0].Value != "allow" || m.Input.Options[1].Value != "deny" {
		t.Fatalf("permission input = %+v, want allow and deny buttons", m.Input)
	}
	if _, err := f.svc.Answer("assistant", m.ID, Answer{Values: []string{"allow"}}); err != nil {
		t.Fatal(err)
	}
	if got := <-done; got != "allow" {
		t.Fatalf("decision = %q", got)
	}
	f.svc.Flush()
	if got := f.deliver.delivered(); len(got) != 0 {
		t.Fatalf("a permission answer was delivered as a chat message: %q", got)
	}
}

func TestPermissionTimeoutLeavesTerminalPrompt(t *testing.T) {
	f := newFixture(t)
	done := make(chan string, 1)
	go func() {
		done <- f.svc.RequestPermission(context.Background(), "assistant", permissionPayload("Bash", "ls"), 2*time.Minute)
	}()
	<-f.timers.got
	f.timers.fireLast()
	if got := <-done; got != "" {
		t.Fatalf("timeout decision = %q, want none", got)
	}
	if m := f.thread(t, "assistant")[0]; m.State != StateExpired {
		t.Fatalf("state = %s", m.State)
	}
}

func TestPermissionAnsweredInTerminalViaPostToolUse(t *testing.T) {
	f := newFixture(t)
	done := make(chan string, 1)
	go func() {
		done <- f.svc.RequestPermission(context.Background(), "assistant", permissionPayload("Bash", "make"), 2*time.Minute)
	}()
	<-f.timers.got
	// A different tool finishing must not settle it.
	f.svc.OnHook("assistant", HookEvent{Event: "PostToolUse", ToolName: "Read", ToolInput: json.RawMessage(`{"file_path":"/x"}`)})
	if m := f.thread(t, "assistant")[0]; m.State != StateOpen {
		t.Fatalf("unrelated PostToolUse settled the prompt: %s", m.State)
	}
	f.svc.OnHook("assistant", HookEvent{Event: "PostToolUse", ToolName: "Bash", ToolInput: json.RawMessage(`{ "command": "make" }`)})
	if got := <-done; got != "" {
		t.Fatalf("terminal-answered decision = %q, want none", got)
	}
	m := f.thread(t, "assistant")[0]
	if m.State != StateAnswered || (m.Answer == nil || !m.Answer.isTerminal()) {
		t.Fatalf("message = %s/%s", m.State, m.Answer)
	}
}

func TestPermissionHookGoneMarksAnsweredInTerminal(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan string, 1)
	go func() { done <- f.svc.RequestPermission(ctx, "assistant", permissionPayload("Bash", "x"), time.Minute) }()
	<-f.timers.got
	cancel()
	<-done
	m := f.thread(t, "assistant")[0]
	if m.State != StateAnswered || (m.Answer == nil || !m.Answer.isTerminal()) {
		t.Fatalf("message = %s/%s", m.State, m.Answer)
	}
}

func TestStopHookPostsCapturedReplyOnce(t *testing.T) {
	f := newFixture(t)
	in, _ := f.svc.Send(context.Background(), "assistant", SendRequest{Text: "status?"})
	f.svc.Flush()
	path := transcript{}.user("[Leo Chat] status?").assistant("All green.").write(t)
	f.svc.OnHook("assistant", HookEvent{Event: "Stop", TranscriptPath: path, LastAssistantMessage: "All green."})
	f.svc.OnHook("assistant", HookEvent{Event: "Stop", TranscriptPath: path, LastAssistantMessage: "All green."})
	thread := f.thread(t, "assistant")
	if len(thread) != 2 || thread[0].Direction != DirectionOut || thread[0].Text != "All green." || thread[0].ReplyTo != in.ID {
		t.Fatalf("thread = %+v", thread)
	}
	if caps, _ := f.store.Captures("assistant"); len(caps) != 0 {
		t.Fatalf("capture not cleared: %+v", caps)
	}
}

func TestStopHookAfterToolPostPostsNothing(t *testing.T) {
	f := newFixture(t)
	_, _ = f.svc.Send(context.Background(), "assistant", SendRequest{Text: "status?"})
	f.svc.Flush()
	if _, err := f.svc.Post(context.Background(), PostRequest{Caller: "assistant", Text: "posted myself"}); err != nil {
		t.Fatal(err)
	}
	path := transcript{}.user("[Leo Chat] status?").chatPost().toolResult().assistant("Posted it.").write(t)
	f.svc.OnHook("assistant", HookEvent{Event: "Stop", TranscriptPath: path, LastAssistantMessage: "Posted it."})
	thread := f.thread(t, "assistant")
	if len(thread) != 2 || thread[0].Text != "posted myself" {
		t.Fatalf("thread = %+v", texts(thread))
	}
}

// Two chat prompts run in one turn, which answers with leo_chat_post: the
// turn's final text must not be posted as well.
func TestStopHookAfterToolPostInATwoPromptTurnPostsNothing(t *testing.T) {
	f := newFixture(t)
	_, _ = f.svc.Send(context.Background(), "assistant", SendRequest{Text: "first"})
	_, _ = f.svc.Send(context.Background(), "assistant", SendRequest{Text: "second"})
	f.svc.Flush()
	if _, err := f.svc.Post(context.Background(), PostRequest{Caller: "assistant", Text: "posted myself"}); err != nil {
		t.Fatal(err)
	}
	path := transcript{}.user("[Leo Chat] first").chatPost().toolResult().user("[Leo Chat] second").assistant("Posted it.").write(t)
	f.svc.OnHook("assistant", HookEvent{Event: "Stop", TranscriptPath: path, LastAssistantMessage: "Posted it."})
	thread := f.thread(t, "assistant")
	if len(thread) != 3 || thread[0].Text != "posted myself" {
		t.Fatalf("thread = %q", texts(thread))
	}
}

func TestStartupExpiresOpenPermissionPrompts(t *testing.T) {
	f := newFixture(t)
	ids := NewULIDSource(f.clock.Now, nil)
	perm := Message{ID: ids.New(), Agent: "assistant", Direction: DirectionOut, Kind: KindPermission, State: StateOpen, TS: f.clock.Now()}
	ask := Message{ID: ids.New(), Agent: "assistant", Direction: DirectionOut, Kind: KindAsk, State: StateOpen, Asker: "assistant", TS: f.clock.Now()}
	for _, m := range []Message{perm, ask} {
		if err := f.store.InsertMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.svc.Recover(); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.store.Message(perm.ID); got.State != StateExpired {
		t.Fatalf("permission prompt = %s, want expired: its hook died with the daemon", got.State)
	}
	if got, _ := f.store.Message(ask.ID); got.State != StateOpen {
		t.Fatalf("deadline-free ask = %s, want open", got.State)
	}
}

func TestStartupFailsMessagesTheLastDaemonNeverDelivered(t *testing.T) {
	f := newFixture(t)
	m := Message{ID: NewULIDSource(f.clock.Now, nil).New(), Agent: "assistant", Direction: DirectionIn, Kind: KindText, Text: "hi", State: StatePending, TS: f.clock.Now()}
	if err := f.store.InsertMessage(m); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Recover(); err != nil {
		t.Fatal(err)
	}
	got, _ := f.store.Message(m.ID)
	notes, _ := f.store.OpenMessages("assistant", KindSystem, "")
	if got.State != StateFailed || len(notes) != 1 || !strings.Contains(notes[0].Text, "restarted") {
		t.Fatalf("message %s, notes %+v", got.State, notes)
	}
}

// The delivery ref (the bridge command id) is stored on the message before
// the deliverer gets it, so a restart mid-delivery can find it.
func TestDeliveryStoresItsRefFirst(t *testing.T) {
	f := newFixture(t)
	var stored string
	f.deliver.onDeliver = func() {
		ms, _, _ := f.store.Messages("assistant", Page{})
		stored = ms[0].DeliveryRef
	}
	m, err := f.svc.Send(context.Background(), "assistant", SendRequest{Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	f.svc.Flush()
	if want := "chat-" + m.ID; stored != want || len(f.deliver.refs) != 1 || f.deliver.refs[0] != want {
		t.Fatalf("stored ref %q, delivered refs %q, want %q", stored, f.deliver.refs, want)
	}
}

// A message the last daemon left pending whose deliver its agent's outbox
// still holds is delivered by the outbox after restore: it is not failed,
// so the user is not told to send it again.
func TestStartupKeepsMessagesTheOutboxStillHolds(t *testing.T) {
	f := newFixture(t)
	ids := NewULIDSource(f.clock.Now, nil)
	queued := Message{ID: ids.New(), Agent: "assistant", Direction: DirectionIn, Kind: KindText, Text: "queued", State: StatePending, DeliveryRef: "chat-a", TS: f.clock.Now()}
	lost := Message{ID: ids.New(), Agent: "assistant", Direction: DirectionIn, Kind: KindText, Text: "lost", State: StatePending, DeliveryRef: "chat-b", TS: f.clock.Now()}
	for _, m := range []Message{queued, lost} {
		if err := f.store.InsertMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	f.held["assistant/chat-a"] = true
	if err := f.svc.Recover(); err != nil {
		t.Fatal(err)
	}
	gotQueued, _ := f.store.Message(queued.ID)
	gotLost, _ := f.store.Message(lost.ID)
	notes, _ := f.store.OpenMessages("assistant", KindSystem, "")
	if gotQueued.State != StateDelivered || gotLost.State != StateFailed || len(notes) != 1 {
		t.Fatalf("queued %s, lost %s, notes %+v", gotQueued.State, gotLost.State, notes)
	}
}

func TestPruneDeletesOldAttachmentFiles(t *testing.T) {
	f := newFixture(t)
	a := upload(t, f, "assistant", "old.txt", "text/plain", "x")
	f.clock.Advance(AttachmentRetention + time.Hour)
	if err := f.svc.Prune(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.root, "assistant", a.ID)); !os.IsNotExist(err) {
		t.Fatalf("file not pruned: %v", err)
	}
	got, _ := f.store.Attachment(a.ID)
	if got.Available {
		t.Fatal("pruned attachment still available")
	}
}

// blockingDeliverer holds each delivery until its context ends.
type blockingDeliverer struct{ started chan struct{} }

func (b blockingDeliverer) Deliver(ctx context.Context, _, _, _ string, _ func()) (string, error) {
	close(b.started)
	<-ctx.Done()
	return "", ctx.Err()
}

// Close stops admission, then cancels and drains the waits and deliveries
// in flight, so none outlives the store the daemon closes next.
func TestCloseCancelsAndDrainsWaitersAndDeliveries(t *testing.T) {
	f := newFixture(t)
	started := make(chan struct{})
	f.svc.opts.Deliver = blockingDeliverer{started: started}
	in, err := f.svc.Send(context.Background(), "assistant", SendRequest{Text: "slow"})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	asked := make(chan AskResult, 1)
	go func() {
		r, _ := f.svc.Ask(context.Background(), AskRequest{Caller: "assistant", Question: "?", Options: []Option{{Value: "a"}}, Wait: 30 * time.Minute})
		asked <- r
	}()
	<-f.timers.got
	f.svc.Close()
	// Close waits for Ask to return, but the caller goroutine's send of the
	// result may still be a step behind: block on it, under a guard.
	select {
	case r := <-asked:
		if !r.Pending || r.TimedOut {
			t.Fatalf("ask after close = %+v, want pending", r)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Close returned while an ask still waited")
	}
	// Shutdown leaves the ask open: the next daemon still answers it.
	if m := f.thread(t, "assistant")[0]; m.Kind != KindAsk || m.State != StateOpen {
		t.Fatalf("ask after close = %s/%s, want open", m.Kind, m.State)
	}
	got, _ := f.store.Message(in.ID)
	notes, _ := f.store.OpenMessages("assistant", KindSystem, "")
	if got.State != StatePending || len(notes) != 0 {
		t.Fatalf("a delivery cut short by shutdown stays pending for recovery: %s, notes %+v", got.State, notes)
	}
	if _, err := f.svc.Send(context.Background(), "assistant", SendRequest{Text: "late"}); !errors.Is(err, ErrClosed) {
		t.Fatalf("send after close = %v", err)
	}
	if _, err := f.svc.Post(context.Background(), PostRequest{Caller: "assistant", Text: "late"}); !errors.Is(err, ErrClosed) {
		t.Fatalf("post after close = %v", err)
	}
}

// An ask's deadline is its own, not one call's: a call that waits out its
// window returns pending with the message id, and a later call resumes it.
func TestAskOutlastsOneCallAndResumes(t *testing.T) {
	f := newFixture(t)
	req := AskRequest{Caller: "assistant", Question: "Ship it?", Options: []Option{{Value: "y", Label: "Yes"}}, Timeout: time.Hour, Wait: 30 * time.Minute}
	done := make(chan AskResult, 1)
	go func() {
		r, _ := f.svc.Ask(context.Background(), req)
		done <- r
	}()
	if d := <-f.timers.got; d != time.Hour {
		t.Fatalf("deadline = %s", d)
	}
	if d := <-f.timers.got; d != 30*time.Minute {
		t.Fatalf("call window = %s", d)
	}
	f.timers.fireLast()
	first := <-done
	open := f.thread(t, "assistant")[0]
	if !first.Pending || first.TimedOut || first.MessageID != open.ID || open.State != StateOpen {
		t.Fatalf("first call = %+v, message %s", first, open.State)
	}
	if _, err := f.svc.Answer("assistant", open.ID, Answer{Values: []string{"y"}}); err != nil {
		t.Fatal(err)
	}
	r, err := f.svc.Ask(context.Background(), AskRequest{Caller: "assistant", MessageID: open.ID, Wait: 30 * time.Minute})
	if err != nil || len(r.Values) != 1 || r.Values[0] != "y" || r.Labels[0] != "Yes" || r.Pending {
		t.Fatalf("resumed = %+v, %v", r, err)
	}
}

// A caller that goes away (its harness gave up on the tool call) leaves the
// ask open until its own deadline; resuming after that reports the timeout.
func TestAskDeadlineDoesNotEndWithTheCall(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan AskResult, 1)
	go func() {
		r, _ := f.svc.Ask(ctx, AskRequest{Caller: "assistant", Question: "?", Options: []Option{{Value: "a"}}, Timeout: time.Hour})
		done <- r
	}()
	<-f.timers.got
	cancel()
	if r := <-done; !r.Pending {
		t.Fatalf("abandoned call = %+v", r)
	}
	open := f.thread(t, "assistant")[0]
	if open.State != StateOpen {
		t.Fatalf("the ask must outlive its call: %s", open.State)
	}
	f.timers.fireLast()
	resumed := make(chan AskResult, 1)
	go func() {
		r, _ := f.svc.Ask(context.Background(), AskRequest{Caller: "assistant", MessageID: open.ID})
		resumed <- r
	}()
	if r := <-resumed; !r.TimedOut {
		t.Fatalf("resumed after the deadline = %+v", r)
	}
	if m, _ := f.store.Message(open.ID); m.State != StateExpired {
		t.Fatalf("state = %s", m.State)
	}
}

func TestAskResumeChecksTheMessage(t *testing.T) {
	f := newFixture(t)
	note, err := f.svc.Post(context.Background(), PostRequest{Caller: "assistant", Text: "not an ask"})
	if err != nil {
		t.Fatal(err)
	}
	for name, req := range map[string]AskRequest{
		"not an ask":       {Caller: "assistant", MessageID: note.ID},
		"another's ask":    {Caller: "web", MessageID: note.ID},
		"malformed id":     {Caller: "assistant", MessageID: "nope"},
		"question as well": {Caller: "assistant", MessageID: note.ID, Question: "?", Options: []Option{{Value: "a"}}},
	} {
		if _, err := f.svc.Ask(context.Background(), req); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

// An ask with no timeout has no deadline: only its call's window ends, and
// a resume days later still finds it open.
func TestAskWithoutTimeoutNeverExpires(t *testing.T) {
	f := newFixture(t)
	done := make(chan AskResult, 1)
	go func() {
		r, _ := f.svc.Ask(context.Background(), AskRequest{Caller: "assistant", Question: "?", Options: []Option{{Value: "a"}}, Wait: 30 * time.Minute})
		done <- r
	}()
	if d := <-f.timers.got; d != 30*time.Minute {
		t.Fatalf("first timer = %s, want only the call window", d)
	}
	select {
	case d := <-f.timers.got:
		t.Fatalf("a deadline timer of %s was armed for a deadline-free ask", d)
	default:
	}
	f.timers.fireLast()
	if r := <-done; !r.Pending || r.TimedOut {
		t.Fatalf("first call = %+v", r)
	}
	open := f.thread(t, "assistant")[0]
	if open.State != StateOpen || !open.Deadline.IsZero() {
		t.Fatalf("ask = %s, deadline %v", open.State, open.Deadline)
	}
	f.clock.Advance(72 * time.Hour)
	resumed := make(chan AskResult, 1)
	go func() {
		r, _ := f.svc.Ask(context.Background(), AskRequest{Caller: "assistant", MessageID: open.ID, Wait: 30 * time.Minute})
		resumed <- r
	}()
	<-f.timers.got
	f.timers.fireLast()
	if r := <-resumed; !r.Pending || r.TimedOut {
		t.Fatalf("resumed = %+v, want pending", r)
	}
}

func TestAskWithTimeoutPersistsItsDeadline(t *testing.T) {
	f := newFixture(t)
	done := make(chan AskResult, 1)
	go func() {
		r, _ := f.svc.Ask(context.Background(), AskRequest{Caller: "assistant", Question: "?", Options: []Option{{Value: "a"}}, Timeout: 10 * time.Minute})
		done <- r
	}()
	<-f.timers.got
	open := f.thread(t, "assistant")[0]
	if want := open.TS.Add(10 * time.Minute); !open.Deadline.Equal(want) {
		t.Fatalf("deadline = %v, want %v", open.Deadline, want)
	}
	f.timers.fireLast()
	r := <-done
	if !r.TimedOut || r.ClosedReason != ClosedTimeout {
		t.Fatalf("result = %+v", r)
	}
	if m, _ := f.store.Message(open.ID); m.State != StateExpired || m.ClosedReason != ClosedTimeout {
		t.Fatalf("message = %s/%q", m.State, m.ClosedReason)
	}
}

// A task run's ask has no agent to hand a late answer to, so it keeps the
// default deadline rather than staying open forever.
func TestAskFromATaskRunKeepsTheDefaultDeadline(t *testing.T) {
	f := newFixture(t)
	f.cfg.Tasks["build"] = config.TaskConfig{Workspace: "/src/web"}
	done := make(chan AskResult, 1)
	go func() {
		r, _ := f.svc.Ask(context.Background(), AskRequest{Caller: "task:build", Task: "build", RunID: "r1", Question: "?", Options: []Option{{Value: "a"}}})
		done <- r
	}()
	if d := <-f.timers.got; d != DefaultAskTimeout {
		t.Fatalf("task-run ask deadline = %s, want %s", d, DefaultAskTimeout)
	}
	f.timers.fireLast()
	if r := <-done; !r.TimedOut || r.ClosedReason != ClosedTimeout {
		t.Fatalf("result = %+v", r)
	}
	if m := f.thread(t, "web")[0]; m.State != StateExpired || m.Asker != "task:build" {
		t.Fatalf("message = %s asker %q", m.State, m.Asker)
	}
}

// pendingAsk posts a deadline-free ask, lets its call window end, and
// returns the open ask: the state a late answer arrives in.
func pendingAsk(t *testing.T, f *harnessFixture, caller string) Message {
	t.Helper()
	done := make(chan AskResult, 1)
	go func() {
		r, _ := f.svc.Ask(context.Background(), AskRequest{Caller: caller, Question: "Ship it?", Options: []Option{{Value: "y", Label: "Yes"}, {Value: "n", Label: "No"}}, Wait: 30 * time.Minute})
		done <- r
	}()
	<-f.timers.got
	f.timers.fireLast()
	if r := <-done; !r.Pending {
		t.Fatalf("call = %+v, want pending", r)
	}
	agent := caller
	if caller == "task:build" {
		agent = "web"
	}
	open, _ := f.store.OpenMessages(agent, KindAsk, StateOpen)
	if len(open) != 1 {
		t.Fatalf("open asks = %d", len(open))
	}
	return open[0]
}

func lateAnswerPrompt(ask Message) string {
	return "[Leo Chat] Yes\nIn reply to your leo_chat_ask " + ask.ID + " (" + ask.TS.UTC().Format(time.RFC3339) + "): Ship it?\n" +
		`Answer: {"message_id":"` + ask.ID + `","values":["y"],"labels":["Yes"],"text":""}`
}

func TestLateAnswerIsDeliveredOnce(t *testing.T) {
	f := newFixture(t)
	ask := pendingAsk(t, f, "assistant")
	if _, err := f.svc.Answer("assistant", ask.ID, Answer{Values: []string{"y"}}); err != nil {
		t.Fatal(err)
	}
	f.svc.Flush()
	if got := f.deliver.delivered(); len(got) != 1 || got[0] != lateAnswerPrompt(ask) {
		t.Fatalf("delivered %q\nwant %q", got, lateAnswerPrompt(ask))
	}
	var replies []Message
	for _, m := range f.thread(t, "assistant") {
		if m.Direction == DirectionIn {
			replies = append(replies, m)
		}
	}
	if len(replies) != 1 || replies[0].Kind != KindText || replies[0].ReplyTo != ask.ID || replies[0].State != StateDelivered || replies[0].Text != "Yes" {
		t.Fatalf("inbound replies = %+v", replies)
	}
	r, err := f.svc.Ask(context.Background(), AskRequest{Caller: "assistant", MessageID: ask.ID})
	if err != nil || len(r.Values) != 1 || r.Values[0] != "y" {
		t.Fatalf("resume = %+v, %v", r, err)
	}
	f.svc.Flush()
	if got := f.deliver.delivered(); len(got) != 1 {
		t.Fatalf("a resume must not deliver again: %q", got)
	}
}

func TestAnswerWhileACallWaitsIsNotDelivered(t *testing.T) {
	f := newFixture(t)
	done := make(chan AskResult, 1)
	go func() {
		r, _ := f.svc.Ask(context.Background(), AskRequest{Caller: "assistant", Question: "?", Options: []Option{{Value: "y", Label: "Yes"}}, Wait: 30 * time.Minute})
		done <- r
	}()
	<-f.timers.got
	open := f.thread(t, "assistant")[0]
	if _, err := f.svc.Answer("assistant", open.ID, Answer{Values: []string{"y"}}); err != nil {
		t.Fatal(err)
	}
	if r := <-done; len(r.Values) != 1 || r.Values[0] != "y" {
		t.Fatalf("call = %+v", r)
	}
	f.svc.Flush()
	if got := f.deliver.delivered(); len(got) != 0 {
		t.Fatalf("the blocked call took the answer, yet it was delivered: %q", got)
	}
	for _, m := range f.thread(t, "assistant") {
		if m.Direction == DirectionIn {
			t.Fatalf("inbound message %+v", m)
		}
	}
}

// The caller leaves, then the answer lands: whichever of the call's exit and
// the answer is noticed first, the answer is delivered exactly once.
func TestAnswerAfterTheCallerLeftIsDeliveredOnce(t *testing.T) {
	f := newFixture(t)
	for i := 1; i <= 25; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan AskResult, 1)
		go func() {
			r, _ := f.svc.Ask(ctx, AskRequest{Caller: "assistant", Question: "Ship it?", Options: []Option{{Value: "y", Label: "Yes"}}, Wait: 30 * time.Minute})
			done <- r
		}()
		<-f.timers.got
		open, _ := f.store.OpenMessages("assistant", KindAsk, StateOpen)
		cancel()
		if _, err := f.svc.Answer("assistant", open[0].ID, Answer{Values: []string{"y"}}); err != nil {
			t.Fatal(err)
		}
		if r := <-done; !r.Pending {
			t.Fatalf("iteration %d: the departed call = %+v, want pending", i, r)
		}
		f.svc.Flush()
		if got := f.deliver.delivered(); len(got) != i {
			t.Fatalf("iteration %d: %d deliveries in all, want %d", i, len(got), i)
		}
	}
}

func TestLateAnswerWakesASuspendedAgent(t *testing.T) {
	f := newFixture(t)
	ask := pendingAsk(t, f, "assistant")
	f.deliver.stopped["assistant"] = true
	if _, err := f.svc.Answer("assistant", ask.ID, Answer{Values: []string{"y"}}); err != nil {
		t.Fatal(err)
	}
	f.svc.Flush()
	var starting bool
	for _, m := range f.thread(t, "assistant") {
		starting = starting || (m.Kind == KindSystem && strings.Contains(m.Text, "Starting assistant"))
	}
	if !starting {
		t.Fatalf("no starting note: %q", texts(f.thread(t, "assistant")))
	}
	if got := f.deliver.delivered(); len(got) != 1 {
		t.Fatalf("delivered %q", got)
	}
}

// A task run's ask has no agent to hand the answer to; the stored answer is
// still there for a resume.
func TestLateAnswerToATaskRunAskIsNotDelivered(t *testing.T) {
	f := newFixture(t)
	f.cfg.Tasks["build"] = config.TaskConfig{Workspace: "/src/web"}
	done := make(chan AskResult, 1)
	go func() {
		r, _ := f.svc.Ask(context.Background(), AskRequest{Caller: "task:build", Task: "build", RunID: "r1", Question: "Ship it?", Options: []Option{{Value: "y", Label: "Yes"}}, Timeout: time.Hour, Wait: 30 * time.Minute})
		done <- r
	}()
	<-f.timers.got
	<-f.timers.got
	f.timers.fireLast()
	if r := <-done; !r.Pending {
		t.Fatalf("call = %+v", r)
	}
	open := f.thread(t, "web")[0]
	if _, err := f.svc.Answer("web", open.ID, Answer{Values: []string{"y"}}); err != nil {
		t.Fatal(err)
	}
	f.svc.Flush()
	if got := f.deliver.delivered(); len(got) != 0 {
		t.Fatalf("delivered to a task run: %q", got)
	}
	r, err := f.svc.Ask(context.Background(), AskRequest{Caller: "task:build", Task: "build", MessageID: open.ID})
	if err != nil || len(r.Values) != 1 || r.Values[0] != "y" {
		t.Fatalf("resume = %+v, %v", r, err)
	}
}

// abandonedAsk posts an ask whose call goes away at once, and returns it
// open: what a daemon restart finds in the store.
func abandonedAsk(t *testing.T, f *harnessFixture, timeout time.Duration) Message {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan AskResult, 1)
	go func() {
		r, _ := f.svc.Ask(ctx, AskRequest{Caller: "assistant", Question: "Ship it?", Options: []Option{{Value: "y", Label: "Yes"}}, Timeout: timeout, Wait: 30 * time.Minute})
		done <- r
	}()
	if timeout > 0 {
		<-f.timers.got
	}
	<-f.timers.got
	cancel()
	if r := <-done; !r.Pending {
		t.Fatalf("abandoned call = %+v", r)
	}
	open, _ := f.store.OpenMessages("assistant", KindAsk, StateOpen)
	if len(open) != 1 {
		t.Fatalf("open asks = %d", len(open))
	}
	return open[0]
}

// updateOf returns the last chat.updated payload published for message id.
func updateOf(f *harnessFixture, id string) (UpdatedPayload, bool) {
	f.pub.mu.Lock()
	defer f.pub.mu.Unlock()
	var out UpdatedPayload
	var found bool
	for _, ev := range f.pub.events {
		if p, ok := ev.Payload.(*UpdatedPayload); ok && ev.Type == EventUpdated && p.Message.ID == id {
			out, found = *p, true
		}
	}
	return out, found
}

func TestRestartKeepsADeadlineFreeAskOpen(t *testing.T) {
	f := newFixture(t)
	ask := abandonedAsk(t, f, 0)
	f.restart(t)
	f.recover(t)
	if got, _ := f.store.Message(ask.ID); got.State != StateOpen {
		t.Fatalf("ask after restart = %s", got.State)
	}
	if _, err := f.svc.Answer("assistant", ask.ID, Answer{Values: []string{"y"}}); err != nil {
		t.Fatal(err)
	}
	f.svc.Flush()
	if got := f.deliver.delivered(); len(got) != 1 || got[0] != lateAnswerPrompt(ask) {
		t.Fatalf("delivered %q", got)
	}
}

func TestRestartExpiresAnAskPastItsDeadline(t *testing.T) {
	f := newFixture(t)
	ask := abandonedAsk(t, f, 10*time.Minute)
	f.clock.Advance(11 * time.Minute)
	f.restart(t)
	f.recover(t)
	got, _ := f.store.Message(ask.ID)
	if got.State != StateExpired || got.ClosedReason != ClosedTimeout {
		t.Fatalf("ask = %s/%q", got.State, got.ClosedReason)
	}
	if u, ok := updateOf(f, ask.ID); !ok || u.Message.ClosedReason != ClosedTimeout {
		t.Fatalf("update event = %+v, %v", u, ok)
	}
}

func TestRestartRearmsTheRemainingDeadline(t *testing.T) {
	f := newFixture(t)
	ask := abandonedAsk(t, f, 10*time.Minute)
	f.clock.Advance(4 * time.Minute)
	f.restart(t)
	f.recover(t)
	if d := <-f.timers.got; d != 6*time.Minute {
		t.Fatalf("re-armed for %s, want 6m", d)
	}
	f.timers.fireLast()
	u := f.pub.waitUpdate(t)
	if u.Message.ID != ask.ID || u.Message.State != StateExpired || u.Message.ClosedReason != ClosedTimeout {
		t.Fatalf("update = %+v", u)
	}
}

// After a restart there is no call to resume into: the store decides. An
// open ask is pending, never timed out.
func TestResumeAfterRestartReturnsTheStoredAnswerOrPending(t *testing.T) {
	f := newFixture(t)
	answered := abandonedAsk(t, f, 0)
	if _, err := f.svc.Answer("assistant", answered.ID, Answer{Values: []string{"y"}}); err != nil {
		t.Fatal(err)
	}
	open := abandonedAsk(t, f, 0)
	f.restart(t)
	f.recover(t)
	r, err := f.svc.Ask(context.Background(), AskRequest{Caller: "assistant", MessageID: answered.ID})
	if err != nil || len(r.Values) != 1 || r.Values[0] != "y" || r.Pending || r.TimedOut {
		t.Fatalf("answered resume = %+v, %v", r, err)
	}
	done := make(chan AskResult, 1)
	go func() {
		r, _ := f.svc.Ask(context.Background(), AskRequest{Caller: "assistant", MessageID: open.ID, Wait: 30 * time.Minute})
		done <- r
	}()
	<-f.timers.got
	f.timers.fireLast()
	if r := <-done; !r.Pending || r.TimedOut {
		t.Fatalf("open resume = %+v, want pending", r)
	}
}

func TestWithdrawClosesTheCallersAsk(t *testing.T) {
	f := newFixture(t)
	done := make(chan AskResult, 1)
	go func() {
		r, _ := f.svc.Ask(context.Background(), AskRequest{Caller: "assistant", Question: "?", Options: []Option{{Value: "y", Label: "Yes"}}, Wait: 30 * time.Minute})
		done <- r
	}()
	<-f.timers.got
	open := f.thread(t, "assistant")[0]
	got, err := f.svc.WithdrawAsk(context.Background(), WithdrawRequest{Caller: "assistant", MessageID: open.ID})
	if err != nil || got.State != StateExpired || got.ClosedReason != ClosedWithdrawn {
		t.Fatalf("withdraw = %s/%q, %v", got.State, got.ClosedReason, err)
	}
	if r := <-done; !r.TimedOut || r.ClosedReason != ClosedWithdrawn || r.Pending {
		t.Fatalf("the blocked call = %+v", r)
	}
	if u, ok := updateOf(f, open.ID); !ok || u.Message.ClosedReason != ClosedWithdrawn || u.Change != ChangeState {
		t.Fatalf("update event = %+v, %v", u, ok)
	}
	if _, err := f.svc.Answer("assistant", open.ID, Answer{Values: []string{"y"}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("answering a withdrawn ask: %v", err)
	}
	f.svc.Flush()
	if got := f.deliver.delivered(); len(got) != 0 {
		t.Fatalf("delivered %q", got)
	}
}

func TestWithdrawRefusesAnotherAgentsAsk(t *testing.T) {
	f := newFixture(t)
	ask := abandonedAsk(t, f, 0)
	_, err := f.svc.WithdrawAsk(context.Background(), WithdrawRequest{Caller: "web", MessageID: ask.ID})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("another agent's withdraw = %v", err)
	}
	if got, _ := f.store.Message(ask.ID); got.State != StateOpen {
		t.Fatalf("ask = %s", got.State)
	}
}

func TestWithdrawOfASettledAskConflicts(t *testing.T) {
	f := newFixture(t)
	ask := abandonedAsk(t, f, 0)
	if _, err := f.svc.Answer("assistant", ask.ID, Answer{Values: []string{"y"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.WithdrawAsk(context.Background(), WithdrawRequest{Caller: "assistant", MessageID: ask.ID}); !errors.Is(err, ErrConflict) {
		t.Fatalf("withdrawing an answered ask = %v", err)
	}
	open := abandonedAsk(t, f, 0)
	if _, err := f.svc.WithdrawAsk(context.Background(), WithdrawRequest{Caller: "assistant", MessageID: open.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.WithdrawAsk(context.Background(), WithdrawRequest{Caller: "assistant", MessageID: open.ID}); !errors.Is(err, ErrConflict) {
		t.Fatalf("a second withdraw = %v", err)
	}
	if _, err := f.svc.WithdrawAsk(context.Background(), WithdrawRequest{Caller: "assistant", MessageID: "nope"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("malformed id = %v", err)
	}
}
