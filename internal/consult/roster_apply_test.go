package consult

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

func TestViewerRosterAppliesChangesAndOwnershipAwareCleanup(t *testing.T) {
	var calls [][]string
	status := "1\n"
	inventory := "leo-worker\t$1\t@7\t%8\n"
	v := &Viewer{
		TmuxPath: "tmux",
		ExecCommand: func(name string, args ...string) *exec.Cmd {
			calls = append(calls, append([]string{name}, args...))
			switch {
			case containsArg(args, "list-panes"):
				return exec.Command("printf", "%s", inventory)
			case containsArg(args, "list-sessions"):
				return exec.Command("printf", "leo-worker\t$1\t\n")
			case containsArg(args, "show-options"):
				return exec.Command("printf", status)
			default:
				if containsArg(args, "set-option") && containsArg(args, "status") {
					if containsArg(args, "-u") {
						status = "1\n"
					} else if containsArg(args, "2") {
						status = "2\n"
					}
				}
				return exec.Command("true")
			}
		},
		windowIDs: map[string]string{"d-a": "@7"},
	}
	now := time.Now()
	rec := Record{ID: "d-a", Kind: "dispatch", Name: "build", Status: StatusIdle, StartedAt: now, ActiveSeconds: 2}

	v.UpdateRoster([]Record{rec}, now)
	assertRosterCall(t, calls, "set-option", "-t", "=leo-worker:", "status", "2")
	assertRosterCall(t, calls, "set-option", "-t", "=leo-worker:", "status-format[1]", "#[align=left] #{@leo_roster}")
	assertRosterCall(t, calls, "set-option", "-t", "=leo-worker:", "@leo_roster")
	firstWrites := countRosterWrites(calls, "@leo_roster")
	v.UpdateRoster([]Record{rec}, now.Add(time.Minute))
	if got := countRosterWrites(calls, "@leo_roster"); got != firstWrites {
		t.Fatalf("unchanged frozen roster writes = %d, want %d", got, firstWrites)
	}

	v.UpdateRoster(nil, now.Add(2*time.Minute))
	assertRosterCall(t, calls, "set-option", "-u", "-t", "=leo-worker:", "status-format[1]")
	assertRosterCall(t, calls, "set-option", "-u", "-t", "=leo-worker:", "@leo_roster")
	assertRosterCall(t, calls, "set-option", "-u", "-t", "=leo-worker:", "status")
}

func TestViewerRosterCleanupDropsEmptySessionStatusFormat(t *testing.T) {
	var calls [][]string
	v := rosterCleanupFormatViewer(&calls, "status-format\n")
	v.UpdateRoster(nil, time.Now())

	want := [][]string{
		{"tmux", "-L", "leo", "set-option", "-u", "-t", "=leo-worker:", "status-format[1]"},
		{"tmux", "-L", "leo", "show-options", "-t", "=leo-worker:", "status-format"},
		{"tmux", "-L", "leo", "set-option", "-u", "-t", "=leo-worker:", "status-format"},
	}
	assertRosterCallSequence(t, calls, want)
}

func TestViewerRosterCleanupPreservesNonEmptySessionStatusFormat(t *testing.T) {
	var calls [][]string
	v := rosterCleanupFormatViewer(&calls, "status-format[0] custom\n")
	v.UpdateRoster(nil, time.Now())

	assertRosterCallSequence(t, calls, [][]string{
		{"tmux", "-L", "leo", "set-option", "-u", "-t", "=leo-worker:", "status-format[1]"},
		{"tmux", "-L", "leo", "show-options", "-t", "=leo-worker:", "status-format"},
	})
	for _, call := range calls {
		if equalRosterCall(call, []string{"tmux", "-L", "leo", "set-option", "-u", "-t", "=leo-worker:", "status-format"}) {
			t.Fatalf("cleared non-empty session status-format: %#v", calls)
		}
	}
}

