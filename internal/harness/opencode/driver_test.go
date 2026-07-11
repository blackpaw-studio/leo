package opencode

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	attachSessionID = "ses_0ae242650ffeKkgOmScky8of5r"
	listSessionID   = "ses_0ae242650ffeKkgOmScky8of5r"
)

func withExecCommand(t *testing.T, fn func(ctx context.Context, name string, args ...string) *exec.Cmd) {
	t.Helper()
	orig := execCommand
	execCommand = fn
	t.Cleanup(func() { execCommand = orig })
}

// catFixture builds an execCommand replacement that ignores name/args and
// runs `cat <fixture>` instead, so real stdout is the fixture bytes with
// exit 0. Every invocation's args are recorded into calls (guarded by mu).
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

func writeServerState(t *testing.T, home, tmuxSession string, state ServerState) {
	t.Helper()
	dir := filepath.Join(home, "state", "opencode")
	if err := os.MkdirAll(dir, 0750); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, tmuxSession+".json"), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestServeArgsRenderServeCommand(t *testing.T) {
	spec := harness.LaunchSpec{
		Kind:    harness.KindProcess,
		Options: Options{ServerPort: 45991},
	}
	args, err := Opencode{}.Args(spec)
	if err != nil {
		t.Fatalf("Args: %v", err)
	}
	want := []string{"serve", "--port", "45991", "--hostname", "127.0.0.1"}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("args = %#v, want %#v", args, want)
	}
}

func TestServeArgsWithoutProvisionError(t *testing.T) {
	spec := harness.LaunchSpec{
		Kind:    harness.KindAgent,
		Options: Options{},
	}
	_, err := Opencode{}.Args(spec)
	if err == nil {
		t.Fatal("expected an error")
	}
	want := "opencode: internal error: server port not provisioned"
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
}

