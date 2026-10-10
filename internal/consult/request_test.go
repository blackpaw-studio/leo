package consult

import (
	"strings"
	"testing"
)

func TestDispatchPreambleReleaseGuidance(t *testing.T) {
	if !strings.Contains(requestPrompt(Request{Prompt: "work"}), "orchestrator has finished with you, it releases this pane") {
		t.Fatal("release guidance missing")
	}
}

func TestDispatchPreambleBackgroundWorkGuidance(t *testing.T) {
	got := requestPrompt(Request{Prompt: "work"})
	for _, want := range []string{"stop any background shells, monitors or wakeups you started", "say so explicitly"} {
		if !strings.Contains(got, want) {
			t.Fatalf("background-work guidance %q missing: %s", want, got)
		}
	}
}

func TestDispatchPreambleAllowsNestedSubagents(t *testing.T) {
	got := requestPrompt(Request{Prompt: "work"})
	for _, banned := range []string{"Do not spawn", "own code review"} {
		if strings.Contains(got, banned) {
			t.Fatalf("dispatch preamble forbids nesting via %q: %s", banned, got)
		}
	}
	if !strings.Contains(got, "You may dispatch your own subagents") {
		t.Fatalf("nesting permission missing: %s", got)
	}
	if !strings.Contains(got, "Do not self-certify your work; the orchestrator reviews it.") {
		t.Fatalf("self-certify guidance missing: %s", got)
	}
}

func TestRequestPrompt(t *testing.T) {
	tests := []struct {
		name string
		req  Request
		want string
	}{
		{name: "dispatch", req: Request{Prompt: "implement it"}, want: dispatchPreamble + " implement it"},
		{name: "consult retains existing preamble", req: Request{Prompt: "inspect it", Preamble: true}, want: preamble + "\n\ninspect it"},
		{name: "isolated dispatch names its worktree", req: Request{Prompt: "review it", Cwd: "/wt/d-1", Isolation: "worktree"},
			want: dispatchPreamble + " You are running in a throwaway Git worktree at /wt/d-1, checked out at the caller's committed HEAD: uncommitted changes in the caller's tree are not visible here, and anything you write stays in this worktree. review it"},
		{name: "isolated consult names its worktree", req: Request{Prompt: "inspect it", Cwd: "/wt/d-2", Isolation: "worktree", Preamble: true},
			want: preamble + " You are running in a throwaway Git worktree at /wt/d-2, checked out at the caller's committed HEAD: uncommitted changes in the caller's tree are not visible here, and anything you write stays in this worktree.\n\ninspect it"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := requestPrompt(tt.req); got != tt.want {
				t.Fatalf("requestPrompt() = %q, want %q", got, tt.want)
			}
		})
	}
}
