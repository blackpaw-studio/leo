package tmuxtui

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

// fakeIDStore is a map-backed harness.SessionIDStore for tests.
type fakeIDStore struct {
	mu sync.Mutex
	id string
}

func (f *fakeIDStore) Get() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.id
}

func (f *fakeIDStore) Set(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.id = id
}

func (f *fakeIDStore) Clear() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.id = ""
}

func testProfile() tmux.Profile {
	return tmux.Profile{Marker: ">", Classify: func(string) tmux.InputState { return tmux.InputEmpty }}
}

func withFastDiscovery(t *testing.T) {
	t.Helper()
	prevPoll, prevBudget := discoverPoll, discoverBudget
	discoverPoll = 5 * time.Millisecond
	discoverBudget = 200 * time.Millisecond
	t.Cleanup(func() {
		discoverPoll, discoverBudget = prevPoll, prevBudget
	})
}

func TestInject_DelegatesToInjectSeam(t *testing.T) {
	var gotSession, gotBody string
	var gotProfile tmux.Profile
	restoreInject := SetInjectPromptForTest(func(_ context.Context, _ string, session, body string, p tmux.Profile) error {
		gotSession, gotBody, gotProfile = session, body, p
		return nil
	})
	defer restoreInject()
	restoreLocate := SetLocateTmuxForTest(func() (string, error) { return "/usr/bin/tmux", nil })
	defer restoreLocate()

	profile := testProfile()
	d := New(Config{Probe: profile})
	h := harness.SessionHandle{TmuxSession: "leo-foo", IDs: &fakeIDStore{}}

	res, err := d.Inject(context.Background(), h, "hello")
	if err != nil {
		t.Fatalf("Inject() error = %v", err)
	}
	if res != nil {
		t.Fatalf("Inject() result = %+v, want nil (fire-and-forget)", res)
	}
	if gotSession != "leo-foo" || gotBody != "hello" {
		t.Fatalf("inject seam got session=%q body=%q, want leo-foo/hello", gotSession, gotBody)
	}
	if gotProfile.Marker != profile.Marker {
		t.Fatalf("inject seam got profile marker=%q, want %q", gotProfile.Marker, profile.Marker)
	}
}

func TestInject_LocateError(t *testing.T) {
	restoreLocate := SetLocateTmuxForTest(func() (string, error) { return "", errors.New("no tmux") })
	defer restoreLocate()

	d := New(Config{})
	h := harness.SessionHandle{TmuxSession: "leo-foo", IDs: &fakeIDStore{}}
	_, err := d.Inject(context.Background(), h, "hello")
	if err == nil {
		t.Fatal("Inject() error = nil, want locate error to surface")
	}
}

func TestStart_InjectsOpeningPromptOnceWhenIDsEmpty(t *testing.T) {
	var injectCalls int
	restoreInject := SetInjectPromptForTest(func(_ context.Context, _ string, _ string, _ string, _ tmux.Profile) error {
		injectCalls++
		return nil
	})
	defer restoreInject()
	restoreLocate := SetLocateTmuxForTest(func() (string, error) { return "/usr/bin/tmux", nil })
	defer restoreLocate()

	d := New(Config{})
	h := harness.SessionHandle{TmuxSession: "leo-start-empty", OpeningPrompt: "hi", IDs: &fakeIDStore{}}

	if err := d.Start(context.Background(), h); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if injectCalls != 1 {
		t.Fatalf("injectCalls = %d, want 1", injectCalls)
	}
}

func TestStart_StoredIDSkipsOpeningPromptInjection(t *testing.T) {
	var injectCalls int
	restoreInject := SetInjectPromptForTest(func(_ context.Context, _ string, _ string, _ string, _ tmux.Profile) error {
		injectCalls++
		return nil
	})
	defer restoreInject()
	restoreLocate := SetLocateTmuxForTest(func() (string, error) { return "/usr/bin/tmux", nil })
	defer restoreLocate()

	d := New(Config{})
	store := &fakeIDStore{id: "abc123"}
	h := harness.SessionHandle{TmuxSession: "leo-start-stored", OpeningPrompt: "hi", IDs: store}

	if err := d.Start(context.Background(), h); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if injectCalls != 0 {
		t.Fatalf("injectCalls = %d, want 0 (restart-safe)", injectCalls)
	}
}

