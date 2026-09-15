package consult

import (
	"context"
	"fmt"
	"os/exec"
	"reflect"
	"testing"
	"time"
)

func TestViewerOpensDispatchInCallerSession(t *testing.T) {
	var calls [][]string
	v := &Viewer{
		ConfigPath: "/tmp/leo.yaml",
		TmuxPath:   "tmux",
		Executable: func() (string, error) { return "/opt/leo", nil },
		ResolveCaller: func(caller string) (string, bool) {
			return "leo-" + caller, caller == "worker"
		},
		ExecCommand: func(name string, args ...string) *exec.Cmd {
			calls = append(calls, append([]string{name}, args...))
			if containsArg(args, "new-window") {
				return exec.Command("printf", "@1\\n")
			}
			return exec.Command("true")
		},
	}

	v.OnStart(Record{ID: "d-123abc", Caller: "worker", Template: "claude", Kind: "dispatch"})

	want := [][]string{
		{"tmux", "-L", "leo", "has-session", "-t", "=leo-worker"},
		{"tmux", "-L", "leo", "new-window", "-d", "-P", "-F", "#{window_id}", "-t", "=leo-worker", "-n", "claude·3abc", "'/opt/leo' --config '/tmp/leo.yaml' dispatch watch d-123abc"},
		{"tmux", "-L", "leo", "set-window-option", "-t", "@1", "remain-on-exit", "on"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("tmux calls = %#v\nwant %#v", calls, want)
	}
}

func TestViewerCreatesFallbackSession(t *testing.T) {
	var calls [][]string
	v := &Viewer{
		ConfigPath: "/tmp/leo.yaml",
		TmuxPath:   "tmux",
		Executable: func() (string, error) { return "/opt/leo", nil },
		ExecCommand: func(name string, args ...string) *exec.Cmd {
			calls = append(calls, append([]string{name}, args...))
			if len(args) > 2 && args[2] == "has-session" {
				return exec.Command("false")
			}
			if containsArg(args, "new-window") {
				return exec.Command("printf", "@2\\n")
			}
			return exec.Command("true")
		},
	}

	v.OnStart(Record{ID: "d-456def", Template: "claude", Kind: "dispatch"})

	want := [][]string{
		{"tmux", "-L", "leo", "has-session", "-t", "=leo-dispatch"},
		{"tmux", "-L", "leo", "new-session", "-d", "-s", "leo-dispatch"},
		{"tmux", "-L", "leo", "new-window", "-d", "-P", "-F", "#{window_id}", "-t", "=leo-dispatch", "-n", "claude·6def", "'/opt/leo' --config '/tmp/leo.yaml' dispatch watch d-456def"},
		{"tmux", "-L", "leo", "set-window-option", "-t", "@2", "remain-on-exit", "on"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("tmux calls = %#v\nwant %#v", calls, want)
	}
}

func TestViewerContinuesWhenFallbackSessionRaces(t *testing.T) {
	var calls [][]string
	hasSessionCalls := 0
	v := &Viewer{
		ConfigPath: "/tmp/leo.yaml",
		TmuxPath:   "tmux",
		Executable: func() (string, error) { return "/opt/leo", nil },
		ExecCommand: func(name string, args ...string) *exec.Cmd {
			calls = append(calls, append([]string{name}, args...))
			if len(args) > 2 && args[2] == "has-session" {
				hasSessionCalls++
				if hasSessionCalls == 1 {
					return exec.Command("false")
				}
			}
			if len(args) > 2 && args[2] == "new-session" {
				return exec.Command("false") // Another dispatch won the race.
			}
			return exec.Command("true")
		},
	}

	v.OnStart(Record{ID: "d-789abc", Kind: "dispatch"})
	if !containsCall(calls, "new-window") {
		t.Fatalf("new-window missing after fallback session race: %#v", calls)
	}
}

func TestViewerShellQuotesExecutable(t *testing.T) {
	var calls [][]string
	v := &Viewer{
		ConfigPath: "/tmp/leo.yaml",
		TmuxPath:   "tmux",
		Executable: func() (string, error) { return "/opt/Leo Tools/leo's", nil },
		ExecCommand: func(name string, args ...string) *exec.Cmd {
			calls = append(calls, append([]string{name}, args...))
			return exec.Command("true")
		},
	}
	v.OnStart(Record{ID: "d-c0ffee", Kind: "dispatch"})
	for _, call := range calls {
		if containsArg(call, "new-window") && !containsArg(call, `'/opt/Leo Tools/leo'"'"'s' --config '/tmp/leo.yaml' dispatch watch d-c0ffee`) {
			t.Fatalf("unquoted watch command: %#v", call)
		}
	}
}

func TestViewerWindowNameSanitizesAndTruncatesLabel(t *testing.T) {
	rec := Record{ID: "d-0123456789ab", Name: "one two:three.four-five-six-seven"}
	if got, want := viewerWindowName(rec), "one-two-three-four-five-·89ab"; got != want {
		t.Fatalf("viewerWindowName = %q, want %q", got, want)
	}
}

func containsCall(calls [][]string, want string) bool {
	for _, call := range calls {
		if containsArg(call, want) {
			return true
		}
	}
	return false
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func TestViewerFailureDoesNotAffectStart(t *testing.T) {
	v := &Viewer{
		TmuxPath:    "tmux",
		Executable:  func() (string, error) { return "/opt/leo", nil },
		ExecCommand: func(string, ...string) *exec.Cmd { return exec.Command("false") },
	}
	d := NewDispatcherWithOnStart(nil, context.Background(), v.OnStart)
	d.ExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "echo", `{"type":"result","result":"done","is_error":false}`)
	}
	if _, err := d.Start(context.Background(), testConfig(), dispatchRequest(t)); err != nil {
		t.Fatalf("Start returned tmux failure: %v", err)
	}
}

func TestViewerTmuxCommandsTimeOut(t *testing.T) {
	v := &Viewer{
		TmuxPath: "tmux", Timeout: 10 * time.Millisecond,
		ExecCommandContext: func(ctx context.Context, name string, args ...string) *exec.Cmd {
			return exec.CommandContext(ctx, "sh", "-c", "sleep 1 & sleep 1")
		},
	}
	started := time.Now()
	v.OnStart(Record{ID: "d-timeout", Kind: "dispatch"})
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("OnStart blocked for %s after tmux timeout", elapsed)
	}
}

