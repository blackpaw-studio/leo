package codex

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/harness"
)

// memIDStore is a concurrency-safe in-memory SessionIDStore for tests.
type memIDStore struct {
	mu sync.Mutex
	id string
}

func (s *memIDStore) Get() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.id
}

func (s *memIDStore) Set(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.id = id
}

func (s *memIDStore) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.id = ""
}

const (
	freshThreadID = "019f4eba-a1a6-77b0-be48-091cd08350e9"
	freshText     = "pong"
)

// catFixture builds an execCommand replacement that ignores name/args and
// runs `cat <fixture>` instead, so real stdout is the fixture bytes with
// exit 0. Every invocation is recorded into calls (guarded by a mutex).
func catFixture(t *testing.T, fixture string, calls *[][]string, mu *sync.Mutex) func(ctx context.Context, name string, args ...string) *exec.Cmd {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatal(err)
	}
	return func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if mu != nil {
			mu.Lock()
			*calls = append(*calls, append([]string{}, args...))
			mu.Unlock()
		}
		return exec.CommandContext(ctx, "cat", path)
	}
}

// failEmpty builds an execCommand replacement that exits 1 with empty
// stdout — the stale-thread ("no rollout found") shape.
func failEmpty(calls *[][]string, mu *sync.Mutex) func(ctx context.Context, name string, args ...string) *exec.Cmd {
	return func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if mu != nil {
			mu.Lock()
			*calls = append(*calls, append([]string{}, args...))
			mu.Unlock()
		}
		return exec.CommandContext(ctx, "sh", "-c", "exit 1")
	}
}

func withExecCommand(t *testing.T, fn func(ctx context.Context, name string, args ...string) *exec.Cmd) {
	t.Helper()
	orig := execCommand
	execCommand = fn
	t.Cleanup(func() { execCommand = orig })
}