func TestStart_DiscoverIDFn_PollsUntilFound(t *testing.T) {
	withFastDiscovery(t)
	restoreLocate := SetLocateTmuxForTest(func() (string, error) { return "/usr/bin/tmux", nil })
	defer restoreLocate()

	var calls int32
	var mu sync.Mutex
	store := &fakeIDStore{}
	d := New(Config{DiscoverIDFn: func(_ context.Context, _ harness.SessionHandle, _ time.Time) (string, error) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n < 3 {
			return "", nil
		}
		return "discovered-id", nil
	}})
	h := harness.SessionHandle{TmuxSession: "leo-discover-poll", IDs: store}

	if err := d.Start(context.Background(), h); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for store.Get() == "" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := store.Get(); got != "discovered-id" {
		t.Fatalf("store.Get() = %q, want discovered-id", got)
	}
}

func TestStart_StoredID_NeverCallsDiscoverIDFn(t *testing.T) {
	withFastDiscovery(t)
	restoreLocate := SetLocateTmuxForTest(func() (string, error) { return "/usr/bin/tmux", nil })
	defer restoreLocate()

	var called int32
	store := &fakeIDStore{id: "already-have-one"}
	d := New(Config{DiscoverIDFn: func(_ context.Context, _ harness.SessionHandle, _ time.Time) (string, error) {
		called++
		return "should-not-be-used", nil
	}})
	h := harness.SessionHandle{TmuxSession: "leo-discover-stored", IDs: store}

	if err := d.Start(context.Background(), h); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	time.Sleep(50 * time.Millisecond)
	if called != 0 {
		t.Fatalf("DiscoverIDFn called %d times, want 0 when id already stored", called)
	}
}

func TestInject_DiscoverDedupe_ConcurrentCallsShareOneLoop(t *testing.T) {
	withFastDiscovery(t)
	restoreInject := SetInjectPromptForTest(func(_ context.Context, _ string, _ string, _ string, _ tmux.Profile) error {
		return nil
	})
	defer restoreInject()
	restoreLocate := SetLocateTmuxForTest(func() (string, error) { return "/usr/bin/tmux", nil })
	defer restoreLocate()

	var mu sync.Mutex
	var starts int
	release := make(chan struct{})
	store := &fakeIDStore{}
	d := New(Config{DiscoverIDFn: func(_ context.Context, _ harness.SessionHandle, _ time.Time) (string, error) {
		mu.Lock()
		starts++
		mu.Unlock()
		<-release // block until test lets it finish, to keep the loop in-flight
		return "id-1", nil
	}})
	h := harness.SessionHandle{TmuxSession: "leo-dedupe", IDs: store}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = d.Inject(context.Background(), h, "one")
	}()
	go func() {
		defer wg.Done()
		_, _ = d.Inject(context.Background(), h, "two")
	}()
	wg.Wait()

	// give the discovery goroutine(s) time to make their first call
	time.Sleep(50 * time.Millisecond)
	close(release)

	deadline := time.Now().Add(2 * time.Second)
	for store.Get() == "" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	mu.Lock()
	got := starts
	mu.Unlock()
	if got != 1 {
		t.Fatalf("DiscoverIDFn invoked from %d distinct discovery loops, want 1 (dedupe by TmuxSession)", got)
	}
}

func TestAttach_ReturnsTmuxSessionOnly(t *testing.T) {
	d := New(Config{})
	h := harness.SessionHandle{TmuxSession: "leo-attach"}
	spec, err := d.Attach(h)
	if err != nil {
		t.Fatalf("Attach() error = %v", err)
	}
	want := harness.AttachSpec{TmuxSession: "leo-attach"}
	if spec != want {
		t.Fatalf("Attach() = %+v, want %+v", spec, want)
	}
}