func TestViewerRosterPreservesUserStatusAndUsesContextExecSeam(t *testing.T) {
	legacyCalled := false
	var calls [][]string
	v := &Viewer{
		TmuxPath:    "tmux",
		ExecCommand: func(string, ...string) *exec.Cmd { legacyCalled = true; return exec.Command("false") },
		ExecCommandContext: func(_ context.Context, name string, args ...string) *exec.Cmd {
			calls = append(calls, append([]string{name}, args...))
			if containsArg(args, "list-panes") {
				return exec.Command("printf", "%s", "leo-worker\t$1\t@7\t%8\n")
			}
			if containsArg(args, "list-sessions") {
				return exec.Command("printf", "leo-worker\t$1\t\n")
			}
			if containsArg(args, "show-options") {
				return exec.Command("printf", "3\n")
			}
			return exec.Command("true")
		},
		windowIDs: map[string]string{"d-a": "@7"},
	}
	now := time.Now()
	v.UpdateRoster([]Record{{ID: "d-a", Kind: "dispatch", Status: StatusQueued, StartedAt: now}}, now)
	if legacyCalled {
		t.Fatal("roster bypassed context-aware exec seam")
	}
	for _, call := range calls {
		if containsArg(call, "status") && containsArg(call, "2") {
			t.Fatalf("user status was overwritten: %#v", call)
		}
	}
}

func TestViewerRosterRetriesOnlyFailedCleanupOperations(t *testing.T) {
	var calls [][]string
	failRosterUnset := true
	v := rosterTestViewer(&calls, func(args []string) *exec.Cmd {
		if containsArg(args, "-u") && containsArg(args, "@leo_roster") && failRosterUnset {
			failRosterUnset = false
			return exec.Command("false")
		}
		return exec.Command("true")
	})
	now := time.Now()
	rec := Record{ID: "d-a", Kind: "dispatch", Status: StatusIdle, StartedAt: now, ViewerWindowID: "@7"}
	v.UpdateRoster([]Record{rec}, now)
	v.UpdateRoster(nil, now)
	firstFormatUnsets := countRosterUnsets(calls, "status-format[1]")
	firstStatusUnsets := countRosterUnsets(calls, "status")
	v.UpdateRoster(nil, now)
	if got := countRosterUnsets(calls, "status-format[1]"); got != firstFormatUnsets {
		t.Fatalf("successful format cleanup retried: %d -> %d", firstFormatUnsets, got)
	}
	if got := countRosterUnsets(calls, "status"); got != firstStatusUnsets {
		t.Fatalf("successful status cleanup retried: %d -> %d", firstStatusUnsets, got)
	}
	if got := countRosterUnsets(calls, "@leo_roster"); got != 2 {
		t.Fatalf("failed roster cleanup attempts = %d, want 2", got)
	}
}

func TestViewerRosterRetriesFailedStatusMutationAfterOwnershipMarker(t *testing.T) {
	var calls [][]string
	failStatus := true
	v := rosterTestViewer(&calls, func(args []string) *exec.Cmd {
		if containsArg(args, "status") && containsArg(args, "2") && failStatus {
			failStatus = false
			return exec.Command("false")
		}
		return exec.Command("true")
	})
	now := time.Now()
	rec := Record{ID: "d-a", Kind: "dispatch", Status: StatusQueued, StartedAt: now, ViewerWindowID: "@7"}
	v.UpdateRoster([]Record{rec}, now)
	v.UpdateRoster([]Record{rec}, now)
	if got := countRosterWrites(calls, "status"); got != 2 {
		t.Fatalf("status mutation attempts = %d, want failed attempt plus retry; calls=%#v", got, calls)
	}
	if got := countRosterWrites(calls, rosterStatusMarker); got != 1 {
		t.Fatalf("ownership marker writes = %d, want 1", got)
	}
}

func TestViewerRosterReinitializesAfterPartialCleanup(t *testing.T) {
	var calls [][]string
	failFormatUnset := true
	v := rosterTestViewer(&calls, func(args []string) *exec.Cmd {
		if containsArg(args, "-u") && containsArg(args, "status-format[1]") && failFormatUnset {
			failFormatUnset = false
			return exec.Command("false")
		}
		return exec.Command("true")
	})
	now := time.Now()
	rec := Record{ID: "d-a", Kind: "dispatch", Status: StatusIdle, StartedAt: now, ViewerWindowID: "@7"}
	v.UpdateRoster([]Record{rec}, now)
	v.UpdateRoster(nil, now)
	formatWrites := countRosterWrites(calls, "status-format[1]")
	statusWrites := countRosterWrites(calls, "status")
	v.UpdateRoster([]Record{rec}, now)
	if got := countRosterWrites(calls, "status-format[1]"); got != formatWrites+1 {
		t.Fatalf("format writes = %d, want %d; calls=%#v", got, formatWrites+1, calls)
	}
	if got := countRosterWrites(calls, "status"); got != statusWrites+1 {
		t.Fatalf("status writes = %d, want %d; calls=%#v", got, statusWrites+1, calls)
	}
}

