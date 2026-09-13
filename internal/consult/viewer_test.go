package consult

import (
	"context"
	"os/exec"
	"reflect"
	"testing"
)

func TestViewerOpensDispatchInCallerSession(t *testing.T) {
	var calls [][]string
	v := &Viewer{
		TmuxPath:   "tmux",
		Executable: func() (string, error) { return "/opt/leo", nil },
		ResolveCaller: func(caller string) (string, bool) {
			return "leo-" + caller, caller == "worker"
		},
		ExecCommand: func(name string, args ...string) *exec.Cmd {
			calls = append(calls, append([]string{name}, args...))
			return exec.Command("true")
		},
	}

	v.OnStart(Record{ID: "d-123abc", Caller: "worker", Kind: "dispatch"})

	want := [][]string{
		{"tmux", "-L", "leo", "list-panes", "-a", "-F", "#{pane_dead}\t#{window_name}\t#{window_id}"},
		{"tmux", "-L", "leo", "has-session", "-t", "=leo-worker"},
		{"tmux", "-L", "leo", "new-window", "-d", "-t", "=leo-worker", "-n", "d-123abc", "'/opt/leo' dispatch watch d-123abc"},
		{"tmux", "-L", "leo", "set-window-option", "-t", "=leo-worker:=d-123abc", "remain-on-exit", "on"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("tmux calls = %#v\nwant %#v", calls, want)
	}
}

func TestViewerCreatesFallbackSession(t *testing.T) {
	var calls [][]string
	v := &Viewer{
		TmuxPath:   "tmux",
		Executable: func() (string, error) { return "/opt/leo", nil },
		ExecCommand: func(name string, args ...string) *exec.Cmd {
			calls = append(calls, append([]string{name}, args...))
			if len(args) > 2 && args[2] == "has-session" {
				return exec.Command("false")
			}
			return exec.Command("true")
		},
	}

	v.OnStart(Record{ID: "d-456def", Kind: "dispatch"})

	want := [][]string{
		{"tmux", "-L", "leo", "list-panes", "-a", "-F", "#{pane_dead}\t#{window_name}\t#{window_id}"},
		{"tmux", "-L", "leo", "has-session", "-t", "=leo-dispatch"},
		{"tmux", "-L", "leo", "new-session", "-d", "-s", "leo-dispatch"},
		{"tmux", "-L", "leo", "new-window", "-d", "-t", "=leo-dispatch", "-n", "d-456def", "'/opt/leo' dispatch watch d-456def"},
		{"tmux", "-L", "leo", "set-window-option", "-t", "=leo-dispatch:=d-456def", "remain-on-exit", "on"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("tmux calls = %#v\nwant %#v", calls, want)
	}
}

func TestViewerContinuesWhenFallbackSessionRaces(t *testing.T) {
	var calls [][]string
	hasSessionCalls := 0
	v := &Viewer{
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
		TmuxPath:   "tmux",
		Executable: func() (string, error) { return "/opt/Leo Tools/leo's", nil },
		ExecCommand: func(name string, args ...string) *exec.Cmd {
			calls = append(calls, append([]string{name}, args...))
			return exec.Command("true")
		},
	}
	v.OnStart(Record{ID: "d-c0ffee", Kind: "dispatch"})
	for _, call := range calls {
		if containsArg(call, "new-window") && !containsArg(call, `'/opt/Leo Tools/leo'"'"'s' dispatch watch d-c0ffee`) {
			t.Fatalf("unquoted watch command: %#v", call)
		}
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

func TestViewerSkipsConsults(t *testing.T) {
	called := false
	v := &Viewer{ExecCommand: func(string, ...string) *exec.Cmd { called = true; return exec.Command("true") }}
	v.OnStart(Record{ID: "d-123abc", Kind: "consult"})
	if called {
		t.Fatal("consult opened a viewer")
	}
}