func TestPaneKey(t *testing.T) {
	t.Run("nil fn returns empty string", func(t *testing.T) {
		d := New(Config{})
		if got := d.PaneKey("some pane"); got != "" {
			t.Fatalf("PaneKey() = %q, want \"\"", got)
		}
	})
	t.Run("delegates to configured fn", func(t *testing.T) {
		d := New(Config{PaneKeyFn: func(pane string) string {
			if pane == "dialog" {
				return "Escape"
			}
			return ""
		}})
		if got := d.PaneKey("dialog"); got != "Escape" {
			t.Fatalf("PaneKey() = %q, want Escape", got)
		}
	})
}

func TestRecoverQuickExit(t *testing.T) {
	t.Run("nil fn returns args unchanged and ClearSession", func(t *testing.T) {
		d := New(Config{})
		args := []string{"a", "b"}
		gotArgs, gotAction := d.RecoverQuickExit(args)
		if len(gotArgs) != 2 || gotArgs[0] != "a" || gotArgs[1] != "b" {
			t.Fatalf("RecoverQuickExit() args = %v, want unchanged", gotArgs)
		}
		if gotAction != harness.QuickExitClearSession {
			t.Fatalf("RecoverQuickExit() action = %v, want QuickExitClearSession", gotAction)
		}
	})
	t.Run("delegates to configured fn", func(t *testing.T) {
		d := New(Config{RecoverFn: func(args []string) ([]string, harness.QuickExitAction) {
			return append(args, "extra"), harness.QuickExitRetryArgs
		}})
		gotArgs, gotAction := d.RecoverQuickExit([]string{"a"})
		if len(gotArgs) != 2 || gotArgs[1] != "extra" {
			t.Fatalf("RecoverQuickExit() args = %v, want [a extra]", gotArgs)
		}
		if gotAction != harness.QuickExitRetryArgs {
			t.Fatalf("RecoverQuickExit() action = %v, want QuickExitRetryArgs", gotAction)
		}
	})
}

func TestPreLaunch(t *testing.T) {
	t.Run("nil fn returns nil error", func(t *testing.T) {
		d := New(Config{})
		if err := d.PreLaunch(harness.SessionHandle{}); err != nil {
			t.Fatalf("PreLaunch() error = %v, want nil", err)
		}
	})
	t.Run("delegates to configured fn", func(t *testing.T) {
		wantErr := errors.New("boom")
		d := New(Config{PreLaunchFn: func(harness.SessionHandle) error { return wantErr }})
		if err := d.PreLaunch(harness.SessionHandle{}); !errors.Is(err, wantErr) {
			t.Fatalf("PreLaunch() error = %v, want %v", err, wantErr)
		}
	})
}

func TestRefreshSessionArgs(t *testing.T) {
	t.Run("nil fn returns args unchanged", func(t *testing.T) {
		d := New(Config{})
		args := []string{"--foo"}
		got := d.RefreshSessionArgs(args, "some-id")
		if len(got) != 1 || got[0] != "--foo" {
			t.Fatalf("RefreshSessionArgs() = %v, want unchanged", got)
		}
	})
	t.Run("delegates to configured fn", func(t *testing.T) {
		d := New(Config{RefreshArgsFn: func(args []string, storedID string) []string {
			return append(args, "--resume", storedID)
		}})
		got := d.RefreshSessionArgs([]string{"--foo"}, "session-1")
		want := []string{"--foo", "--resume", "session-1"}
		if len(got) != len(want) {
			t.Fatalf("RefreshSessionArgs() = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("RefreshSessionArgs() = %v, want %v", got, want)
			}
		}
	})
}

func TestAbortTurn_LocateError(t *testing.T) {
	restoreLocate := SetLocateTmuxForTest(func() (string, error) { return "", errors.New("no tmux") })
	defer restoreLocate()

	d := New(Config{})
	err := d.AbortTurn(harness.SessionHandle{TmuxSession: "leo-abort"})
	if err == nil {
		t.Fatal("AbortTurn() error = nil, want locate error to surface")
	}
}
