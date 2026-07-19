package tmux

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

// paneListOutput renders a minimal list-panes -F "#{pane_id}" response
// carrying a single pane id.
func paneListOutput(paneID string) string {
	return paneID + "\n"
}

// TestInjectPromptUsesResolvedPaneTarget proves injectPrompt targets the
// concrete pane ResolvePane reports (e.g. "%42") for every send-keys,
// capture-pane, and paste-buffer call — not PaneTarget's active-pane
// selector — so a split session doesn't misdirect the injection.
func TestInjectPromptUsesResolvedPaneTarget(t *testing.T) {
	const resolvedPane = "%42"
	var got [][]string
	orig := execCommand
	defer func() { execCommand = orig }()
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		got = append(got, append([]string{name}, args...))
		switch {
		case len(args) >= 3 && args[2] == "list-panes":
			return exec.Command("printf", "%s", paneListOutput(resolvedPane))
		case len(args) >= 3 && args[2] == "capture-pane":
			return exec.Command("printf", "%s", paneWithInput(inputProbe)+"hello\n")
		default:
			return exec.Command("true")
		}
	}

	if err := injectPrompt(context.Background(), "tmux", "leo-agent-foo", "hello", 5, time.Millisecond); err != nil {
		t.Fatalf("injectPrompt: %v", err)
	}

	for _, sub := range []string{"send-keys", "capture-pane", "paste-buffer"} {
		found := false
		for _, c := range got {
			if len(c) < 4 || c[3] != sub {
				continue
			}
			found = true
			target := ""
			for i, a := range c {
				if a == "-t" && i+1 < len(c) {
					target = c[i+1]
					break
				}
			}
			if target != resolvedPane {
				t.Fatalf("%s call targeted %q, want resolved pane %q: %#v", sub, target, resolvedPane, c)
			}
		}
		if !found {
			t.Fatalf("expected at least one %s call, got none: %#v", sub, got)
		}
	}
}

// TestInjectPromptProbeRetriesWhenResolvePaneFails proves a ResolvePane
// failure during the readiness probe is treated as "not ready" (wait the
// poll interval, retry within the existing budget) rather than as a hard
// failure — mirroring the existing failed-probe-send retry behavior.
func TestInjectPromptProbeRetriesWhenResolvePaneFails(t *testing.T) {
	const resolvedPane = "%9"
	var got [][]string
	listPanesCalls := 0
	orig := execCommand
	defer func() { execCommand = orig }()
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		got = append(got, append([]string{name}, args...))
		switch {
		case len(args) >= 3 && args[2] == "list-panes":
			listPanesCalls++
			if listPanesCalls < 3 {
				return exec.Command("false")
			}
			return exec.Command("printf", "%s", paneListOutput(resolvedPane))
		case len(args) >= 3 && args[2] == "capture-pane":
			return exec.Command("printf", "%s", paneWithInput(inputProbe)+"body\n")
		default:
			return exec.Command("true")
		}
	}

	if err := injectPrompt(context.Background(), "tmux", "leo-agent-foo", "body", 10, time.Millisecond); err != nil {
		t.Fatalf("injectPrompt: %v", err)
	}
	if listPanesCalls < 3 {
		t.Fatalf("expected the probe to retry list-panes at least 3 times, got %d", listPanesCalls)
	}
	if n := countSub(got, "paste-buffer"); n != 1 {
		t.Fatalf("body must be pasted exactly once, got %d paste-buffer calls: %#v", n, got)
	}
	last := got[len(got)-1]
	if last[3] != "send-keys" || last[len(last)-1] != "Enter" {
		t.Fatalf("last call must be submit Enter, got %#v", last)
	}
}
