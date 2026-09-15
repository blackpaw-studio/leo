package consult

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const rosterTestFormat0Value = `#[bg=colour63,fg=colour253,bold] #S "quoted" \\ path with spaces`

func TestViewerRosterDiagnosticsLogOnlyOnChange(t *testing.T) {
	var logs []string
	inventory := "leo-leo\t$1\t@574\t%573\n"
	v := &Viewer{
		TmuxPath: "tmux",
		Logf:     func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) },
		ExecCommand: func(_ string, args ...string) *exec.Cmd {
			switch {
			case containsArg(args, "list-panes"):
				return exec.Command("printf", "%s", inventory)
			case containsArg(args, "list-sessions"):
				return exec.Command("printf", "leo-leo\t$1\t\t\t\n")
			case containsArg(args, "show-options") && containsArg(args, "status-interval") && !containsArg(args, "-A"):
				return exec.Command("printf", "")
			case containsArg(args, "show-options"):
				return exec.Command("printf", "2\n")
			default:
				return exec.Command("true")
			}
		},
	}
	now := time.Now()
	records := []Record{
		{ID: "d-ok", Kind: "dispatch", Mode: ModeInteractive, PaneID: "%573", Status: StatusQueued, StartedAt: now},
		{ID: "d-pane", Kind: "dispatch", Mode: ModeInteractive, PaneID: "%999", Status: StatusQueued, StartedAt: now},
		{ID: "d-window", Kind: "dispatch", ViewerWindowID: "@999", Status: StatusQueued, StartedAt: now},
		{ID: "c-no", Kind: "consult", Status: StatusQueued, StartedAt: now},
		{ID: "d-old", Kind: "dispatch", Status: StatusDone, EndedAt: now.Add(-viewerGraceAfterEnd), ViewerWindowID: "@574"},
	}
	v.UpdateRoster(records, now)
	v.UpdateRoster(records, now)
	want := "dispatch viewer: roster: inventory panes=1 sessions=1 records=5 eligible=3 resolved=[leo-leo:1] unresolved=[c-no:not-dispatch d-old:expired d-pane:no-pane %999 d-window:no-window @999]"
	if got := countExactLog(logs, want); got != 1 {
		t.Fatalf("inventory log count = %d, want 1; logs=%q", got, logs)
	}
	if got := countContainingLog(logs, `roster: applied to "leo-leo"`); got != 1 {
		t.Fatalf("applied log count = %d, want 1; logs=%q", got, logs)
	}
	records = append(records, Record{ID: "d-new", Kind: "dispatch", Mode: ModeInteractive, PaneID: "%573", Status: StatusQueued, StartedAt: now})
	v.UpdateRoster(records, now)
	if got := countContainingLog(logs, "roster: inventory panes="); got != 2 {
		t.Fatalf("changed inventory logs = %d, want 2; logs=%q", got, logs)
	}
}

func TestViewerRosterDiagnosticsLogsZeroPanesOnce(t *testing.T) {
	var logs []string
	v := &Viewer{TmuxPath: "tmux", Logf: func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }, ExecCommand: func(_ string, args ...string) *exec.Cmd {
		return exec.Command("printf", "")
	}}
	v.UpdateRoster(nil, time.Now())
	v.UpdateRoster(nil, time.Now())
	if got := countContainingLog(logs, "roster: inventory returned zero panes"); got != 1 {
		t.Fatalf("zero-pane logs = %d, want 1; logs=%q", got, logs)
	}
}

