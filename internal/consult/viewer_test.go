package consult

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"testing"
	"time"
)

func viewerConfig(t *testing.T, placement string) string {
	t.Helper()
	path := t.TempDir() + "/leo.yaml"
	contents := "defaults:\n  dispatch:\n    viewer:\n      placement: " + placement + "\ntasks: {}\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestViewerSplitArgv(t *testing.T) {
	var calls [][]string
	v := &Viewer{ConfigPath: viewerConfig(t, "pane"), TmuxPath: "tmux", Executable: func() (string, error) { return "/opt/leo", nil }, ExecCommand: func(name string, args ...string) *exec.Cmd {
		calls = append(calls, append([]string{name}, args...))
		if len(args) > 2 && args[2] == "split-window" {
			return exec.Command("printf", "%%9\\n")
		}
		return exec.Command("true")
	}}
	rec := Record{ID: "d-123abc", Kind: "dispatch", Template: "claude", Cwd: "/work", CallerPaneID: "%1", CallerSessionID: "$1", CallerWindowID: "@1"}
	if got := v.OnStart(rec); got != "%9" {
		t.Fatalf("OnStart=%q", got)
	}
	want := [][]string{
		{"tmux", "-L", "leo", "show-options", "-t", "$1"},
		{"tmux", "-L", "leo", "split-window", "-d", "-P", "-F", "#{pane_id}", "-t", "%1", "-c", "/work", "'/opt/leo' --config '" + v.ConfigPath + "' dispatch watch d-123abc"},
		{"tmux", "-L", "leo", "select-pane", "-t", "%9", "-T", "claude·3abc"},
		{"tmux", "-L", "leo", "set-option", "-p", "-t", "%9", "remain-on-exit", "on"},
		{"tmux", "-L", "leo", "set-option", "-w", "-t", "@1", "main-pane-height", "60%"},
		{"tmux", "-L", "leo", "select-layout", "-t", "@1", "main-horizontal"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls=%#v\nwant=%#v", calls, want)
	}
}

func TestViewerSplitFailureFallsBack(t *testing.T) {
	var calls [][]string
	v := &Viewer{ConfigPath: viewerConfig(t, "pane"), TmuxPath: "tmux", Executable: func() (string, error) { return "/opt/leo", nil }, ResolveCaller: func(string) (string, bool) { return "leo-caller", true }, ExecCommand: func(name string, args ...string) *exec.Cmd {
		calls = append(calls, append([]string{name}, args...))
		if len(args) > 2 && args[2] == "split-window" {
			return exec.Command("false")
		}
		if len(args) > 2 && args[2] == "new-window" {
			return exec.Command("printf", "@7\\n")
		}
		return exec.Command("true")
	}}
	rec := Record{ID: "d-123abc", Kind: "dispatch", Caller: "caller", Template: "claude", Cwd: "/work", CallerPaneID: "%1", CallerSessionID: "$1", CallerWindowID: "@1"}
	if got := v.OnStart(rec); got != "@7" {
		t.Fatalf("OnStart=%q", got)
	}
	want := [][]string{
		{"tmux", "-L", "leo", "show-options", "-t", "$1"},
		{"tmux", "-L", "leo", "split-window", "-d", "-P", "-F", "#{pane_id}", "-t", "%1", "-c", "/work", "'/opt/leo' --config '" + v.ConfigPath + "' dispatch watch d-123abc"},
		{"tmux", "-L", "leo", "has-session", "-t", "=leo-caller"},
		{"tmux", "-L", "leo", "new-window", "-d", "-P", "-F", "#{window_id}", "-t", "=leo-caller", "-n", "claude·3abc", "'/opt/leo' --config '" + v.ConfigPath + "' dispatch watch d-123abc"},
		{"tmux", "-L", "leo", "set-window-option", "-t", "@7", "remain-on-exit", "on"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls=%#v\nwant=%#v", calls, want)
	}
}

func TestViewerWindowArgvUnchanged(t *testing.T) {
	var calls [][]string
	v := &Viewer{ConfigPath: viewerConfig(t, "window"), TmuxPath: "tmux", Executable: func() (string, error) { return "/opt/leo", nil }, ResolveCaller: func(string) (string, bool) { return "leo-caller", true }, ExecCommand: func(name string, args ...string) *exec.Cmd {
		calls = append(calls, append([]string{name}, args...))
		if len(args) > 2 && args[2] == "new-window" {
			return exec.Command("printf", "@7\\n")
		}
		return exec.Command("true")
	}}
	rec := Record{ID: "d-123abc", Kind: "dispatch", Caller: "caller", Template: "claude", CallerPaneID: "%1", CallerSessionID: "$1", CallerWindowID: "@1"}
	v.OnStart(rec)
	want := [][]string{{"tmux", "-L", "leo", "show-options", "-t", "$1"}, {"tmux", "-L", "leo", "has-session", "-t", "=leo-caller"}, {"tmux", "-L", "leo", "new-window", "-d", "-P", "-F", "#{window_id}", "-t", "=leo-caller", "-n", "claude·3abc", "'/opt/leo' --config '" + v.ConfigPath + "' dispatch watch d-123abc"}, {"tmux", "-L", "leo", "set-window-option", "-t", "@7", "remain-on-exit", "on"}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls=%#v\nwant=%#v", calls, want)
	}
}

func TestViewerSplitCollectAndSweep(t *testing.T) {
	var calls [][]string
	v := &Viewer{TmuxPath: "tmux", ExecCommand: func(name string, args ...string) *exec.Cmd {
		calls = append(calls, append([]string{name}, args...))
		return exec.Command("true")
	}}
	v.Close(Record{ID: "d-done", Kind: "dispatch", Status: StatusDone, ViewerKind: "split", ViewerPaneID: "%9", CallerPaneID: "%1", CallerWindowID: "@1"})
	now := time.Now()
	v.Sweep([]Record{{ID: "d-fail", Kind: "dispatch", Status: StatusFailed, EndedAt: now.Add(-viewerGraceAfterEnd - time.Second), ViewerKind: "split", ViewerPaneID: "%8", CallerPaneID: "%1", CallerWindowID: "@1"}}, now)
	want := [][]string{{"tmux", "-L", "leo", "kill-pane", "-t", "%9"}, {"tmux", "-L", "leo", "select-layout", "-t", "@1", "main-horizontal"}, {"tmux", "-L", "leo", "kill-pane", "-t", "%8"}, {"tmux", "-L", "leo", "select-layout", "-t", "@1", "main-horizontal"}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls=%#v\nwant=%#v", calls, want)
	}
}

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

func TestViewerBoundsHandledWindowsInInsertionOrder(t *testing.T) {
	v := &Viewer{TmuxPath: "tmux", ExecCommand: func(string, ...string) *exec.Cmd { return exec.Command("true") }}
	for i := 0; i < viewerHandledLimit+10; i++ {
		v.Close(Record{ID: fmt.Sprintf("d-%04d", i), Kind: "dispatch", Status: StatusDone, ViewerWindowID: fmt.Sprintf("@%d", i)})
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	if got := len(v.handledWindowIDs); got != viewerHandledLimit {
		t.Fatalf("handled windows = %d, want %d", got, viewerHandledLimit)
	}
	for i := 0; i < 10; i++ {
		if _, found := v.handledWindowIDs[fmt.Sprintf("d-%04d", i)]; found {
			t.Fatalf("old handled window d-%04d was retained", i)
		}
	}
	for i := 10; i < viewerHandledLimit+10; i++ {
		if got := v.handledWindowIDs[fmt.Sprintf("d-%04d", i)]; got != fmt.Sprintf("@%d", i) {
			t.Fatalf("handled window d-%04d = %q, want @%d", i, got, i)
		}
	}
}

func TestViewerSweepPrunesAbsentHandledWindowsWithoutRekillingPresentWindow(t *testing.T) {
	var calls [][]string
	v := &Viewer{TmuxPath: "tmux", ExecCommand: func(name string, args ...string) *exec.Cmd {
		calls = append(calls, append([]string{name}, args...))
		return exec.Command("true")
	}}
	v.Close(Record{ID: "d-pruned", Kind: "dispatch", Status: StatusDone, ViewerWindowID: "@pruned"})
	present := Record{ID: "d-present", Kind: "dispatch", Status: StatusFailed, ViewerWindowID: "@present"}
	v.Close(Record{ID: present.ID, Kind: "dispatch", Status: StatusDone, ViewerWindowID: present.ViewerWindowID})
	now := time.Now()
	present.EndedAt = now.Add(-viewerGraceAfterEnd - time.Second)
	v.Sweep([]Record{present}, now)
	v.Sweep([]Record{present}, now.Add(time.Second))

	if got := len(calls); got != 2 {
		t.Fatalf("kill calls = %d, want 2; calls=%#v", got, calls)
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if _, found := v.handledWindowIDs["d-pruned"]; found {
		t.Fatal("handled window for absent record was retained")
	}
	if got := v.handledWindowIDs[present.ID]; got != present.ViewerWindowID {
		t.Fatalf("present handled window = %q, want %q", got, present.ViewerWindowID)
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