func TestViewerRosterReinitializesWhenSessionIDChanges(t *testing.T) {
	var calls [][]string
	inventory := "leo-worker\t$1\t@7\t%8\n"
	sessionID := "$1"
	v := &Viewer{TmuxPath: "tmux", ExecCommand: func(name string, args ...string) *exec.Cmd {
		calls = append(calls, append([]string{name}, args...))
		switch {
		case containsArg(args, "list-panes"):
			return exec.Command("printf", "%s", inventory)
		case containsArg(args, "list-sessions"):
			return exec.Command("printf", "leo-worker\t"+sessionID+"\t\t\t\n")
		case containsArg(args, "show-options"):
			return exec.Command("printf", "1\n")
		default:
			return exec.Command("true")
		}
	}, windowIDs: map[string]string{"d-a": "@7"}}
	now := time.Now()
	rec := Record{ID: "d-a", Kind: "dispatch", Status: StatusIdle, StartedAt: now, ViewerWindowID: "@7"}
	v.UpdateRoster([]Record{rec}, now)
	formatWrites := countRosterWrites(calls, "status-format[1]")
	statusWrites := countRosterWrites(calls, "status")
	rosterWrites := countRosterWrites(calls, "@leo_roster")
	inventory = "leo-worker\t$2\t@7\t%8\n"
	sessionID = "$2"
	v.UpdateRoster([]Record{rec}, now)
	for option, before := range map[string]int{"status-format[1]": formatWrites, "status": statusWrites, "@leo_roster": rosterWrites} {
		if got := countRosterWrites(calls, option); got != before+1 {
			t.Fatalf("%s writes = %d, want %d; calls=%#v", option, got, before+1, calls)
		}
	}
}

func TestViewerRosterRestartDiscoversTextAndCleansOwnedStatus(t *testing.T) {
	var calls [][]string
	v := &Viewer{TmuxPath: "tmux", ExecCommand: func(name string, args ...string) *exec.Cmd {
		calls = append(calls, append([]string{name}, args...))
		switch {
		case containsArg(args, "list-panes"):
			return exec.Command("printf", "")
		case containsArg(args, "list-sessions"):
			return exec.Command("printf", "%s", "leo-worker\t$1\t1\t1\told roster\n")
		case containsArg(args, "show-options"):
			return exec.Command("printf", "2\n")
		default:
			return exec.Command("true")
		}
	}}
	v.UpdateRoster(nil, time.Now())
	for _, option := range []string{"status-format[1]", "@leo_roster", "status", rosterStatusMarker, rosterMarker} {
		if countRosterUnsets(calls, option) != 1 {
			t.Fatalf("restart cleanup %s calls = %#v", option, calls)
		}
	}
}

func TestViewerRosterRestartAfterFailedStatusCleanupRetainsOwnership(t *testing.T) {
	var firstCalls [][]string
	first := restartCleanupViewer(&firstCalls, "leo-worker\t$1\t1\t1\told\n", func(args []string) *exec.Cmd {
		if containsArg(args, "-u") && containsArg(args, "status") {
			return exec.Command("false")
		}
		return exec.Command("true")
	})
	first.UpdateRoster(nil, time.Now())
	if got := countRosterUnsets(firstCalls, rosterStatusMarker); got != 0 {
		t.Fatalf("status ownership cleared before status: calls=%#v", firstCalls)
	}

	var restartCalls [][]string
	restarted := restartCleanupViewer(&restartCalls, "leo-worker\t$1\t\t1\t\n", func([]string) *exec.Cmd { return exec.Command("true") })
	restarted.UpdateRoster(nil, time.Now())
	if countRosterUnsets(restartCalls, "status") != 1 || countRosterUnsets(restartCalls, rosterStatusMarker) != 1 {
		t.Fatalf("restart did not finish owned status cleanup: %#v", restartCalls)
	}
}

func TestViewerRosterRestartAfterFailedFormatCleanupRetainsOwnership(t *testing.T) {
	var firstCalls [][]string
	first := restartCleanupViewer(&firstCalls, "leo-worker\t$1\t1\t\told\n", func(args []string) *exec.Cmd {
		if containsArg(args, "-u") && containsArg(args, "status-format[1]") {
			return exec.Command("false")
		}
		return exec.Command("true")
	})
	first.UpdateRoster(nil, time.Now())
	if got := countRosterUnsets(firstCalls, rosterMarker); got != 0 {
		t.Fatalf("roster ownership cleared before format: calls=%#v", firstCalls)
	}

	var restartCalls [][]string
	restarted := restartCleanupViewer(&restartCalls, "leo-worker\t$1\t1\t\t\n", func([]string) *exec.Cmd { return exec.Command("true") })
	restarted.UpdateRoster(nil, time.Now())
	if countRosterUnsets(restartCalls, "status-format[1]") != 1 || countRosterUnsets(restartCalls, rosterMarker) != 1 {
		t.Fatalf("restart did not finish owned roster cleanup: %#v", restartCalls)
	}
}