func TestViewerRosterDiagnosticsLogsApplyForRecreatedSession(t *testing.T) {
	var logs []string
	sessionID := "$1"
	v := &Viewer{TmuxPath: "tmux", Logf: func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }, ExecCommand: func(_ string, args ...string) *exec.Cmd {
		switch {
		case containsArg(args, "list-panes"):
			return exec.Command("printf", "%s", "leo-leo\t"+sessionID+"\t@1\t%1\n")
		case containsArg(args, "list-sessions"):
			return exec.Command("printf", "%s", "leo-leo\t"+sessionID+"\t\t\t\n")
		case containsArg(args, "show-options") && containsArg(args, "status-interval") && !containsArg(args, "-A"):
			return exec.Command("printf", "")
		case containsArg(args, "show-options"):
			return exec.Command("printf", "2\n")
		default:
			return exec.Command("true")
		}
	}}
	now := time.Now()
	record := Record{ID: "d-a", Kind: "dispatch", Mode: ModeInteractive, PaneID: "%1", Status: StatusQueued, StartedAt: now}
	v.UpdateRoster([]Record{record}, now)
	sessionID = "$2"
	v.UpdateRoster([]Record{record}, now)
	if got := countContainingLog(logs, `roster: applied to "leo-leo"`); got != 2 {
		t.Fatalf("applied logs = %d, want one per session identity; logs=%q", got, logs)
	}
}

func countExactLog(logs []string, want string) int {
	n := 0
	for _, line := range logs {
		if line == want {
			n++
		}
	}
	return n
}

func countContainingLog(logs []string, part string) int {
	n := 0
	for _, line := range logs {
		if strings.Contains(line, part) {
			n++
		}
	}
	return n
}

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
			case containsArg(args, "show-options") && containsArg(args, "status-interval") && !containsArg(args, "-A"):
				return exec.Command("printf", "")
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

func TestViewerRosterSetsStatusIntervalWhenInheritedEffectiveValueIsGreaterThanOne(t *testing.T) {
	var calls [][]string
	v := rosterIntervalViewer(&calls, "", "5\n", func([]string) *exec.Cmd { return exec.Command("true") })
	v.applyRoster("leo-worker", "roster", rosterSessionState{})
	assertRosterCallSequence(t, calls, [][]string{
		{"tmux", "-L", "leo", "show-options", "-t", "=leo-worker:", "-v", "status-interval"},
		{"tmux", "-L", "leo", "show-options", "-A", "-t", "=leo-worker:", "-v", "status-interval"},
		{"tmux", "-L", "leo", "set-option", "-t", "=leo-worker:", rosterStatusIntervalMarker, "1"},
		{"tmux", "-L", "leo", "set-option", "-t", "=leo-worker:", "status-interval", "1"},
	})
}

func TestViewerRosterDoesNotSetStatusIntervalWhenEffectiveValueIsOne(t *testing.T) {
	var calls [][]string
	v := rosterIntervalViewer(&calls, "", "1\n", func([]string) *exec.Cmd { return exec.Command("true") })
	v.applyRoster("leo-worker", "roster", rosterSessionState{})
	for _, call := range calls {
		if containsArg(call, rosterStatusIntervalMarker) || (containsArg(call, "status-interval") && containsArg(call, "set-option")) {
			t.Fatalf("changed status interval already set to one: %#v", calls)
		}
	}
}

func TestViewerRosterPreservesSessionLocalStatusInterval(t *testing.T) {
	var calls [][]string
	v := rosterIntervalViewer(&calls, "5\n", "5\n", func([]string) *exec.Cmd { return exec.Command("true") })
	v.applyRoster("leo-worker", "roster", rosterSessionState{})
	for _, call := range calls {
		if containsArg(call, rosterStatusIntervalMarker) || (containsArg(call, "set-option") && containsArg(call, "status-interval")) || (containsArg(call, "-A") && containsArg(call, "status-interval")) {
			t.Fatalf("changed session-local status interval: %#v", calls)
		}
	}
}

func TestViewerRosterCleanupUnsetsStatusIntervalOnlyWhenOwned(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state rosterSessionState
		want  int
	}{
		{"owned", rosterSessionState{statusIntervalMarked: true}, 1},
		{"not owned", rosterSessionState{}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls [][]string
			v := &Viewer{TmuxPath: "tmux", ExecCommand: func(name string, args ...string) *exec.Cmd {
				calls = append(calls, append([]string{name}, args...))
				if containsArg(args, "show-options") && containsArg(args, "status-interval") && !containsArg(args, "-A") {
					return exec.Command("printf", "1\n")
				}
				return exec.Command("true")
			}}
			v.defaults()
			v.clearRosterState("leo-worker", tc.state)
			if got := countRosterUnsets(calls, "status-interval"); got != tc.want {
				t.Fatalf("status-interval unsets = %d, want %d; calls=%#v", got, tc.want, calls)
			}
		})
	}
}

