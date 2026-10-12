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
	harness   string
	err       error
	stopped   map[string]bool
	onDeliver func()
}

func (f *fakeDeliverer) Deliver(_ context.Context, agent, prompt string, starting func()) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
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
}

func (p *recordingPublisher) Publish(ev observe.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, ev)
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

type harnessFixture struct {
	svc        *Service
	store      *Store
	deliver    *fakeDeliverer
	pub        *recordingPublisher
	clock      *testClock
	timers     *manualTimers
	cfg        *config.Config
	root       string
	transcript func(ctx context.Context, path string) (string, error)
}

func newFixture(t *testing.T) *harnessFixture {
	t.Helper()
	f := &harnessFixture{
		store:   openTestStore(t),
		deliver: &fakeDeliverer{stopped: map[string]bool{}},
		pub:     &recordingPublisher{},
		clock:   newTestClock(),
		timers:  newManualTimers(),
		cfg:     &config.Config{HomePath: "/home/leo", Tasks: map[string]config.TaskConfig{}},
		root:    t.TempDir(),
	}
	f.transcript = func(context.Context, string) (string, error) { return "", errors.New("no whisper") }
	f.svc = NewService(Options{
		Store:   f.store,
		Files:   Files{Root: f.root},
		Deliver: f.deliver,
		Transcribe: func(ctx context.Context, path string) (string, error) {
			return f.transcript(ctx, path)
		},
		Publish: f.pub,
		Now:     f.clock.Now,
		IDs:     NewULIDSource(f.clock.Now, nil),
		After:   f.timers.after,
		Agents: func() []AgentRef {
			return []AgentRef{{Name: "assistant", Workspace: "/home/leo/workspace"}, {Name: "web", Workspace: "/src/web"}}
		},
		Config: func() (*config.Config, error) { return f.cfg, nil },
	})
	t.Cleanup(f.svc.Close)
	return f
}

func (f *harnessFixture) thread(t *testing.T, agent string) []Message {
	t.Helper()
	ms, err := f.store.Messages(agent, Page{})
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

func TestPostCopiesFilesAndFlagsCaptures(t *testing.T) {
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
	caps, _ := f.store.Captures("assistant")
	if len(caps) != 1 || !caps[0].ToolPosted {
		t.Fatalf("leo_chat_post must flag the pending capture: %+v", caps)
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
	if _, err := f.svc.Answer("assistant", open.ID, "maybe"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a value outside the options must be refused: %v", err)
	}
	if _, err := f.svc.Answer("assistant", open.ID, "y"); err != nil {
		t.Fatal(err)
	}
	got := <-done
	if got.err != nil || got.r.Value != "y" || got.r.Label != "Yes" || got.r.TimedOut {
		t.Fatalf("ask result = %+v", got)
	}
	if _, err := f.svc.Answer("assistant", open.ID, "n"); !errors.Is(err, ErrConflict) {
		t.Fatalf("second answer err = %v", err)
	}
}

func TestAskTimesOutAndExpires(t *testing.T) {
	f := newFixture(t)
	done := make(chan AskResult, 1)
	go func() {
		r, _ := f.svc.Ask(context.Background(), AskRequest{Caller: "assistant", Question: "?", Options: []Option{{Value: "a", Label: "a"}}})
		done <- r
	}()
	if d := <-f.timers.got; d != DefaultAskTimeout {
		t.Fatalf("default timeout = %s", d)
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
		"no options":   {Caller: "assistant", Question: "?"},
		"too many":     {Caller: "assistant", Question: "?", Options: many},
		"no question":  {Caller: "assistant", Options: []Option{{Value: "a"}}},
		"long timeout": {Caller: "assistant", Question: "?", Options: []Option{{Value: "a"}}, Timeout: 2 * time.Hour},
		"dup values":   {Caller: "assistant", Question: "?", Options: []Option{{Value: "a"}, {Value: "a"}}},
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
	if _, err := f.svc.Answer("assistant", m.ID, "allow"); err != nil {
		t.Fatal(err)
	}
	if got := <-done; got != "allow" {
		t.Fatalf("decision = %q", got)
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
	if m.State != StateAnswered || m.Answer != AnswerTerminal {
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
	if m.State != StateAnswered || m.Answer != AnswerTerminal {
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
	path := transcript{}.user("[Leo Chat] status?").assistant("Posted it.").write(t)
	f.svc.OnHook("assistant", HookEvent{Event: "Stop", TranscriptPath: path, LastAssistantMessage: "Posted it."})
	thread := f.thread(t, "assistant")
	if len(thread) != 2 || thread[0].Text != "posted myself" {
		t.Fatalf("thread = %+v", texts(thread))
	}
}

func TestStartupExpiresOpenPrompts(t *testing.T) {
	f := newFixture(t)
	m := Message{ID: NewULIDSource(f.clock.Now, nil).New(), Agent: "assistant", Direction: DirectionOut, Kind: KindAsk, State: StateOpen, TS: f.clock.Now()}
	if err := f.store.InsertMessage(m); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Recover(); err != nil {
		t.Fatal(err)
	}
	got, _ := f.store.Message(m.ID)
	if got.State != StateExpired {
		t.Fatalf("state = %s", got.State)
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