func restartCleanupViewer(calls *[][]string, sessions string, action func([]string) *exec.Cmd) *Viewer {
	return &Viewer{TmuxPath: "tmux", ExecCommand: func(name string, args ...string) *exec.Cmd {
		*calls = append(*calls, append([]string{name}, args...))
		switch {
		case containsArg(args, "list-panes"):
			return exec.Command("printf", "")
		case containsArg(args, "list-sessions"):
			return exec.Command("printf", "%s", sessions)
		case containsArg(args, "show-options"):
			return exec.Command("printf", "2\n")
		default:
			return action(args)
		}
	}}
}

func rosterCleanupFormatViewer(calls *[][]string, statusFormat string) *Viewer {
	return &Viewer{TmuxPath: "tmux", ExecCommand: func(name string, args ...string) *exec.Cmd {
		*calls = append(*calls, append([]string{name}, args...))
		switch {
		case containsArg(args, "list-panes"):
			return exec.Command("printf", "")
		case containsArg(args, "list-sessions"):
			return exec.Command("printf", "%s", "leo-worker\t$1\t1\t\told roster\n")
		case containsArg(args, "show-options") && containsArg(args, "status-format"):
			return exec.Command("printf", "%s", statusFormat)
		case containsArg(args, "show-options"):
			return exec.Command("printf", "2\n")
		default:
			return exec.Command("true")
		}
	}}
}

func TestViewerRosterSerializesFullUpdate(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	v := &Viewer{TmuxPath: "tmux", ExecCommand: func(_ string, args ...string) *exec.Cmd {
		if containsArg(args, "list-panes") {
			select {
			case entered <- struct{}{}:
				<-release
			default:
			}
			return exec.Command("printf", "")
		}
		if containsArg(args, "list-sessions") {
			return exec.Command("printf", "")
		}
		return exec.Command("true")
	}}
	done := make(chan struct{})
	go func() { v.UpdateRoster(nil, time.Now()); close(done) }()
	<-entered
	second := make(chan struct{})
	go func() { v.UpdateRoster(nil, time.Now()); close(second) }()
	select {
	case <-second:
		t.Fatal("second inventory ran before first UpdateRoster completed")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	<-done
	<-second
}

func rosterTestViewer(calls *[][]string, action func([]string) *exec.Cmd) *Viewer {
	return &Viewer{TmuxPath: "tmux", ExecCommand: func(name string, args ...string) *exec.Cmd {
		*calls = append(*calls, append([]string{name}, args...))
		switch {
		case containsArg(args, "list-panes"):
			return exec.Command("printf", "%s", "leo-worker\t$1\t@7\t%8\n")
		case containsArg(args, "list-sessions"):
			return exec.Command("printf", "%s", "leo-worker\t$1\t\t\t\n")
		case containsArg(args, "show-options"):
			return exec.Command("printf", "1\n")
		default:
			return action(args)
		}
	}, windowIDs: map[string]string{"d-a": "@7"}}
}

func assertRosterCall(t *testing.T, calls [][]string, parts ...string) {
	t.Helper()
	for _, call := range calls {
		ok := true
		for _, part := range parts {
			if !containsArg(call, part) {
				ok = false
				break
			}
		}
		if ok {
			return
		}
	}
	t.Fatalf("missing call containing %q in %#v", parts, calls)
}

func assertRosterCallSequence(t *testing.T, calls, want [][]string) {
	t.Helper()
	position := 0
	for _, call := range calls {
		if position < len(want) && equalRosterCall(call, want[position]) {
			position++
		}
	}
	if position != len(want) {
		t.Fatalf("missing ordered calls %#v in %#v", want[position:], calls)
	}
}

func equalRosterCall(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func countRosterWrites(calls [][]string, option string) int {
	n := 0
	for _, call := range calls {
		if containsArg(call, "set-option") && containsArg(call, option) && !containsArg(call, "-u") {
			n++
		}
	}
	return n
}

func countRosterUnsets(calls [][]string, option string) int {
	n := 0
	for _, call := range calls {
		if containsArg(call, "set-option") && containsArg(call, "-u") && containsArg(call, option) {
			n++
		}
	}
	return n
}