func TestViewerRosterCleanupPreservesUserChangedStatusInterval(t *testing.T) {
	var calls [][]string
	v := &Viewer{TmuxPath: "tmux", ExecCommand: func(name string, args ...string) *exec.Cmd {
		calls = append(calls, append([]string{name}, args...))
		if containsArg(args, "show-options") && containsArg(args, "status-interval") {
			return exec.Command("printf", "5\n")
		}
		return exec.Command("true")
	}}
	v.defaults()
	v.clearRosterState("leo-worker", rosterSessionState{statusIntervalMarked: true, statusIntervalSet: true})
	if got := countRosterUnsets(calls, "status-interval"); got != 0 {
		t.Fatalf("cleared user-changed status interval: %#v", calls)
	}
	if got := countRosterUnsets(calls, rosterStatusIntervalMarker); got != 1 {
		t.Fatalf("did not clear ownership marker: %#v", calls)
	}
}

func TestViewerRosterCleanupDoesNotUnsetStatusIntervalAfterSetFailure(t *testing.T) {
	var calls [][]string
	v := rosterIntervalViewer(&calls, "", "5\n", func(args []string) *exec.Cmd {
		if containsArg(args, "set-option") && containsArg(args, "status-interval") {
			return exec.Command("false")
		}
		return exec.Command("true")
	})
	v.defaults()
	state := v.applyRoster("leo-worker", "roster", rosterSessionState{})
	v.clearRosterState("leo-worker", state)
	if got := countRosterUnsets(calls, "status-interval"); got != 0 {
		t.Fatalf("unset status interval Leo never set: %#v", calls)
	}
	if got := countRosterUnsets(calls, rosterStatusIntervalMarker); got != 1 {
		t.Fatalf("did not clear ownership marker after set failure: %#v", calls)
	}
}

func TestViewerRosterRestartDiscoversOwnedStatusInterval(t *testing.T) {
	var calls [][]string
	v := restartCleanupViewer(&calls, "leo-worker\t$1\t\t\t1\t\t\n", func([]string) *exec.Cmd { return exec.Command("true") })
	v.UpdateRoster(nil, time.Now())
	if countRosterUnsets(calls, "status-interval") != 1 || countRosterUnsets(calls, rosterStatusIntervalMarker) != 1 {
		t.Fatalf("restart did not clean owned status interval: %#v", calls)
	}
}

func TestViewerRosterRestartWithOwnedStatusIntervalReappliesChangedText(t *testing.T) {
	var calls [][]string
	v := &Viewer{TmuxPath: "tmux", ExecCommand: func(name string, args ...string) *exec.Cmd {
		calls = append(calls, append([]string{name}, args...))
		switch {
		case containsArg(args, "list-panes"):
			return exec.Command("printf", "%s", "leo-worker\t$1\t@7\t%8\n")
		case containsArg(args, "list-sessions"):
			return exec.Command("printf", "leo-worker\t$1\t\t\t1\t\told roster\n")
		case containsArg(args, "show-options") && containsArg(args, "status-interval") && !containsArg(args, "-A"):
			return exec.Command("printf", "1\n")
		case containsArg(args, "show-options"):
			return exec.Command("printf", "1\n")
		default:
			return exec.Command("true")
		}
	}, windowIDs: map[string]string{"d-a": "@7"}}
	now := time.Now()
	v.UpdateRoster([]Record{{ID: "d-a", Kind: "dispatch", Name: "new", Status: StatusRunning, StartedAt: now, ViewerWindowID: "@7"}}, now)
	assertRosterCallSequence(t, calls, [][]string{
		{"tmux", "-L", "leo", "set-option", "-t", "=leo-worker:", "@leo_roster", "#[default]⟳ new 0:00#[default]"},
	})
}