func TestViewerSkipsConsults(t *testing.T) {
	called := false
	v := &Viewer{ExecCommand: func(string, ...string) *exec.Cmd { called = true; return exec.Command("true") }}
	v.OnStart(Record{ID: "d-123abc", Kind: "consult"})
	if called {
		t.Fatal("consult opened a viewer")
	}
}

func TestViewerClosesDoneDispatchOnCollection(t *testing.T) {
	var calls [][]string
	v := &Viewer{
		TmuxPath: "tmux",
		ExecCommand: func(name string, args ...string) *exec.Cmd {
			calls = append(calls, append([]string{name}, args...))
			return exec.Command("true")
		},
		windowIDs: map[string]string{"d-done": "@42"},
	}

	v.Close(Record{ID: "d-done", Kind: "dispatch", Status: StatusDone})
	if !reflect.DeepEqual(calls, [][]string{{"tmux", "-L", "leo", "kill-window", "-t", "@42"}}) {
		t.Fatalf("tmux calls = %#v", calls)
	}
}

func TestViewerCloseUsesPersistedWindowIDAfterRestart(t *testing.T) {
	var calls [][]string
	v := &Viewer{
		TmuxPath: "tmux",
		ExecCommand: func(name string, args ...string) *exec.Cmd {
			calls = append(calls, append([]string{name}, args...))
			return exec.Command("true")
		},
	}

	v.Close(Record{ID: "d-done", Kind: "dispatch", Status: StatusDone, ViewerWindowID: "@42"})
	if !reflect.DeepEqual(calls, [][]string{{"tmux", "-L", "leo", "kill-window", "-t", "@42"}}) {
		t.Fatalf("tmux calls = %#v", calls)
	}
}

func TestViewerLeavesFailedDispatchOpen(t *testing.T) {
	called := false
	v := &Viewer{
		ExecCommand: func(string, ...string) *exec.Cmd { called = true; return exec.Command("true") },
		windowIDs:   map[string]string{"d-failed": "@42"},
	}

	v.Close(Record{ID: "d-failed", Kind: "dispatch", Status: StatusFailed})
	if called {
		t.Fatal("failed dispatch closed its viewer")
	}
}