func TestTurnDriverInjectFreshRecordsThreadID(t *testing.T) {
	var calls [][]string
	var mu sync.Mutex
	withExecCommand(t, catFixture(t, "fresh.jsonl", &calls, &mu))

	ids := &memIDStore{}
	h := harness.SessionHandle{
		TmuxSession: "leo-test-fresh",
		Workspace:   t.TempDir(),
		HomePath:    t.TempDir(),
		TurnArgs:    []string{"exec", "--json", "--skip-git-repo-check"},
		IDs:         ids,
	}

	res, err := (TurnDriver{}).Inject(context.Background(), h, "hi")
	if err != nil {
		t.Fatalf("Inject: %v", err)
	}
	if res.Text != freshText {
		t.Errorf("Result.Text = %q, want %q", res.Text, freshText)
	}
	if ids.Get() != freshThreadID {
		t.Errorf("IDs.Get() = %q, want %q", ids.Get(), freshThreadID)
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	for _, tok := range calls[0] {
		if tok == "resume" {
			t.Errorf("fresh turn argv must not contain resume: %v", calls[0])
		}
	}
}

func TestTurnDriverInjectResumeArgvOrder(t *testing.T) {
	var calls [][]string
	var mu sync.Mutex
	withExecCommand(t, catFixture(t, "fresh.jsonl", &calls, &mu))

	ids := &memIDStore{}
	ids.Set("th_1")
	turnArgs := []string{"exec", "--json", "--skip-git-repo-check"}
	h := harness.SessionHandle{
		TmuxSession: "leo-test-resume",
		Workspace:   t.TempDir(),
		HomePath:    t.TempDir(),
		TurnArgs:    turnArgs,
		IDs:         ids,
	}

	if _, err := (TurnDriver{}).Inject(context.Background(), h, "hi"); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	want := append(append([]string{}, turnArgs...), "resume", "th_1", "hi")
	if strings.Join(calls[0], "\x00") != strings.Join(want, "\x00") {
		t.Errorf("argv = %#v\nwant %#v", calls[0], want)
	}
}

func TestTurnDriverStaleResumeFallsBackFresh(t *testing.T) {
	var calls [][]string
	var mu sync.Mutex

	callN := 0
	withExecCommand(t, func(ctx context.Context, name string, args ...string) *exec.Cmd {
		mu.Lock()
		calls = append(calls, append([]string{}, args...))
		callN++
		n := callN
		mu.Unlock()
		if n == 1 {
			return exec.CommandContext(ctx, "sh", "-c", "exit 1")
		}
		path, err := filepath.Abs(filepath.Join("testdata", "fresh.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		return exec.CommandContext(ctx, "cat", path)
	})

	ids := &memIDStore{}
	ids.Set("th_stale")
	h := harness.SessionHandle{
		TmuxSession: "leo-test-stale",
		Workspace:   t.TempDir(),
		HomePath:    t.TempDir(),
		TurnArgs:    []string{"exec", "--json", "--skip-git-repo-check"},
		IDs:         ids,
	}

	res, err := (TurnDriver{}).Inject(context.Background(), h, "hi")
	if err != nil {
		t.Fatalf("Inject: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("calls = %d, want 2 (first stale resume, then fresh retry)", len(calls))
	}
	found := false
	for _, tok := range calls[0] {
		if tok == "resume" {
			found = true
		}
	}
	if !found {
		t.Errorf("first call argv should contain resume: %v", calls[0])
	}
	for _, tok := range calls[1] {
		if tok == "resume" {
			t.Errorf("retry argv must not contain resume: %v", calls[1])
		}
	}
	if ids.Get() != freshThreadID {
		t.Errorf("IDs.Get() after fallback = %q, want %q (cleared then re-set)", ids.Get(), freshThreadID)
	}
	if res.Text != freshText {
		t.Errorf("Result.Text = %q, want %q", res.Text, freshText)
	}
}

// TestTurnDriverAbortDuringTurnDoesNotMisclassifyAsStale guards against
// AbortTurn's cancellation being confused with the stale-thread ("no
// rollout found") shape: both produce exit!=0 with empty stdout. An abort
// must return an error immediately — never clearing a valid stored id or
// silently re-running the message as a fresh turn.
func TestTurnDriverAbortDuringTurnDoesNotMisclassifyAsStale(t *testing.T) {
	var calls int32
	withExecCommand(t, func(ctx context.Context, name string, args ...string) *exec.Cmd {
		atomic.AddInt32(&calls, 1)
		return exec.CommandContext(ctx, "sh", "-c", "sleep 5")
	})

	ids := &memIDStore{}
	ids.Set("th_valid")
	h := harness.SessionHandle{
		TmuxSession: "leo-test-abort",
		Workspace:   t.TempDir(),
		HomePath:    t.TempDir(),
		TurnArgs:    []string{"exec", "--json", "--skip-git-repo-check"},
		IDs:         ids,
	}

	type outcome struct {
		res *harness.Result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := (TurnDriver{}).Inject(context.Background(), h, "hi")
		done <- outcome{res, err}
	}()

	// Wait for the turn to register its cancel func (i.e. the child is
	// about to run), then abort it.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, ok := aborts.Load(h.TmuxSession); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the turn to start")
		}
		time.Sleep(time.Millisecond)
	}
	if err := (TurnDriver{}).AbortTurn(h); err != nil {
		t.Fatalf("AbortTurn: %v", err)
	}

	var out outcome
	select {
	case out = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Inject did not return after abort")
	}

	if out.err == nil {
		t.Fatal("expected an error for an aborted turn")
	}
	if !strings.Contains(out.err.Error(), "turn cancelled") {
		t.Errorf("err = %v, want to mention turn cancelled", out.err)
	}
	if out.res != nil {
		t.Errorf("res = %v, want nil", out.res)
	}
	if ids.Get() != "th_valid" {
		t.Errorf("IDs.Get() = %q, want unchanged %q (abort must not clear a valid thread id)", ids.Get(), "th_valid")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("exec calls = %d, want exactly 1 (no fresh retry after an abort)", got)
	}
}

// TestTurnDriverCancelledCtxWithSuccessfulExitIsNotMisclassified guards the
// other half of the cancellation predicate: a turn whose child process
// completes successfully (exit 0, valid output) at the exact instant its
// context is cancelled must NOT be treated as an abort. Misclassifying it
// would discard a valid Result and, on a first turn, a freshly-minted
// thread id, losing continuity. The fake execCommand ignores ctx (so
// cancellation never kills the child), while the parent ctx passed to
// Inject is already cancelled before the call — isolating the
// `runErr != nil && turnCtx.Err() != nil` predicate from the unrelated
// question of whether ctx cancellation actually killed the process.
func TestTurnDriverCancelledCtxWithSuccessfulExitIsNotMisclassified(t *testing.T) {
	var calls [][]string
	var mu sync.Mutex
	path, err := filepath.Abs(filepath.Join("testdata", "fresh.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	withExecCommand(t, func(ctx context.Context, name string, args ...string) *exec.Cmd {
		mu.Lock()
		calls = append(calls, append([]string{}, args...))
		mu.Unlock()
		// Deliberately ignore ctx: the child must run to completion
		// regardless of the (already-cancelled) parent context, so the
		// test can isolate the runErr/ctx.Err() predicate.
		return exec.Command("cat", path)
	})

	ids := &memIDStore{}
	h := harness.SessionHandle{
		TmuxSession: "leo-test-cancelled-success",
		Workspace:   t.TempDir(),
		HomePath:    t.TempDir(),
		TurnArgs:    []string{"exec", "--json", "--skip-git-repo-check"},
		IDs:         ids,
	}

	parentCtx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before the turn even starts

	res, err := (TurnDriver{}).Inject(parentCtx, h, "hi")
	if err != nil {
		t.Fatalf("Inject: %v, want nil (a successful exit must not be misclassified as cancelled)", err)
	}
	if res == nil {
		t.Fatal("res = nil, want a valid Result")
	}
	if res.Text != freshText {
		t.Errorf("Result.Text = %q, want %q", res.Text, freshText)
	}
	if ids.Get() != freshThreadID {
		t.Errorf("IDs.Get() = %q, want %q (thread id must be recorded despite the cancelled ctx)", ids.Get(), freshThreadID)
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1 (no stale-thread retry)", len(calls))
	}
}

func TestTurnDriverSerializesPerSession(t *testing.T) {
	withExecCommand(t, func(ctx context.Context, name string, args ...string) *exec.Cmd {
		path, err := filepath.Abs(filepath.Join("testdata", "fresh.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		return exec.CommandContext(ctx, "sh", "-c", "sleep 0.05; cat \"$1\"", "_", path)
	})

	ids := &memIDStore{}
	h := harness.SessionHandle{
		TmuxSession: "leo-test-serial",
		Workspace:   t.TempDir(),
		HomePath:    t.TempDir(),
		TurnArgs:    []string{"exec", "--json", "--skip-git-repo-check"},
		IDs:         ids,
	}

	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := (TurnDriver{}).Inject(context.Background(), h, "hi"); err != nil {
				t.Errorf("Inject: %v", err)
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	// Two 50ms turns serialized must take ~100ms; if they ran concurrently
	// they'd finish in ~50ms. Generous floor to avoid flakiness.
	if elapsed < 90*time.Millisecond {
		t.Errorf("elapsed = %v, want >= 90ms (turns must serialize per session)", elapsed)
	}
}

func TestTurnDriverStartRunsOpeningPromptOnce(t *testing.T) {
	var calls [][]string
	var mu sync.Mutex
	withExecCommand(t, catFixture(t, "fresh.jsonl", &calls, &mu))

	ids := &memIDStore{}
	h := harness.SessionHandle{
		TmuxSession:   "leo-test-start",
		Workspace:     t.TempDir(),
		HomePath:      t.TempDir(),
		TurnArgs:      []string{"exec", "--json", "--skip-git-repo-check"},
		OpeningPrompt: "hello",
		IDs:           ids,
	}

	if err := (TurnDriver{}).Start(context.Background(), h); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("calls after first Start = %d, want 1", len(calls))
	}
	if calls[0][len(calls[0])-1] != "hello" {
		t.Errorf("first Start argv should end with the opening prompt: %v", calls[0])
	}
	if ids.Get() != freshThreadID {
		t.Errorf("IDs.Get() = %q, want %q", ids.Get(), freshThreadID)
	}

	// Second Start: a thread id is already stored, so Start is a no-op.
	if err := (TurnDriver{}).Start(context.Background(), h); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	if len(calls) != 1 {
		t.Errorf("calls after second Start = %d, want still 1 (no-op)", len(calls))
	}
}

func TestTurnDriverTranscriptAppends(t *testing.T) {
	var calls [][]string
	var mu sync.Mutex
	withExecCommand(t, catFixture(t, "fresh.jsonl", &calls, &mu))

	ids := &memIDStore{}
	home := t.TempDir()
	h := harness.SessionHandle{
		TmuxSession: "leo-test-transcript",
		Workspace:   t.TempDir(),
		HomePath:    home,
		TurnArgs:    []string{"exec", "--json", "--skip-git-repo-check"},
		IDs:         ids,
	}

	if _, err := (TurnDriver{}).Inject(context.Background(), h, "hi there"); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	path := filepath.Join(home, "state", "transcripts", "leo-test-transcript.log")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading transcript: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "hi there") {
		t.Errorf("transcript missing user message: %q", content)
	}
	if !strings.Contains(content, freshText) {
		t.Errorf("transcript missing result text: %q", content)
	}
}

func TestTurnDriverStyle(t *testing.T) {
	if got := (TurnDriver{}).Style(); got != harness.DriveTurns {
		t.Errorf("Style() = %q, want %q", got, harness.DriveTurns)
	}
}

func TestTurnDriverAttach(t *testing.T) {
	h := harness.SessionHandle{TmuxSession: "leo-x", HomePath: "/home/leo"}
	spec, err := (TurnDriver{}).Attach(h)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	want := filepath.Join("/home/leo", "state", "transcripts", "leo-x.log")
	if spec.HistoryPath != want {
		t.Errorf("HistoryPath = %q, want %q", spec.HistoryPath, want)
	}
	if spec.Argv != nil {
		t.Errorf("Argv = %v, want nil", spec.Argv)
	}
}

func TestTurnDriverAbortTurnNoOpWhenIdle(t *testing.T) {
	if err := (TurnDriver{}).AbortTurn(harness.SessionHandle{TmuxSession: "leo-idle"}); err != nil {
		t.Errorf("AbortTurn: %v", err)
	}
}

func TestEnvSlice(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want []string
	}{
		{"nil", nil, []string{}},
		{"empty", map[string]string{}, []string{}},
		{"single", map[string]string{"K": "V"}, []string{"K=V"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := envSlice(tt.env)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("envSlice(%v) = %v, want %v", tt.env, got, tt.want)
			}
		})
	}
}