func TestViewerRosterCleanupDropsEmptySessionStatusFormat(t *testing.T) {
	var calls [][]string
	v := rosterCleanupFormatViewer(&calls, "status-format\n", "", "")
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
	v := rosterCleanupFormatViewer(&calls, "status-format[0] custom\n", "", "")
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

func TestViewerRosterCleanupClearsOwnedFormatZeroBeforeEmptyArray(t *testing.T) {
	var calls [][]string
	v := rosterCleanupFormatViewer(&calls, "status-format\n", "1", rosterTestFormat0Value)
	v.UpdateRoster(nil, time.Now())

	assertRosterCallSequence(t, calls, [][]string{
		{"tmux", "-L", "leo", "set-option", "-u", "-t", "=leo-worker:", "status-format[1]"},
		{"tmux", "-L", "leo", "set-option", "-u", "-t", "=leo-worker:", "status-format[0]"},
		{"tmux", "-L", "leo", "show-options", "-t", "=leo-worker:", "status-format"},
		{"tmux", "-L", "leo", "set-option", "-u", "-t", "=leo-worker:", "status-format"},
		{"tmux", "-L", "leo", "set-option", "-u", "-t", "=leo-worker:", rosterFormat0Marker},
		{"tmux", "-L", "leo", "set-option", "-u", "-t", "=leo-worker:", rosterFormat0ValueMarker},
	})
}

func TestViewerRosterCleanupPreservesUserEditedOwnedFormatZero(t *testing.T) {
	var calls [][]string
	v := &Viewer{TmuxPath: "tmux", ExecCommand: func(name string, args ...string) *exec.Cmd {
		calls = append(calls, append([]string{name}, args...))
		switch {
		case containsArg(args, "show-options") && containsArg(args, "status-format[0]"):
			return exec.Command("printf", "user format\n")
		case containsArg(args, "show-options") && containsArg(args, "status-interval") && !containsArg(args, "-A"):
			return exec.Command("printf", "")
		case containsArg(args, "show-options"):
			return exec.Command("printf", "2\n")
		default:
			return exec.Command("true")
		}
	}}
	v.defaults()
	v.clearRosterState("leo-worker", rosterSessionState{format0Marked: true, format0Value: rosterTestFormat0Value})
	assertRosterCallSequence(t, calls, [][]string{
		{"tmux", "-L", "leo", "show-options", "-t", "=leo-worker:", "-v", "status-format[0]"},
	})
	for _, option := range []string{"status-format[0]", "status-format"} {
		if countRosterUnsets(calls, option) != 0 {
			t.Fatalf("cleared user-edited %s: %#v", option, calls)
		}
	}
	if countRosterUnsets(calls, rosterFormat0Marker) != 1 {
		t.Fatalf("did not clear format ownership marker: %#v", calls)
	}
}

func TestViewerRosterCopiesGlobalFormatZeroOnlyWhenSessionFormatIsUnset(t *testing.T) {
	for _, tc := range []struct {
		name        string
		localFormat string
		want        [][]string
	}{
		{
			name: "copies inherited format",
			want: [][]string{
				{"tmux", "-L", "leo", "show-options", "-t", "=leo-worker:", "status-format"},
				{"tmux", "-L", "leo", "show-options", "-g", "-v", "status-format[0]"},
				{"tmux", "-L", "leo", "set-option", "-t", "=leo-worker:", "@leo_roster_format0_owned", "1"},
				{"tmux", "-L", "leo", "set-option", "-t", "=leo-worker:", rosterFormat0ValueMarker, rosterTestFormat0Value},
				{"tmux", "-L", "leo", "set-option", "-t", "=leo-worker:", "status-format[0]", rosterTestFormat0Value},
				{"tmux", "-L", "leo", "set-option", "-t", "=leo-worker:", "status-format[1]", rosterFormat},
			},
		},
		{
			name:        "preserves session format",
			localFormat: "status-format[0] custom\n",
			want: [][]string{
				{"tmux", "-L", "leo", "show-options", "-t", "=leo-worker:", "status-format"},
				{"tmux", "-L", "leo", "set-option", "-t", "=leo-worker:", "status-format[1]", rosterFormat},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls [][]string
			v := &Viewer{TmuxPath: "tmux", ExecCommand: func(name string, args ...string) *exec.Cmd {
				calls = append(calls, append([]string{name}, args...))
				switch {
				case containsArg(args, "status-format") && containsArg(args, "show-options") && containsArg(args, "-t"):
					return exec.Command("printf", "%s", tc.localFormat)
				case containsArg(args, "status-format[0]") && containsArg(args, "show-options"):
					return exec.Command("printf", "%s\n", rosterTestFormat0Value)
				case containsArg(args, "status-interval") && containsArg(args, "show-options") && !containsArg(args, "-A"):
					return exec.Command("printf", "")
				case containsArg(args, "show-options"):
					return exec.Command("printf", "1\n")
				default:
					return exec.Command("true")
				}
			}}
			v.defaults()
			v.applyRoster("leo-worker", "roster", rosterSessionState{})
			assertRosterCallSequence(t, calls, tc.want)
		})
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
			if containsArg(args, "show-options") && containsArg(args, "status-interval") && !containsArg(args, "-A") {
				return exec.Command("printf", "")
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
		case containsArg(args, "show-options") && containsArg(args, "status-interval") && !containsArg(args, "-A"):
			return exec.Command("printf", "")
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
	format0Output := rosterFormat0ValueFromTmux(t)
	var calls [][]string
	v := &Viewer{TmuxPath: "tmux", ExecCommand: func(name string, args ...string) *exec.Cmd {
		calls = append(calls, append([]string{name}, args...))
		switch {
		case containsArg(args, "list-panes"):
			return exec.Command("printf", "")
		case containsArg(args, "list-sessions"):
			return exec.Command("printf", "%s", "leo-worker\t$1\t1\t1\t\t1\told roster\n")
		case containsArg(args, "show-options") && containsArg(args, rosterFormat0ValueMarker):
			return exec.Command("printf", "%s", format0Output)
		case containsArg(args, "show-options") && containsArg(args, "status-format[0]"):
			return exec.Command("printf", "%s\n", rosterTestFormat0Value)
		case containsArg(args, "show-options") && containsArg(args, "status-interval") && !containsArg(args, "-A"):
			return exec.Command("printf", "")
		case containsArg(args, "show-options"):
			return exec.Command("printf", "2\n")
		default:
			return exec.Command("true")
		}
	}}
	v.UpdateRoster(nil, time.Now())
	for _, option := range []string{"status-format[1]", "status-format[0]", "@leo_roster", "status", rosterFormat0Marker, rosterFormat0ValueMarker, rosterStatusMarker, rosterMarker} {
		if countRosterUnsets(calls, option) != 1 {
			t.Fatalf("restart cleanup %s calls = %#v", option, calls)
		}
	}
}

func rosterFormat0ValueFromTmux(t *testing.T) string {
	t.Helper()
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not available")
	}
	session := fmt.Sprintf("scratch-%d", os.Getpid())
	if out, err := exec.Command(tmuxPath, "-L", "leo", "new-session", "-d", "-s", session).CombinedOutput(); err != nil {
		t.Fatalf("creating scratch tmux session: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command(tmuxPath, "-L", "leo", "kill-session", "-t", session).Run() })
	if out, err := exec.Command(tmuxPath, "-L", "leo", "set-option", "-t", session+":", rosterFormat0ValueMarker, rosterTestFormat0Value).CombinedOutput(); err != nil {
		t.Fatalf("setting scratch tmux option: %v: %s", err, out)
	}
	line, err := exec.Command(tmuxPath, "-L", "leo", "show-options", "-t", session+":", rosterFormat0ValueMarker).Output()
	if err != nil {
		t.Fatalf("reading scratch tmux option: %v", err)
	}
	value, err := rosterOptionValue(rosterFormat0ValueMarker, string(line))
	if err != nil || value != rosterTestFormat0Value {
		t.Fatalf("unexpected scratch tmux option output %q", line)
	}
	return string(line)
}

func TestViewerRosterRestartAfterFailedStatusCleanupRetainsOwnership(t *testing.T) {
	var firstCalls [][]string
	first := restartCleanupViewer(&firstCalls, "leo-worker\t$1\t1\t1\t\t\told\n", func(args []string) *exec.Cmd {
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
	restarted := restartCleanupViewer(&restartCalls, "leo-worker\t$1\t\t1\t\t\t\n", func([]string) *exec.Cmd { return exec.Command("true") })
	restarted.UpdateRoster(nil, time.Now())
	if countRosterUnsets(restartCalls, "status") != 1 || countRosterUnsets(restartCalls, rosterStatusMarker) != 1 {
		t.Fatalf("restart did not finish owned status cleanup: %#v", restartCalls)
	}
}

func TestViewerRosterRestartAfterFailedFormatCleanupRetainsOwnership(t *testing.T) {
	var firstCalls [][]string
	first := restartCleanupViewer(&firstCalls, "leo-worker\t$1\t1\t\t\t\told\n", func(args []string) *exec.Cmd {
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
	restarted := restartCleanupViewer(&restartCalls, "leo-worker\t$1\t1\t\t\t\t\n", func([]string) *exec.Cmd { return exec.Command("true") })
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
		case containsArg(args, "show-options") && containsArg(args, "status-interval") && !containsArg(args, "-A"):
			return exec.Command("printf", "1\n")
		case containsArg(args, "show-options"):
			return exec.Command("printf", "2\n")
		default:
			return action(args)
		}
	}}
}

func rosterCleanupFormatViewer(calls *[][]string, statusFormat, format0Owned, format0Value string) *Viewer {
	return &Viewer{TmuxPath: "tmux", ExecCommand: func(name string, args ...string) *exec.Cmd {
		*calls = append(*calls, append([]string{name}, args...))
		switch {
		case containsArg(args, "list-panes"):
			return exec.Command("printf", "")
		case containsArg(args, "list-sessions"):
			return exec.Command("printf", "%s", "leo-worker\t$1\t1\t\t\t"+format0Owned+"\told roster\n")
		case containsArg(args, "show-options") && containsArg(args, rosterFormat0ValueMarker):
			return exec.Command("printf", "%s %s\n", rosterFormat0ValueMarker, format0Value)
		case containsArg(args, "show-options") && containsArg(args, "status-format[0]"):
			return exec.Command("printf", "%s\n", format0Value)
		case containsArg(args, "show-options") && containsArg(args, "status-format"):
			return exec.Command("printf", "%s", statusFormat)
		case containsArg(args, "show-options") && containsArg(args, "status-interval") && !containsArg(args, "-A"):
			return exec.Command("printf", "")
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
		case containsArg(args, "show-options") && containsArg(args, "status-interval") && !containsArg(args, "-A"):
			return exec.Command("printf", "")
		case containsArg(args, "show-options"):
			return exec.Command("printf", "1\n")
		default:
			return action(args)
		}
	}, windowIDs: map[string]string{"d-a": "@7"}}
}

func rosterIntervalViewer(calls *[][]string, localStatusInterval, effectiveStatusInterval string, action func([]string) *exec.Cmd) *Viewer {
	v := rosterTestViewer(calls, action)
	legacy := v.ExecCommand
	v.ExecCommand = func(name string, args ...string) *exec.Cmd {
		if containsArg(args, "show-options") && containsArg(args, "status-interval") {
			*calls = append(*calls, append([]string{name}, args...))
			if containsArg(args, "-A") {
				return exec.Command("printf", "%s", effectiveStatusInterval)
			}
			return exec.Command("printf", "%s", localStatusInterval)
		}
		return legacy(name, args...)
	}
	return v
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