func TestViewerSweepClosesTerminalDispatchAfterGrace(t *testing.T) {
	var calls [][]string
	now := time.Now()
	v := &Viewer{
		TmuxPath: "tmux",
		ExecCommand: func(name string, args ...string) *exec.Cmd {
			calls = append(calls, append([]string{name}, args...))
			return exec.Command("true")
		},
		windowIDs: map[string]string{"d-failed": "@42"},
	}

	v.Sweep([]Record{{ID: "d-failed", Kind: "dispatch", Status: StatusFailed, EndedAt: now.Add(-viewerGraceAfterEnd - time.Second)}}, now)
	if !reflect.DeepEqual(calls, [][]string{{"tmux", "-L", "leo", "kill-window", "-t", "@42"}}) {
		t.Fatalf("tmux calls = %#v", calls)
	}
}

func TestViewerSweepAttemptsStalePersistedWindowOnlyOnce(t *testing.T) {
	var calls [][]string
	now := time.Now()
	v := &Viewer{
		TmuxPath: "tmux",
		ExecCommand: func(name string, args ...string) *exec.Cmd {
			calls = append(calls, append([]string{name}, args...))
			return exec.Command("false")
		},
	}
	record := Record{ID: "d-stale", Kind: "dispatch", Status: StatusFailed, EndedAt: now.Add(-viewerGraceAfterEnd - time.Second), ViewerWindowID: "@42"}
	v.Sweep([]Record{record}, now)
	v.Sweep([]Record{record}, now.Add(time.Second))
	if got := len(calls); got != 1 {
		t.Fatalf("kill attempts = %d, want 1; calls=%#v", got, calls)
	}
}

func TestViewerCollectionOfClosedWindowIsNoop(t *testing.T) {
	called := false
	v := &Viewer{
		ExecCommand: func(string, ...string) *exec.Cmd { called = true; return exec.Command("true") },
		windowIDs:   map[string]string{"d-done": "@42"},
	}
	rec := Record{ID: "d-done", Kind: "dispatch", Status: StatusDone}
	v.Close(rec)
	called = false
	v.Close(rec)
	if called {
		t.Fatal("closed window was killed twice")
	}
}

func TestViewerPersistedCloseAndSweepDoNotRekillAfterHandledEviction(t *testing.T) {
	var calls [][]string
	v := &Viewer{TmuxPath: "tmux", ExecCommand: func(name string, args ...string) *exec.Cmd {
		calls = append(calls, append([]string{name}, args...))
		return exec.Command("true")
	}}
	target := Record{ID: "d-target", Kind: "dispatch", Status: StatusDone, ViewerWindowID: "@target"}
	v.Close(target)
	v.Close(target)
	v.Close(target)
	for i := 0; i < viewerHandledLimit; i++ {
		v.Close(Record{ID: fmt.Sprintf("d-pressure-%d", i), Kind: "dispatch", Status: StatusDone, ViewerWindowID: fmt.Sprintf("@%d", i)})
	}
	now := time.Now()
	target.EndedAt = now.Add(-viewerGraceAfterEnd - time.Second)
	v.Sweep([]Record{target}, now)
	if got := len(calls); got != viewerHandledLimit+1 {
		t.Fatalf("kill calls = %d, want %d; calls=%#v", got, viewerHandledLimit+1, calls)
	}
}

func TestViewerSweepDoesNotRetrackHandledWindow(t *testing.T) {
	now := time.Now()
	v := &Viewer{TmuxPath: "tmux", ExecCommand: func(string, ...string) *exec.Cmd { return exec.Command("true") }}
	record := Record{ID: "d-done", Kind: "dispatch", Status: StatusDone, EndedAt: now, ViewerWindowID: "@42"}
	v.Close(record)
	v.Sweep([]Record{record}, now.Add(time.Minute))
	v.mu.Lock()
	defer v.mu.Unlock()
	if _, tracked := v.windowIDs[record.ID]; tracked {
		t.Fatalf("handled viewer was re-tracked: %#v", v.windowIDs)
	}
}