func TestServerDriverStartWaitsForHealth(t *testing.T) {
	t.Run("healthy on third poll", func(t *testing.T) {
		orig := healthPollInterval
		healthPollInterval = 5 * time.Millisecond
		t.Cleanup(func() { healthPollInterval = orig })

		var polls int
		var mu sync.Mutex
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			polls++
			n := polls
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if n < 3 {
				w.Write([]byte(`{"healthy":false}`))
				return
			}
			w.Write([]byte(`{"healthy":true}`))
		}))
		defer srv.Close()

		port := srv.Listener.Addr().(*net.TCPAddr).Port
		home := t.TempDir()
		writeServerState(t, home, "leo-test-health", ServerState{Port: port, Password: "x"})

		h := harness.SessionHandle{TmuxSession: "leo-test-health", HomePath: home}
		if err := (ServerDriver{}).Start(context.Background(), h); err != nil {
			t.Fatalf("Start: %v", err)
		}
		mu.Lock()
		defer mu.Unlock()
		if polls < 3 {
			t.Errorf("polls = %d, want >= 3", polls)
		}
	})

	t.Run("never healthy errors within tiny budget", func(t *testing.T) {
		origInterval, origBudget := healthPollInterval, healthPollBudget
		healthPollInterval = 2 * time.Millisecond
		healthPollBudget = 20 * time.Millisecond
		t.Cleanup(func() { healthPollInterval, healthPollBudget = origInterval, origBudget })

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"healthy":false}`))
		}))
		defer srv.Close()

		port := srv.Listener.Addr().(*net.TCPAddr).Port
		home := t.TempDir()
		writeServerState(t, home, "leo-test-unhealthy", ServerState{Port: port, Password: "x"})

		h := harness.SessionHandle{TmuxSession: "leo-test-unhealthy", HomePath: home}
		if err := (ServerDriver{}).Start(context.Background(), h); err == nil {
			t.Fatal("expected an error when the server never becomes healthy")
		}
	})
}

func TestServerDriverInjectArgvAndEnv(t *testing.T) {
	var calls [][]string
	var mu sync.Mutex
	var lastCmd *exec.Cmd
	withExecCommand(t, func(ctx context.Context, name string, args ...string) *exec.Cmd {
		path, err := filepath.Abs(filepath.Join("testdata", "attach_fresh.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.CommandContext(ctx, "cat", path)
		mu.Lock()
		calls = append(calls, append([]string{}, args...))
		lastCmd = cmd
		mu.Unlock()
		return cmd
	})

	home := t.TempDir()
	workspace := t.TempDir()
	writeServerState(t, home, "leo-test-argv", ServerState{Port: 12345, Password: "deadbeef"})

	ids := &memIDStore{}
	ids.Set("ses_stored")
	h := harness.SessionHandle{
		TmuxSession: "leo-test-argv",
		Workspace:   workspace,
		HomePath:    home,
		IDs:         ids,
	}

	if _, err := (ServerDriver{}).Inject(context.Background(), h, "hi"); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	want := []string{"run", "--attach", "http://127.0.0.1:12345", "--format", "json", "--dir", workspace, "-s", "ses_stored", "hi"}
	if strings.Join(calls[0], "\x00") != strings.Join(want, "\x00") {
		t.Errorf("argv = %#v\nwant %#v", calls[0], want)
	}

	// The driver sets cmd.Env (post-execCommand, pre-Run) to parent env plus
	// OPENCODE_SERVER_PASSWORD — assert it landed on the *exec.Cmd the seam
	// returned, since that's what actually ran.
	found := false
	for _, kv := range lastCmd.Env {
		if kv == "OPENCODE_SERVER_PASSWORD=deadbeef" {
			found = true
		}
	}
	if !found {
		t.Errorf("cmd.Env = %v, want to contain OPENCODE_SERVER_PASSWORD=deadbeef", lastCmd.Env)
	}
}

func TestServerDriverInjectSessionIDFromStream(t *testing.T) {
	var calls [][]string
	var mu sync.Mutex
	withExecCommand(t, catFixture(t, "attach_fresh.jsonl", &calls, &mu))

	home := t.TempDir()
	writeServerState(t, home, "leo-test-stream", ServerState{Port: 12345, Password: "x"})

	ids := &memIDStore{}
	h := harness.SessionHandle{TmuxSession: "leo-test-stream", Workspace: t.TempDir(), HomePath: home, IDs: ids}

	res, err := (ServerDriver{}).Inject(context.Background(), h, "hi")
	if err != nil {
		t.Fatalf("Inject: %v", err)
	}
	if res.SessionID != attachSessionID {
		t.Errorf("Result.SessionID = %q, want %q", res.SessionID, attachSessionID)
	}
	if ids.Get() != attachSessionID {
		t.Errorf("IDs.Get() = %q, want %q", ids.Get(), attachSessionID)
	}
}

func TestServerDriverInjectSessionIDFallbackToList(t *testing.T) {
	var calls [][]string
	var mu sync.Mutex
	callN := 0
	// The workspace must match session_list.json's sanitized "directory"
	// field exactly, and cmd.Dir must exist for the attach-run cmd.Run() to
	// succeed (exit 0 with empty stdout is the lossy-attach shape this test
	// exercises) — a nonexistent Dir would fail the chdir instead.
	workspace := "/tmp/leo-e2e-ws"
	if err := os.MkdirAll(workspace, 0755); err != nil {
		t.Fatal(err)
	}

	withExecCommand(t, func(ctx context.Context, name string, args ...string) *exec.Cmd {
		mu.Lock()
		calls = append(calls, append([]string{}, args...))
		callN++
		n := callN
		mu.Unlock()
		if n == 1 {
			// First call: attach run produces empty stdout (no events).
			return exec.CommandContext(ctx, "true")
		}
		path, err := filepath.Abs(filepath.Join("testdata", "session_list.json"))
		if err != nil {
			t.Fatal(err)
		}
		return exec.CommandContext(ctx, "cat", path)
	})

	home := t.TempDir()
	writeServerState(t, home, "leo-test-fallback", ServerState{Port: 12345, Password: "x"})

	ids := &memIDStore{}
	h := harness.SessionHandle{TmuxSession: "leo-test-fallback", Workspace: workspace, HomePath: home, IDs: ids}

	res, err := (ServerDriver{}).Inject(context.Background(), h, "hi")
	if err != nil {
		t.Fatalf("Inject: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("calls = %d, want 2 (attach run, then session list fallback)", len(calls))
	}
	if calls[1][0] != "session" || calls[1][1] != "list" {
		t.Errorf("second call = %v, want session list", calls[1])
	}
	if res.SessionID != listSessionID {
		t.Errorf("Result.SessionID = %q, want %q", res.SessionID, listSessionID)
	}
	if ids.Get() != listSessionID {
		t.Errorf("IDs.Get() = %q, want %q", ids.Get(), listSessionID)
	}
}

func TestServerDriverStaleSessionRetriesFresh(t *testing.T) {
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
			// Stale -s id: exit 1, empty stdout.
			return exec.CommandContext(ctx, "sh", "-c", "exit 1")
		}
		path, err := filepath.Abs(filepath.Join("testdata", "attach_fresh.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		return exec.CommandContext(ctx, "cat", path)
	})

	home := t.TempDir()
	writeServerState(t, home, "leo-test-stale", ServerState{Port: 12345, Password: "x"})

	ids := &memIDStore{}
	ids.Set("ses_stale")
	h := harness.SessionHandle{TmuxSession: "leo-test-stale", Workspace: t.TempDir(), HomePath: home, IDs: ids}

	res, err := (ServerDriver{}).Inject(context.Background(), h, "hi")
	if err != nil {
		t.Fatalf("Inject: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("calls = %d, want 2 (first stale, then fresh retry)", len(calls))
	}
	found := false
	for _, tok := range calls[0] {
		if tok == "-s" {
			found = true
		}
	}
	if !found {
		t.Errorf("first call argv should contain -s: %v", calls[0])
	}
	for _, tok := range calls[1] {
		if tok == "-s" {
			t.Errorf("retry argv must not contain -s: %v", calls[1])
		}
	}
	if res.SessionID != attachSessionID {
		t.Errorf("Result.SessionID = %q, want %q", res.SessionID, attachSessionID)
	}
	if ids.Get() != attachSessionID {
		t.Errorf("IDs.Get() after fallback = %q, want %q (cleared then re-set)", ids.Get(), attachSessionID)
	}
}

// TestServerDriverAbortDuringTurnDoesNotMisclassifyAsStale guards against
// AbortTurn's cancellation being confused with the stale-session ("Session
// not found") shape: both produce exit!=0 with empty stdout. An abort must
// return an error immediately — never clearing a valid stored id, never
// retrying fresh, never falling back to `session list`.
func TestServerDriverAbortDuringTurnDoesNotMisclassifyAsStale(t *testing.T) {
	var calls int32
	withExecCommand(t, func(ctx context.Context, name string, args ...string) *exec.Cmd {
		atomic.AddInt32(&calls, 1)
		return exec.CommandContext(ctx, "sh", "-c", "sleep 5")
	})

	home := t.TempDir()
	writeServerState(t, home, "leo-test-abort", ServerState{Port: 12345, Password: "x"})

	ids := &memIDStore{}
	ids.Set("ses_valid")
	h := harness.SessionHandle{TmuxSession: "leo-test-abort", Workspace: t.TempDir(), HomePath: home, IDs: ids}

	type outcome struct {
		res *harness.Result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := (ServerDriver{}).Inject(context.Background(), h, "hi")
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
	if err := (ServerDriver{}).AbortTurn(h); err != nil {
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
	if ids.Get() != "ses_valid" {
		t.Errorf("IDs.Get() = %q, want unchanged %q (abort must not clear a valid session id)", ids.Get(), "ses_valid")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("exec calls = %d, want exactly 1 (no fresh retry, no session-list fallback after an abort)", got)
	}
}

func TestServerDriverQuickExitKeepsSession(t *testing.T) {
	args := []string{"serve", "--port", "45991", "--hostname", "127.0.0.1"}
	gotArgs, action := (ServerDriver{}).RecoverQuickExit(args)
	if action != harness.QuickExitNone {
		t.Errorf("action = %v, want QuickExitNone", action)
	}
	if strconv.Itoa(len(gotArgs)) != strconv.Itoa(len(args)) || strings.Join(gotArgs, "\x00") != strings.Join(args, "\x00") {
		t.Errorf("args = %#v, want unchanged %#v", gotArgs, args)
	}
}

func TestServerDriverStyle(t *testing.T) {
	if got := (ServerDriver{}).Style(); got != harness.DriveTmux {
		t.Errorf("Style() = %q, want %q", got, harness.DriveTmux)
	}
}

func TestServerDriverAttach(t *testing.T) {
	home := t.TempDir()
	writeServerState(t, home, "leo-test-attach", ServerState{Port: 12345, Password: "secretpw"})

	ids := &memIDStore{}
	ids.Set("ses_x")
	h := harness.SessionHandle{TmuxSession: "leo-test-attach", Workspace: "/ws", HomePath: home, IDs: ids}

	spec, err := (ServerDriver{}).Attach(h)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	want := []string{"opencode", "attach", "http://127.0.0.1:12345", "--dir", "/ws", "-p", "secretpw", "-s", "ses_x"}
	if strings.Join(spec.Argv, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("Argv = %#v, want %#v", spec.Argv, want)
	}
}

func TestServerDriverAbortTurnNoOpWhenIdle(t *testing.T) {
	if err := (ServerDriver{}).AbortTurn(harness.SessionHandle{TmuxSession: "leo-idle"}); err != nil {
		t.Errorf("AbortTurn: %v", err)
	}
}
